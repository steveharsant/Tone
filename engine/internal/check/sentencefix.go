package check

import (
	"sort"
	"unicode/utf16"
)

// SentenceFix is one whole-sentence suggestion combining every individual
// fix inside that sentence — offered first when a sentence has 2+ issues so
// the writer can repair it in a single accept.
type SentenceFix struct {
	Span        Span   `json:"span"`
	Original    string `json:"original"`
	Replacement string `json:"replacement"`
	Count       int    `json:"count"`
}

// SentenceFixes groups suggestions by sentence and, for sentences with two
// or more, splices all replacements into the sentence text. Overlapping
// suggestions (tiers can duplicate a finding) are applied at most once,
// preferring the longer span.
func SentenceFixes(text string, sugs []Suggestion) []SentenceFix {
	if len(sugs) < 2 {
		return nil
	}
	conv := NewU16Converter(text)
	u := utf16.Encode([]rune(text))

	var out []SentenceFix
	for _, seg := range Split(text) {
		segStart := conv.ToUTF16(seg.ByteStart)
		segEnd := conv.ToUTF16(seg.ByteStart + len(seg.Text))

		var in []Suggestion
		for _, s := range sugs {
			if s.Span.Start >= segStart && s.Span.End <= segEnd {
				in = append(in, s)
			}
		}
		// Dedupe overlaps (cross-tier duplicates), longer span first.
		sort.Slice(in, func(i, j int) bool {
			li, lj := in[i].Span.End-in[i].Span.Start, in[j].Span.End-in[j].Span.Start
			if li != lj {
				return li > lj
			}
			return in[i].Span.Start < in[j].Span.Start
		})
		var apply []Suggestion
		for _, s := range in {
			clash := false
			for _, a := range apply {
				if s.Span.Start < a.Span.End && s.Span.End > a.Span.Start {
					clash = true
					break
				}
			}
			if !clash {
				apply = append(apply, s)
			}
		}
		if len(apply) < 2 {
			continue
		}

		// Splice right-to-left so earlier offsets stay valid.
		sort.Slice(apply, func(i, j int) bool { return apply[i].Span.Start > apply[j].Span.Start })
		window := append([]uint16(nil), u[segStart:segEnd]...)
		for _, s := range apply {
			rs, re := s.Span.Start-segStart, s.Span.End-segStart
			repl := utf16.Encode([]rune(s.Replacement))
			next := make([]uint16, 0, len(window)-(re-rs)+len(repl))
			next = append(next, window[:rs]...)
			next = append(next, repl...)
			next = append(next, window[re:]...)
			window = next
		}
		out = append(out, SentenceFix{
			Span:        Span{Start: segStart, End: segEnd},
			Original:    seg.Text,
			Replacement: string(utf16.Decode(window)),
			Count:       len(apply),
		})
	}
	return out
}
