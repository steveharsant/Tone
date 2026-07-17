package check

import "testing"

func TestAnchorExact(t *testing.T) {
	seg := "I has a dream about it."
	got := anchorAll(seg, []RawSuggestion{
		{Original: "has", Replacement: "have", Category: "correctness"},
	})
	if len(got) != 1 {
		t.Fatalf("got %d anchors", len(got))
	}
	if seg[got[0].byteStart:got[0].byteEnd] != "has" {
		t.Errorf("anchored %q", seg[got[0].byteStart:got[0].byteEnd])
	}
}

func TestAnchorMultipleOccurrencesClaimInOrder(t *testing.T) {
	seg := "the cat and the dog"
	got := anchorAll(seg, []RawSuggestion{
		{Original: "the", Replacement: "The", Category: "correctness"},
		{Original: "the", Replacement: "a", Category: "clarity"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d anchors, want 2", len(got))
	}
	if got[0].byteStart != 0 || got[1].byteStart != 12 {
		t.Errorf("offsets = %d,%d want 0,12", got[0].byteStart, got[1].byteStart)
	}
}

func TestAnchorDropsHallucinatedSnippet(t *testing.T) {
	got := anchorAll("A perfectly clean sentence.", []RawSuggestion{
		{Original: "definately", Replacement: "definitely", Category: "correctness"},
	})
	if len(got) != 0 {
		t.Fatalf("hallucinated snippet must be dropped, got %+v", got)
	}
}

func TestAnchorDropsIdenticalReplacement(t *testing.T) {
	got := anchorAll("Some text here.", []RawSuggestion{
		{Original: "text", Replacement: "text", Category: "clarity"},
	})
	if len(got) != 0 {
		t.Fatal("no-op suggestion must be dropped")
	}
}

func TestAnchorTrimmedFallback(t *testing.T) {
	seg := "It was very good."
	got := anchorAll(seg, []RawSuggestion{
		{Original: " very good ", Replacement: "excellent", Category: "engagement"},
	})
	if len(got) != 1 {
		t.Fatalf("trimmed fallback failed, got %d", len(got))
	}
	if seg[got[0].byteStart:got[0].byteEnd] != "very good" {
		t.Errorf("anchored %q", seg[got[0].byteStart:got[0].byteEnd])
	}
}

func TestAnchorFuzzyCaseAndWhitespace(t *testing.T) {
	seg := "In order  to\tsucceed, try."
	got := anchorAll(seg, []RawSuggestion{
		{Original: "in order to succeed", Replacement: "to succeed", Category: "clarity"},
	})
	if len(got) != 1 {
		t.Fatalf("fuzzy anchor failed")
	}
	if want := "In order  to\tsucceed"; seg[got[0].byteStart:got[0].byteEnd] != want {
		t.Errorf("anchored %q, want %q", seg[got[0].byteStart:got[0].byteEnd], want)
	}
}

func TestAnchorRejectsOverlap(t *testing.T) {
	seg := "this is a wordy phrase indeed"
	got := anchorAll(seg, []RawSuggestion{
		{Original: "a wordy phrase", Replacement: "wordy", Category: "clarity"},
		{Original: "wordy phrase indeed", Replacement: "phrase", Category: "clarity"},
	})
	if len(got) != 1 {
		t.Fatalf("overlapping suggestions must collapse to one, got %d", len(got))
	}
}

func TestAnchorDropsWhenTextAlreadyEqualsReplacement(t *testing.T) {
	// Fuzzy match can anchor "The" onto "the"; if the replacement is what is
	// already in the document, there is no edit to offer.
	got := anchorAll("the start.", []RawSuggestion{
		{Original: "The", Replacement: "the", Category: "correctness"},
	})
	if len(got) != 0 {
		t.Fatalf("self-identical anchored edit must be dropped, got %+v", got)
	}
}
