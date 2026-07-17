package check

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Segment is a sentence-sized slice of the document. ByteStart is the byte
// offset of Text within the original document string.
type Segment struct {
	Text      string
	ByteStart int
}

// maxSegmentBytes hard-caps a segment so a pathological wall of text without
// sentence terminators still fits comfortably in a small model's context.
const maxSegmentBytes = 2000

// Split breaks text into sentence-sized segments. Boundaries are newlines and
// sentence terminators (. ! ?) followed by whitespace, with trailing closing
// quotes/brackets kept attached to the sentence. Abbreviation false-splits
// (e.g. "Mr. Smith") are tolerated: they only shrink model context, never
// corrupt offsets, because anchoring happens per segment against real text.
func Split(text string) []Segment {
	var segs []Segment
	start := 0

	flush := func(end int) {
		seg := text[start:end]
		// Trim whitespace but keep ByteStart pointing at the first kept byte.
		trimmedLeft := strings.TrimLeftFunc(seg, unicode.IsSpace)
		offset := len(seg) - len(trimmedLeft)
		trimmed := strings.TrimRightFunc(trimmedLeft, unicode.IsSpace)
		if trimmed != "" {
			segs = append(segs, Segment{Text: trimmed, ByteStart: start + offset})
		}
		start = end
	}

	i := 0
	for i < len(text) {
		r, size := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == '\n':
			flush(i)
			i += size
			start = i
		case r == '.' || r == '!' || r == '?':
			j := i + size
			// Attach any run of closing punctuation to this sentence.
			for j < len(text) {
				r2, s2 := utf8.DecodeRuneInString(text[j:])
				if isClosing(r2) || r2 == '.' || r2 == '!' || r2 == '?' {
					j += s2
					continue
				}
				break
			}
			// Only a real boundary if followed by whitespace or end-of-text.
			if j >= len(text) {
				flush(j)
				i = j
			} else if r2, _ := utf8.DecodeRuneInString(text[j:]); unicode.IsSpace(r2) {
				flush(j)
				i = j
			} else {
				i = j
			}
		default:
			i += size
		}
		// Hard cap: split at the last whitespace inside the window.
		if i-start > maxSegmentBytes {
			cut := strings.LastIndexFunc(text[start:i], unicode.IsSpace)
			if cut <= 0 {
				cut = i - start
			}
			flush(start + cut)
		}
	}
	flush(len(text))
	return segs
}

func isClosing(r rune) bool {
	switch r {
	case '"', '\'', ')', ']', '}', '»', '”', '’':
		return true
	}
	return false
}
