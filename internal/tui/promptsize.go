package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// This file sizes the prompt editor. It ports the two bounds the original sets
// on its textarea (component/prompt/index.tsx):
//
//	minHeight={1}
//	maxHeight={tuiConfig.prompt?.max_height ?? Math.max(6, Math.floor(dimensions().height / 3))}
//
// The editor grows with its content between them, which is why typing a second
// line in the original pushes the top of the prompt box upward.

// promptMaxHeight ports the maxHeight memo. The config override
// (`prompt.max_height`) has no Go equivalent yet, so only the default applies.
func (a *App) promptMaxHeight() int {
	third := a.height / 3
	if third < 6 {
		return 6
	}
	return third
}

// promptContentHeight is how many rows the editor needs for what is in it,
// clamped to the same bounds the original uses.
//
// The width used here is input.Width(), not inputWidth(). They differ by the
// textarea's prompt column: SetWidth(w) stores w minus the prompt width, and
// wrapping happens at that stored width. Measuring at the wider value made the
// box one row short for any line landing in that one-column band, and the
// viewport then scrolled to keep the cursor visible — which pushed the first
// line off the top. See TestDiagnoseWrapWidthMismatch.
func (a *App) promptContentHeight() int {
	rows := wrappedRowCount(a.input.Value(), a.input.Width())
	if rows < 1 {
		rows = 1
	}
	if max := a.promptMaxHeight(); rows > max {
		rows = max
	}
	return rows
}

// syncPromptSize resizes the editor to the current view and content.
//
// Called on every update rather than at the few places that change one of its
// inputs: the width depends on which view is showing, the height on the text,
// and both on the terminal size, so anything less than "recompute each cycle"
// leaves one of them stale. Both setters are no-ops when the value is
// unchanged, so the common path costs a comparison.
// expandPromptForInput gives the editor its full allowance before it handles a
// key, and is the other half of syncPromptSize.
//
// The textarea scrolls its viewport during Update to keep the cursor visible,
// and it only ever scrolls *toward* the cursor — repositionView never scrolls
// back up when the window later grows. So a keystroke that pushes the content
// onto a second row while the height is still one scrolls the first row off
// the top, and the correct height applied afterwards does not bring it back.
// That is the "first line disappears when the line wraps" bug.
//
// Editing at the full height means the content always fits while the key is
// handled, so no scroll happens; syncPromptSize then trims the height back to
// what the content needs, which does not scroll either because the cursor is
// already inside the smaller window.
func (a *App) expandPromptForInput() {
	if max := a.promptMaxHeight(); a.input.Height() < max {
		a.input.SetHeight(max)
	}
}

func (a *App) syncPromptSize() {
	// Width first: the height is measured against the width the textarea ends
	// up wrapping at, so a stale width would size the box for the wrong number
	// of rows.
	//
	// Compared against inputWidth() rather than input.Width() because the
	// latter is the post-adjustment value (minus the prompt column) and would
	// never equal what is being set, making this re-set on every update.
	if width := a.inputWidth(); width != a.promptWidthSet {
		a.promptWidthSet = width
		a.input.SetWidth(width)
	}
	if height := a.promptContentHeight(); height != a.input.Height() {
		a.input.SetHeight(height)
	}
}

// wrappedRowCount reports how many rendered rows a string occupies at a given
// width, matching the word wrapping bubbles/textarea applies: a word that does
// not fit moves to the next row whole, and a word longer than the width is
// broken across rows.
//
// It has to agree with the textarea's own wrapping, or the box is sized for a
// different number of rows than it renders. TestPromptGrowsToFitContent
// asserts the agreement end to end rather than trusting this to stay in step.
func wrappedRowCount(text string, width int) int {
	if width < 1 {
		return 1
	}
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		rows += wrappedLineRows(line, width)
	}
	if rows < 1 {
		return 1
	}
	return rows
}

func wrappedLineRows(line string, width int) int {
	if line == "" {
		return 1
	}
	rows := 1
	used := 0
	fields := splitKeepingSpaces(line)
	for i, field := range fields {
		word := strings.TrimRight(field, " \t")
		wordSize := ansi.StringWidth(word)
		spaceSize := ansi.StringWidth(field) - wordSize

		// The trailing word is measured with >= rather than >: the library's
		// final flush wraps when the line exactly reaches the width, unlike
		// the mid-line checks which wrap only past it. Missing this made a
		// line that ends exactly at the edge one row short.
		fits := used+wordSize+spaceSize <= width
		if i == len(fields)-1 {
			fits = used+wordSize+spaceSize < width
		}

		switch {
		case wordSize >= width:
			// A word at least as wide as the line is chunked across rows. The
			// break comes one column early — the library re-counts the last
			// rune's width when deciding whether it still fits, which costs a
			// column for every character — so a run of exactly `width` needs
			// two rows, not one. Empirically: rows = n/width + 1.
			if used > 0 {
				rows++
			}
			rows += wordSize / width
			used = wordSize%width + spaceSize
		case fits:
			used += wordSize + spaceSize
		default:
			rows++
			used = wordSize + spaceSize
		}
	}
	return rows
}

// splitKeepingSpaces breaks a line into words with their trailing spaces
// attached, which is the unit the textarea wraps on.
func splitKeepingSpaces(line string) []string {
	var out []string
	var current strings.Builder
	inSpaces := false
	for _, r := range line {
		if r == ' ' || r == '\t' {
			inSpaces = true
			current.WriteRune(r)
			continue
		}
		if inSpaces {
			out = append(out, current.String())
			current.Reset()
			inSpaces = false
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}
	return out
}
