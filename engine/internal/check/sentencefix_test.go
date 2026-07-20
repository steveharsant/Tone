package check

import "testing"

func TestSentenceFixesCombinesPerSentence(t *testing.T) {
	text := "I definately want the recipt now. This sentence is fine."
	sugs := []Suggestion{
		{Span: Span{Start: 2, End: 12}, Original: "definately", Replacement: "definitely", Category: "correctness"},
		{Span: Span{Start: 22, End: 28}, Original: "recipt", Replacement: "receipt", Category: "correctness"},
	}
	fixes := SentenceFixes(text, sugs)
	if len(fixes) != 1 {
		t.Fatalf("got %d fixes, want 1", len(fixes))
	}
	f := fixes[0]
	if f.Count != 2 {
		t.Errorf("count = %d", f.Count)
	}
	if f.Original != "I definately want the recipt now." {
		t.Errorf("original = %q", f.Original)
	}
	if f.Replacement != "I definitely want the receipt now." {
		t.Errorf("replacement = %q", f.Replacement)
	}
	if text[f.Span.Start:f.Span.End] != f.Original { // ASCII: utf16 == byte offsets
		t.Errorf("span mismatch: %q", text[f.Span.Start:f.Span.End])
	}
}

func TestSentenceFixesSkipsSingleIssueSentences(t *testing.T) {
	text := "I definately agree. We recieved it."
	sugs := []Suggestion{
		{Span: Span{Start: 2, End: 12}, Original: "definately", Replacement: "definitely"},
		{Span: Span{Start: 23, End: 31}, Original: "recieved", Replacement: "received"},
	}
	if fixes := SentenceFixes(text, sugs); len(fixes) != 0 {
		t.Fatalf("single-issue sentences must not produce fixes: %+v", fixes)
	}
}

func TestSentenceFixesDedupesCrossTierOverlaps(t *testing.T) {
	text := "I have sum thing and a recipt here."
	sugs := []Suggestion{
		{Span: Span{Start: 7, End: 16}, Original: "sum thing", Replacement: "something"}, // grammar tier
		{Span: Span{Start: 7, End: 16}, Original: "sum thing", Replacement: "something"}, // spelling tier duplicate
		{Span: Span{Start: 23, End: 29}, Original: "recipt", Replacement: "receipt"},
	}
	fixes := SentenceFixes(text, sugs)
	if len(fixes) != 1 {
		t.Fatalf("got %d fixes", len(fixes))
	}
	if fixes[0].Replacement != "I have something and a receipt here." {
		t.Errorf("replacement = %q (duplicate applied twice?)", fixes[0].Replacement)
	}
	if fixes[0].Count != 2 {
		t.Errorf("count = %d, want 2 after dedupe", fixes[0].Count)
	}
}

func TestSentenceFixesUnicodeOffsets(t *testing.T) {
	text := "😀 I saw tehm and tehre today."
	sugs := []Suggestion{
		{Span: Span{Start: 9, End: 13}, Original: "tehm", Replacement: "them"},
		{Span: Span{Start: 18, End: 23}, Original: "tehre", Replacement: "there"},
	}
	fixes := SentenceFixes(text, sugs)
	if len(fixes) != 1 {
		t.Fatalf("got %d fixes", len(fixes))
	}
	if fixes[0].Replacement != "😀 I saw them and there today." {
		t.Errorf("replacement = %q", fixes[0].Replacement)
	}
}
