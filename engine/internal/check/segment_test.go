package check

import "testing"

func TestSplitBasicSentences(t *testing.T) {
	text := "This is one. And this is two! Is this three?"
	segs := Split(text)
	want := []string{"This is one.", "And this is two!", "Is this three?"}
	if len(segs) != len(want) {
		t.Fatalf("got %d segments %+v, want %d", len(segs), segs, len(want))
	}
	for i, w := range want {
		if segs[i].Text != w {
			t.Errorf("segment %d = %q, want %q", i, segs[i].Text, w)
		}
	}
}

func TestSplitOffsetsIndexBackIntoSource(t *testing.T) {
	text := "First sentence. Second one here!\n\nA new paragraph. And más—unicode ünïcode. \"Quoted end.\" Trailing"
	for _, seg := range Split(text) {
		got := text[seg.ByteStart : seg.ByteStart+len(seg.Text)]
		if got != seg.Text {
			t.Errorf("ByteStart mismatch: text[%d:] = %q, want %q", seg.ByteStart, got, seg.Text)
		}
	}
}

func TestSplitNoBreakOnDecimalOrAcronymMidword(t *testing.T) {
	segs := Split("The value is 3.5 which is fine.")
	if len(segs) != 1 {
		t.Fatalf("decimal point split the sentence: %+v", segs)
	}
}

func TestSplitKeepsClosingQuote(t *testing.T) {
	segs := Split(`He said "stop." Then he left.`)
	if len(segs) != 2 {
		t.Fatalf("got %d segments %+v", len(segs), segs)
	}
	if segs[0].Text != `He said "stop."` {
		t.Errorf("closing quote not attached: %+v", segs[0].Text)
	}
}

func TestSplitNewlinesAndBlankLines(t *testing.T) {
	segs := Split("line one\n\n\nline two")
	if len(segs) != 2 || segs[0].Text != "line one" || segs[1].Text != "line two" {
		t.Fatalf("got %+v", segs)
	}
}

func TestSplitEmptyAndWhitespace(t *testing.T) {
	if segs := Split(""); len(segs) != 0 {
		t.Errorf("empty text: got %+v", segs)
	}
	if segs := Split("   \n\t  "); len(segs) != 0 {
		t.Errorf("whitespace text: got %+v", segs)
	}
}

func TestSplitLongTextWithoutTerminators(t *testing.T) {
	long := ""
	for range 900 {
		long += "word "
	}
	segs := Split(long)
	if len(segs) < 2 {
		t.Fatalf("expected hard cap to split, got %d segments", len(segs))
	}
	for _, seg := range segs {
		if len(seg.Text) > maxSegmentBytes {
			t.Errorf("segment exceeds cap: %d bytes", len(seg.Text))
		}
		if got := long[seg.ByteStart : seg.ByteStart+len(seg.Text)]; got != seg.Text {
			t.Errorf("offset mismatch on capped segment")
		}
	}
}
