package check

import (
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
		claimed = append(claimed, [2]int{s, e})
		out = append(out, anchored{raw: raw, byteStart: s, byteEnd: e})
	}
	return out
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
