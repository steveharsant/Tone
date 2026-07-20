package check

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// anchored is a raw suggestion successfully located in its segment.
type anchored struct {
	raw                RawSuggestion
	byteStart, byteEnd int // within the segment text
}

// anchorAll locates each raw suggestion's Original inside segText and returns
// only the ones that anchor cleanly. Resolution order per suggestion:
//
//  1. exact substring match (first occurrence not claimed by an earlier one)
//  2. exact match of the whitespace-trimmed snippet
//  3. fuzzy match: ASCII-case-insensitive, whitespace-run-flexible
//
// Anything else — hallucinated text, self-identical replacements, overlaps —
// is dropped. Precision over recall: a wrong underline is worse than a
// missing one.
func anchorAll(segText string, raws []RawSuggestion) []anchored {
	// Longer originals anchor first: when the model emits both a fragment
	// fix ("sum"→"some") and the complete fix ("sum thing"→"something"),
	// the complete one must claim the span — emission order is arbitrary.
	ordered := make([]RawSuggestion, len(raws))
	copy(ordered, raws)
	sort.SliceStable(ordered, func(i, j int) bool {
		return len(ordered[i].Original) > len(ordered[j].Original)
	})
	raws = ordered

	var out []anchored
	var claimed [][2]int

	overlaps := func(s, e int) bool {
		for _, c := range claimed {
			if s < c[1] && e > c[0] {
				return true
			}
		}
		return false
	}

	for _, raw := range raws {
		orig := raw.Original
		if strings.TrimSpace(orig) == "" || raw.Replacement == orig {
			continue
		}
		if malformedReplacement(raw.Replacement) {
			continue
		}
		s, e, ok := findUnclaimed(segText, orig, overlaps)
		if !ok {
			trimmed := strings.TrimSpace(orig)
			if trimmed != orig && trimmed != "" {
				s, e, ok = findUnclaimed(segText, trimmed, overlaps)
			}
		}
		if !ok {
			s, e, ok = fuzzyFind(segText, strings.TrimSpace(orig), overlaps)
		}
		if !ok {
			continue
		}
		// A fuzzy/trimmed anchor may differ from the model's snippet; if the
		// text at the anchor already equals the replacement, there is no edit.
		if segText[s:e] == raw.Replacement {
			continue
		}
		s, e = expandMergedWords(segText, raw, s, e, overlaps)
		claimed = append(claimed, [2]int{s, e})
		out = append(out, anchored{raw: raw, byteStart: s, byteEnd: e})
	}
	return out
}

// expandMergedWords repairs a sloppy merged-word span from the model:
// original "sum" with replacement "something" while the document continues
// "sum thing" means the model intended to merge both words — applying it
// verbatim would yield "something thing". If the replacement ends with the
// next document word (or begins with the previous one) AND a meaningful
// remainder is left over, the span grows to cover that word. The remainder
// requirement keeps duplicate-word fixes ("teh the"→"the") from expanding.
func expandMergedWords(segText string, raw RawSuggestion, s, e int, overlaps func(s, e int) bool) (int, int) {
	repl := strings.ToLower(raw.Replacement)

	// Forward: replacement swallows the following word.
	if e < len(segText) && segText[e] == ' ' {
		rest := segText[e+1:]
		wEnd := strings.IndexFunc(rest, func(r rune) bool { return !isWordRune(r) })
		if wEnd == -1 {
			wEnd = len(rest)
		}
		if matched := swallowLen(repl, strings.ToLower(rest[:wEnd]), false); matched > 0 {
			if remainder := strings.TrimSpace(repl[:len(repl)-matched]); len(remainder) >= 2 {
				if ne := e + 1 + wEnd; !overlaps(s, ne) {
					e = ne
				}
			}
		}
	}
	// Backward: replacement swallows the preceding word.
	if s > 0 && segText[s-1] == ' ' {
		head := segText[:s-1]
		wStart := strings.LastIndexFunc(head, func(r rune) bool { return !isWordRune(r) }) + 1
		if matched := swallowLen(repl, strings.ToLower(head[wStart:]), true); matched > 0 {
			if remainder := strings.TrimSpace(repl[matched:]); len(remainder) >= 2 {
				if ns := wStart; !overlaps(ns, e) {
					s = ns
				}
			}
		}
	}
	return s, e
}

// swallowLen reports how many bytes at the edge of repl correspond to the
// neighbor word w, tolerating small misspellings — the swallowed neighbor is
// often the typo'd half ("sum" vs the "some" inside "something"). Returns
// the matched length in repl, or 0 for no match.
func swallowLen(repl, w string, prefix bool) int {
	if w == "" {
		return 0
	}
	for _, l := range []int{len(w), len(w) + 1, len(w) - 1} {
		if l < 1 || l >= len(repl) { // l == len(repl) would leave no remainder
			continue
		}
		cand := repl[len(repl)-l:]
		if prefix {
			cand = repl[:l]
		}
		if cand == w {
			return l
		}
		// Fuzzy only for words long enough to make a typo distinguishable.
		if len(w) >= 3 && editDistance(cand, w) <= 1 {
			return l
		}
	}
	return 0
}

// editDistance is a plain Levenshtein for short words (neighbor-word sized).
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// malformedReplacement rejects tokenizer debris the model occasionally
// emits, like "do n't" — a space directly before an apostrophe-word is
// never valid English output.
func malformedReplacement(repl string) bool {
	// "do n't": the n't suffix split off as its own token.
	if strings.Contains(repl, " n't") || strings.Contains(repl, " n’t") {
		return true
	}
	// " 's", " 're", " 'll", …: apostrophe-suffix tokens after a space.
	for _, marker := range []string{" '", " ’"} {
		idx := 0
		for {
			i := strings.Index(repl[idx:], marker)
			if i < 0 {
				break
			}
			after := repl[idx+i+len(marker):]
			if r, _ := utf8.DecodeRuneInString(after); unicode.IsLetter(r) {
				return true
			}
			idx += i + len(marker)
		}
	}
	return false
}

// findUnclaimed returns the first exact occurrence of needle in hay that
// sits on word boundaries and does not overlap an already-claimed range.
func findUnclaimed(hay, needle string, overlaps func(s, e int) bool) (int, int, bool) {
	from := 0
	for {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return 0, 0, false
		}
		s := from + i
		e := s + len(needle)
		if !overlaps(s, e) && onWordBoundaries(hay, s, e) {
			return s, e, true
		}
		from = s + 1
	}
}

// onWordBoundaries rejects matches that start or end mid-word — e.g. the
// model suggesting "can"→"cannot" against the text "can't" would otherwise
// anchor inside the contraction and corrupt it on accept. Apostrophes count
// as word-internal so contractions are treated as single words.
func onWordBoundaries(hay string, s, e int) bool {
	if s > 0 {
		first, _ := utf8.DecodeRuneInString(hay[s:])
		prev, _ := utf8.DecodeLastRuneInString(hay[:s])
		if isWordRune(first) && isWordRune(prev) {
			return false
		}
	}
	if e < len(hay) {
		last, _ := utf8.DecodeLastRuneInString(hay[:e])
		next, _ := utf8.DecodeRuneInString(hay[e:])
		if isWordRune(last) && isWordRune(next) {
			return false
		}
	}
	return true
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’'
}

// fuzzyFind scans hay for needle with two relaxations: ASCII letters compare
// case-insensitively, and any whitespace run matches any whitespace run.
// Returns the matched byte range in hay.
func fuzzyFind(hay, needle string, overlaps func(s, e int) bool) (int, int, bool) {
	if needle == "" {
		return 0, 0, false
	}
	for start := 0; start < len(hay); start++ {
		if !utf8.RuneStart(hay[start]) {
			continue
		}
		if end, ok := fuzzyMatchAt(hay, needle, start); ok && !overlaps(start, end) && onWordBoundaries(hay, start, end) {
			return start, end, true
		}
	}
	return 0, 0, false
}

func fuzzyMatchAt(hay, needle string, start int) (int, bool) {
	h, n := start, 0
	for n < len(needle) {
		if h >= len(hay) {
			return 0, false
		}
		hr, hs := utf8.DecodeRuneInString(hay[h:])
		nr, ns := utf8.DecodeRuneInString(needle[n:])
		switch {
		case unicode.IsSpace(nr):
			if !unicode.IsSpace(hr) {
				return 0, false
			}
			for h < len(hay) {
				r, s := utf8.DecodeRuneInString(hay[h:])
				if !unicode.IsSpace(r) {
					break
				}
				h += s
			}
			for n < len(needle) {
				r, s := utf8.DecodeRuneInString(needle[n:])
				if !unicode.IsSpace(r) {
					break
				}
				n += s
			}
		case asciiLower(hr) == asciiLower(nr):
			h += hs
			n += ns
		default:
			return 0, false
		}
	}
	return h, true
}

func asciiLower(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}
