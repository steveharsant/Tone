// Package check implements Tone's suggestion pipeline: it segments text,
// asks the configured LLM provider for edits per segment, then anchors each
// edit back onto the source text with exact offsets.
//
// Load-bearing invariant: the model NEVER emits offsets. It returns
// (original, replacement) snippet pairs; the engine locates each snippet in
// the source itself and silently drops anything it cannot anchor. That
// validation step is what makes small local models reliable enough to drive
// precise DOM underlines.
package check

import (
	"crypto/rand"
	"encoding/hex"
)

// Span is measured in UTF-16 code units over the whole document, matching
// JavaScript string/DOM offset semantics so the extension can use it as-is.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

const (
	CategoryCorrectness = "correctness"
	CategoryClarity     = "clarity"
	CategoryEngagement  = "engagement"
	CategoryDelivery    = "delivery"
)

type Suggestion struct {
	ID          string  `json:"id"`
	Span        Span    `json:"span"`
	Original    string  `json:"original"`
	Replacement string  `json:"replacement"`
	// Alternatives are other plausible corrections — the intended word is
	// often not the closest edit ("hers" may mean "here", not "her's").
	Alternatives []string `json:"alternatives,omitempty"`
	Category     string   `json:"category"`
	Rule         string   `json:"rule,omitempty"`
	Explanation  string   `json:"explanation"`
	Confidence   float64  `json:"confidence"`
}

// RawSuggestion is what the model returns for one segment, before anchoring.
type RawSuggestion struct {
	Original     string   `json:"original"`
	Replacement  string   `json:"replacement"`
	Alternatives []string `json:"alternatives"`
	Category     string   `json:"category"`
	Rule         string   `json:"rule"`
	Explanation  string   `json:"explanation"`
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// defaultConfidence reflects how much we trust each category from small
// models; the extension can threshold on it.
func defaultConfidence(category string) float64 {
	switch category {
	case CategoryCorrectness:
		return 0.9
	case CategoryClarity:
		return 0.75
	default:
		return 0.7
	}
}
