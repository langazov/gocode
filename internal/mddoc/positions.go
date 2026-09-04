package mddoc

import (
	"unicode/utf16"
	"unicode/utf8"
)

// lineIndex maps byte offsets to LSP positions and back. LSP positions count
// UTF-16 code units per line, while Go and goldmark count bytes, so every
// crossing goes through here.
type lineIndex struct {
	// starts[i] is the byte offset of line i. Line 0 starts at 0.
	starts []int
	// lineEndsAtNewline records whether the document's final line ends with
	// a newline (a trailing empty last line vs. an unterminated one).
	trailingNewline bool
}

func newLineIndex(text string) lineIndex {
	starts := []int{0}
	trailing := false
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
			trailing = true
		} else {
			trailing = false
		}
	}
	return lineIndex{starts: starts, trailingNewline: trailing}
}

// lines reports the number of lines the document has. "Hello\n" is two lines;
// "Hello" is one.
func (ix lineIndex) lines() int {
	if len(ix.starts) == 1 && ix.starts[0] == 0 && !ix.trailingNewline {
		// Single line, may still be empty text.
		return 1
	}
	n := len(ix.starts)
	// A trailing newline starts one extra (empty) line, which editors count.
	return n
}

// lineStart returns the byte offset of the given line, clamped.
func (ix lineIndex) lineStart(line int) int {
	if line < 0 {
		return 0
	}
	if line >= len(ix.starts) {
		// Past the end: one past the last byte.
		if len(ix.starts) == 0 {
			return 0
		}
		return ix.starts[len(ix.starts)-1]
	}
	return ix.starts[line]
}

// positionToOffset converts an LSP position to a byte offset in text. An
// out-of-range position clamps to the nearest valid offset, matching how
// tolerant servers behave.
func (ix lineIndex) positionToOffset(text string, line, character int) int {
	if line < 0 {
		return 0
	}
	if line >= len(ix.starts) {
		return len(text)
	}
	start := ix.starts[line]
	end := len(text)
	if line+1 < len(ix.starts) {
		end = ix.starts[line+1] - 1 // exclude the newline
	}
	lineText := text[start:end]

	// Walk UTF-16 code units until `character` of them are consumed.
	units := 0
	for offset := 0; offset < len(lineText); {
		if units >= character {
			return start + offset
		}
		r, size := utf8.DecodeRuneInString(lineText[offset:])
		if r == utf8.RuneError && size <= 1 {
			size = 1
		}
		units += utf16.RuneLen(r)
		if utf16.RuneLen(r) < 0 { // unencodable surrogate; count as 1
			units++
		}
		offset += size
	}
	return start + len(lineText)
}

// offsetToPosition converts a byte offset to an LSP position. Out-of-range
// offsets clamp.
func (ix lineIndex) offsetToPosition(text string, offset int) (line, character int) {
	if offset < 0 {
		return 0, 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	// Binary search for the line that contains offset.
	lo, hi := 0, len(ix.starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if ix.starts[mid] <= offset {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	line = lo
	start := ix.starts[line]
	end := len(text)
	if line+1 < len(ix.starts) {
		end = ix.starts[line+1] - 1
	}
	lineText := text[start:end]
	units := 0
	for pos := 0; pos < offset-start; {
		r, size := utf8.DecodeRuneInString(lineText[pos:])
		if r == utf8.RuneError && size <= 1 {
			size = 1
		}
		units += utf16.RuneLen(r)
		if utf16.RuneLen(r) < 0 {
			units++
		}
		pos += size
	}
	return line, units
}
