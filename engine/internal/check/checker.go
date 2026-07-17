package check

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

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
func (c *Checker) Check(ctx context.Context, text string, categories []string) ([]Suggestion, Stats, error) {
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
		key := CacheKey(c.model, categories, seg.Text)
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
			raws, err := c.checkSegment(ctx, seg.Text, categories)
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
	allowed := make(map[string]bool, len(categories))
	for _, cat := range categories {
		allowed[cat] = true
	}

	var out []Suggestion
	for i, seg := range segs {
		for _, a := range anchorAll(seg.Text, rawsBySeg[i]) {
			cat := strings.ToLower(strings.TrimSpace(a.raw.Category))
			if !allowed[cat] {
				continue
			}
			bs := seg.ByteStart + a.byteStart
			be := seg.ByteStart + a.byteEnd
			out = append(out, Suggestion{
				ID:   newID(),
				Span: Span{Start: conv.ToUTF16(bs), End: conv.ToUTF16(be)},
				// Ground truth is the document, not the model's echo.
				Original:    text[bs:be],
				Replacement: a.raw.Replacement,
				Category:    cat,
				Rule:        a.raw.Rule,
				Explanation: a.raw.Explanation,
				Confidence:  defaultConfidence(cat),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Span.Start < out[j].Span.Start })
	return out, stats, nil
}

func (c *Checker) checkSegment(ctx context.Context, segText string, categories []string) ([]RawSuggestion, error) {
	content, err := c.prov.Complete(ctx, provider.Request{
		Model:       c.model,
		Messages:    buildMessages(segText, categories),
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
