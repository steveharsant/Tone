package check

import "testing"

func TestU16ConverterASCII(t *testing.T) {
	c := NewU16Converter("hello world")
	for _, off := range []int{0, 5, 11} {
		if got := c.ToUTF16(off); got != off {
			t.Errorf("ToUTF16(%d) = %d, want identity for ASCII", off, got)
		}
	}
}

func TestU16ConverterSurrogatesAndMultibyte(t *testing.T) {
	// "a😀é" — 'a': 1 byte/1 unit; '😀': 4 bytes/2 units; 'é': 2 bytes/1 unit.
	s := "a\U0001F600éb"
	c := NewU16Converter(s)
	cases := map[int]int{
		0: 0, // 'a'
		1: 1, // start of 😀
		5: 3, // start of é (after surrogate pair)
		7: 4, // 'b'
		8: 5, // end
	}
	for byteOff, want := range cases {
		if got := c.ToUTF16(byteOff); got != want {
			t.Errorf("ToUTF16(%d) = %d, want %d", byteOff, got, want)
		}
	}
	if got := UTF16Len(s); got != 5 {
		t.Errorf("UTF16Len = %d, want 5", got)
	}
}

func TestU16ConverterClamping(t *testing.T) {
	c := NewU16Converter("😀")
	if got := c.ToUTF16(2); got != 0 { // inside the rune → clamp to rune start
		t.Errorf("mid-rune offset = %d, want 0", got)
	}
	if got := c.ToUTF16(-1); got != 0 {
		t.Errorf("negative offset = %d, want 0", got)
	}
	if got := c.ToUTF16(99); got != 2 {
		t.Errorf("past-end offset = %d, want 2", got)
	}
}
