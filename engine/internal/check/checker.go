package check

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/steveharsant/tone/engine/internal/provider"
)

// Checker runs the full pipeline: segment → cache lookup → model call →
// parse → anchor → document-level UTF-16 spans.
type Checker struct {
	prov        provider.Provider
	model       string
	cache       *Cache
	concurrency int
}

func New(p provider.Provider, model string, cache *Cache) *Checker {
	return &Checker{
		prov:  p,
		model: model,
		cache: cache,
		// Local Ollama serializes requests by default; 2 in flight keeps the
		// pipe full without queue blowout, and is polite to cloud APIs too.
		concurrency: 2,
	}
}

type Stats struct {
	Segments  int `json:"segments"`
	CacheHits int `json:"cache_hits"`
}

// Check analyzes text and returns anchored suggestions ordered by position.
func (c *Checker) Check(ctx context.Context, text string, opts Options) ([]Suggestion, Stats, error) {
	segs := Split(text)
	stats := Stats{Segments: len(segs)}
	if len(segs) == 0 {
		return nil, stats, nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	rawsBySeg := make([][]RawSuggestion, len(segs))
	sem := make(chan struct{}, c.concurrency)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)

	for i, seg := range segs {
		key := CacheKey(c.model, opts.key(), seg.Text)
		if raws, ok := c.cache.Get(key); ok {
			rawsBySeg[i] = raws
			stats.CacheHits++
			continue
		}
		wg.Add(1)
		go func(i int, seg Segment, key string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			raws, err := c.checkSegment(ctx, seg.Text, opts)
			if err != nil {
				mu.Lock()
				if firstErr == nil && ctx.Err() == nil {
					firstErr = fmt.Errorf("segment %d: %w", i, err)
					cancel()
				}
				mu.Unlock()
				return
			}
			c.cache.Put(key, raws)
			rawsBySeg[i] = raws
		}(i, seg, key)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, stats, firstErr
	}

	conv := NewU16Converter(text)
	allowed := opts.AllowedCategories()
	disabled := make(map[string]bool, len(opts.DisabledRules))
	for _, r := range opts.DisabledRules {
		disabled[normalizeRule(r)] = true
	}

	var out []Suggestion
	for i, seg := range segs {
		for _, a := range anchorAll(seg.Text, rawsBySeg[i]) {
			cat := strings.ToLower(strings.TrimSpace(a.raw.Category))
			if !allowed[cat] {
				continue
			}
			if disabled[normalizeRule(a.raw.Rule)] {
				continue
			}
			bs := seg.ByteStart + a.byteStart
			be := seg.ByteStart + a.byteEnd
			out = append(out, Suggestion{
				ID:   newID(),
				Span: Span{Start: conv.ToUTF16(bs), End: conv.ToUTF16(be)},
				// Ground truth is the document, not the model's echo.
				Original:     text[bs:be],
				Replacement:  a.raw.Replacement,
				Alternatives: cleanAlternatives(a.raw, text[bs:be]),
				Category:     cat,
				Rule:         a.raw.Rule,
				Explanation:  a.raw.Explanation,
				Confidence:   defaultConfidence(cat),
			})
		}
	}
	// Deterministic rule: sentences start with a capital letter. The model's
	// attention on this is unreliable; the engine checks it itself.
	if opts.Grammar && !disabled[normalizeRule("capitalization")] {
		out = append(out, capitalizationSuggestions(text, segs, conv, out)...)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Span.Start < out[j].Span.Start })
	return out, stats, nil
}

// capitalizationSuggestions flags segments whose first letter is lowercase.
// Skips mixed-case first words (iPhone, eBay) and spans the model already
// covers.
func capitalizationSuggestions(text string, segs []Segment, conv *U16Converter, existing []Suggestion) []Suggestion {
	var out []Suggestion
	for _, seg := range segs {
		// Step over leading quotes/brackets to the first letter.
		i := 0
		var first rune
		var size int
		for i < len(seg.Text) {
			r, s := utf8.DecodeRuneInString(seg.Text[i:])
			if unicode.IsLetter(r) {
				first, size = r, s
				break
			}
			if !strings.ContainsRune(`"'“‘([`, r) {
				break // starts with a digit, emoji, etc. — not our business
			}
			i += s
		}
		if first == 0 || !unicode.IsLower(first) {
			continue
		}
		// Mixed-case first word (iPhone) is intentional.
		rest := seg.Text[i+size:]
		wordEnd := strings.IndexFunc(rest, func(r rune) bool { return !isWordRune(r) })
		if wordEnd == -1 {
			wordEnd = len(rest)
		}
		if strings.IndexFunc(rest[:wordEnd], unicode.IsUpper) != -1 {
			continue
		}

		bs := seg.ByteStart + i
		be := bs + size
		start, end := conv.ToUTF16(bs), conv.ToUTF16(be)
		clash := false
		for _, s := range existing {
			if start < s.Span.End && end > s.Span.Start {
				clash = true
				break
			}
		}
		if clash {
			continue
		}
		out = append(out, Suggestion{
			ID:          newID(),
			Span:        Span{Start: start, End: end},
			Original:    string(first),
			Replacement: string(unicode.ToUpper(first)),
			Category:    CategoryCorrectness,
			Rule:        "capitalization",
			Explanation: "Sentences start with a capital letter.",
			Confidence:  1,
		})
	}
	return out
}

// Tier is one priority pass of a tiered check.
type Tier struct {
	Name string
	Opts Options
}

// TiersFor decomposes enabled checks into priority-ordered passes: spelling
// first (fastest, most wanted), then grammar, clarity, vocabulary, tone.
// Each tier is a lean single-purpose prompt, so time-to-first-suggestion
// beats one combined pass; per-tier cache keys fall out of Options.key().
func TiersFor(opts Options) []Tier {
	base := Options{DisabledRules: opts.DisabledRules, Language: opts.Language}
	var tiers []Tier
	add := func(name string, mod func(*Options)) {
		o := base
		mod(&o)
		tiers = append(tiers, Tier{Name: name, Opts: o})
	}
	if opts.Spelling {
		add("spelling", func(o *Options) { o.Spelling = true })
	}
	if opts.Grammar {
		add("grammar", func(o *Options) { o.Grammar = true })
	}
	if opts.Clarity {
		add("clarity", func(o *Options) { o.Clarity = true })
	}
	if opts.Vocabulary {
		add("vocabulary", func(o *Options) { o.Vocabulary = true })
	}
	if opts.Tone || len(opts.StyleRules) > 0 {
		add("tone", func(o *Options) {
			o.Tone = opts.Tone
			o.ToneTarget = opts.ToneTarget
			o.StyleRules = opts.StyleRules
		})
	}
	return tiers
}

// CheckTiered runs the enabled checks as concurrent priority passes,
// emitting each tier's suggestions the moment that pass completes. With a
// GPU backend (OLLAMA_NUM_PARALLEL > 1) the passes genuinely overlap; on a
// serialized backend they queue and arrive in near-priority order anyway
// since spelling is submitted first. Emission order is completion order —
// the extension merges per-tier, so ordering doesn't matter to it.
func (c *Checker) CheckTiered(ctx context.Context, text string, opts Options, emit func(tier string, sugs []Suggestion, stats Stats)) error {
	tiers := TiersFor(opts)
	if len(tiers) == 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		emitMu   sync.Mutex
		errMu    sync.Mutex
		firstErr error
	)
	for _, t := range tiers {
		wg.Add(1)
		go func(t Tier) {
			defer wg.Done()
			sugs, stats, err := c.Check(ctx, text, t.Opts)
			if err != nil {
				errMu.Lock()
				if firstErr == nil && ctx.Err() == nil {
					firstErr = fmt.Errorf("%s pass: %w", t.Name, err)
					cancel()
				}
				errMu.Unlock()
				return
			}
			if sugs == nil {
				sugs = []Suggestion{}
			}
			emitMu.Lock()
			emit(t.Name, sugs, stats)
			emitMu.Unlock()
		}(t)
	}
	wg.Wait()
	return firstErr
}

// cleanAlternatives dedupes the model's alternative corrections against the
// primary replacement and the original text, capping at 2.
func cleanAlternatives(raw RawSuggestion, original string) []string {
	var out []string
	for _, alt := range raw.Alternatives {
		alt = strings.TrimSpace(alt)
		if alt == "" || alt == raw.Replacement || alt == original {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing == alt {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, alt)
		}
		if len(out) == 2 {
			break
		}
	}
	return out
}

// normalizeRule folds case and dash/space variants so "Subject Verb
// Agreement" blocks "subject-verb-agreement".
func normalizeRule(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	return strings.ReplaceAll(strings.ReplaceAll(r, " ", "-"), "_", "-")
}

func (c *Checker) checkSegment(ctx context.Context, segText string, opts Options) ([]RawSuggestion, error) {
	content, err := c.prov.Complete(ctx, provider.Request{
		Model:       c.model,
		Messages:    buildMessages(segText, opts),
		Temperature: 0.2,
		MaxTokens:   2000,
		JSONMode:    true,
	})
	if err != nil {
		return nil, err
	}
	// A model reply we can't parse is treated as "no suggestions" rather than
	// an error: dropping output beats surfacing garbage as underlines.
	return parseRaws(content), nil
}

// parseRaws tolerantly extracts suggestions from model output: it strips
// markdown fences, then tries the documented object shape, then a bare array.
func parseRaws(content string) []RawSuggestion {
	s := strings.TrimSpace(content)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}

	tryArray := func() ([]RawSuggestion, bool) {
		var arr []RawSuggestion
		if start, end := strings.Index(s, "["), strings.LastIndex(s, "]"); start >= 0 && end > start {
			if err := json.Unmarshal([]byte(s[start:end+1]), &arr); err == nil {
				return arr, true
			}
		}
		return nil, false
	}
	// A reply that opens with '[' is a bare array; the object path would
	// otherwise "succeed" on a fragment of it and return nothing.
	if strings.HasPrefix(s, "[") {
		if arr, ok := tryArray(); ok {
			return arr
		}
	}
	var wrapper struct {
		Suggestions []RawSuggestion `json:"suggestions"`
	}
	if start, end := strings.Index(s, "{"), strings.LastIndex(s, "}"); start >= 0 && end > start {
		if err := json.Unmarshal([]byte(s[start:end+1]), &wrapper); err == nil {
			return wrapper.Suggestions
		}
	}
	if arr, ok := tryArray(); ok {
		return arr
	}
	return nil
}
