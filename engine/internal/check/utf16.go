package check

import "sort"

// U16Converter maps byte offsets within a UTF-8 Go string to UTF-16 code-unit
// offsets, which is what JavaScript (and therefore the extension's DOM code)
// counts in. Built once per document, O(log n) per lookup.
type U16Converter struct {
	byteIdx []int // byte offset of each rune start
	u16Idx  []int // UTF-16 offset at that rune start
	byteLen int
	u16Len  int
}

func NewU16Converter(s string) *U16Converter {
	c := &U16Converter{
		byteIdx: make([]int, 0, len(s)),
		u16Idx:  make([]int, 0, len(s)),
		byteLen: len(s),
	}
	u16 := 0
	for i, r := range s {
		c.byteIdx = append(c.byteIdx, i)
		c.u16Idx = append(c.u16Idx, u16)
		if r > 0xFFFF {
			u16 += 2 // surrogate pair
		} else {
			u16++
		}
	}
	c.u16Len = u16
	return c
}

// ToUTF16 converts a byte offset to a UTF-16 offset. Offsets that fall inside
// a rune are clamped down to that rune's start.
func (c *U16Converter) ToUTF16(byteOff int) int {
	if byteOff <= 0 {
		return 0
	}
	if byteOff >= c.byteLen {
		return c.u16Len
	}
	// Greatest rune start <= byteOff.
	i := sort.SearchInts(c.byteIdx, byteOff+1) - 1
	return c.u16Idx[i]
}

// UTF16Len reports the UTF-16 code-unit length of s.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
