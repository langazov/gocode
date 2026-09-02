package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/tui/client"
)

// actualWrappedRows asks the textarea itself how many rows a logical line
// occupies, by putting the cursor at the end and reading the wrapped-row index
// it reports. This is the library's own wrapping rather than a
// reimplementation of it, so it is the reference the port has to agree with.
//
// Single logical line only: it reads the index within the cursor's own line,
// so a value containing newlines reports just the last one. Multi-line inputs
// are summed per line by the caller — see TestMultiLineWrapAgrees.
func actualWrappedRows(app *App, text string) int {
	app.input.SetValue(text)
	app.input.MoveToEnd()
	return app.input.LineInfo().RowOffset + 1
}

// TestDiagnoseWrapWidthMismatch is the diagnostic for "it is still hiding the
// first line of text when typing and the line ends".
//
// It compares the row count the port computes against the row count the
// textarea actually renders, at the boundary where a line wraps.
func TestDiagnoseWrapWidthMismatch(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.view = viewChat
	app.active = &client.Session{ID: "s", Title: "T"}
	app.syncPromptSize()

	declared := app.inputWidth()
	textWidth := app.input.Width()
	fmt.Printf("\ninputWidth()      = %d   (what SetWidth is given)\n", declared)
	fmt.Printf("input.Width()     = %d   (the width the textarea actually wraps at)\n", textWidth)
	fmt.Printf("difference        = %d   (input.Prompt = %q)\n\n", declared-textWidth, " ")

	fmt.Printf("%-6s %-10s %-10s %s\n", "len", "box sized", "textarea", "")
	mismatches := 0
	for _, length := range []int{declared - 3, declared - 2, declared - 1, declared, declared + 1, declared + 2} {
		text := strings.Repeat("x", length)
		// The production path: set the text, size the box, read the height.
		app.input.SetValue(text)
		app.syncPromptSize()
		mine := app.input.Height()
		theirs := actualWrappedRows(app, text)
		flag := ""
		if mine != theirs {
			flag = "  <-- MISMATCH: the box is sized for fewer rows than are rendered"
			mismatches++
		}
		fmt.Printf("%-6d %-10d %-10d%s\n", length, mine, theirs, flag)
	}
	if mismatches > 0 {
		t.Errorf("%d widths disagree; the viewport scrolls to keep the cursor visible and the first line goes off the top", mismatches)
	}
}

// TestDiagnoseFirstLineVisibleWhileTyping walks a line one character at a time
// past the wrap point and checks the first line is still on screen — the
// reported symptom, reproduced directly.
func TestDiagnoseFirstLineVisibleWhileTyping(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.view = viewChat
	app.active = &client.Session{ID: "s", Title: "T"}
	app.syncPromptSize()

	width := app.inputWidth()
	marker := "FIRSTWORD"
	var lost []int
	for extra := -4; extra <= 8; extra++ {
		// Walk straight through the wrap boundary one character at a time,
		// which is what typing does.
		fill := width - len(marker) + extra
		if fill < 0 {
			continue
		}
		text := marker + strings.Repeat("z", fill)
		app.input.SetValue(text)
		app.syncPromptSize()

		box := app.promptBox(app.sessionPromptBoxWidth())
		if !strings.Contains(box, marker) {
			lost = append(lost, extra)
		}
	}
	if len(lost) > 0 {
		t.Errorf("the start of the line disappeared at these overshoot lengths: %v", lost)
	}
}

// TestDiagnoseRowCountAgreesAcrossManyInputs is the broad sweep: any input
// where the two disagree is an input where the box is the wrong height.
func TestDiagnoseRowCountAgreesAcrossManyInputs(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.view = viewChat
	app.active = &client.Session{ID: "s", Title: "T"}
	app.syncPromptSize()
	width := app.inputWidth()

	inputs := []string{
		"short",
		strings.Repeat("a", width-1),
		strings.Repeat("a", width),
		strings.Repeat("a", width+1),
		strings.Repeat("a", width*2),
		strings.Repeat("word ", width/3),
		strings.Repeat("word ", width/2),
		"a b c " + strings.Repeat("d", width),
	}
	for i, text := range inputs {
		app.input.SetValue(text)
		app.syncPromptSize()
		mine := app.input.Height()
		theirs := actualWrappedRows(app, text)
		if mine != theirs {
			t.Errorf("input %d (%d chars): box sized for %d rows, textarea renders %d", i, len(text), mine, theirs)
		}
	}
}

// TestWrapAgreesExhaustively sweeps every length and several word shapes and
// requires the computed height to equal what the textarea renders, at three
// terminal widths.
//
// A count that is too low is the reported bug: the viewport scrolls to keep
// the cursor visible and the top of the text goes off screen. A count that is
// too high leaves a dead row in the box. Neither is acceptable, so this
// asserts equality rather than a bound.
func TestWrapAgreesExhaustively(t *testing.T) {
	for _, terminal := range []int{80, 100, 160} {
		app := newTestApp(t, "http://example.invalid")
		app.width, app.height = terminal, 200 // tall, so the max-height cap never applies
		app.view = viewChat
		app.active = &client.Session{ID: "s", Title: "T"}
		app.syncPromptSize()
		width := app.input.Width()

		shapes := map[string]func(int) string{
			"continuous":      func(n int) string { return strings.Repeat("x", n) },
			"words of 4":      func(n int) string { return trimTo(strings.Repeat("abcd ", n/5+1), n) },
			"words of 1":      func(n int) string { return trimTo(strings.Repeat("a ", n/2+1), n) },
			"long word after": func(n int) string { return "hi " + strings.Repeat("y", n) },
		}
		for name, build := range shapes {
			for n := 1; n <= width*3; n++ {
				text := build(n)
				app.input.SetValue(text)
				app.syncPromptSize()
				got := app.input.Height()
				want := actualWrappedRows(app, text)
				if got != want {
					t.Fatalf("terminal %d, %s, n=%d (%d chars): box sized for %d rows, textarea renders %d",
						terminal, name, n, len(text), got, want)
				}
			}
		}
	}
}

func trimTo(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.TrimRight(s, " ")
}

// TestMultiLineWrapAgrees: several logical lines, each of which may wrap.
func TestMultiLineWrapAgrees(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 200
	app.view = viewChat
	app.active = &client.Session{ID: "s", Title: "T"}
	app.syncPromptSize()
	width := app.input.Width()

	for _, lengths := range [][]int{
		{5, 5},
		{width, 5},
		{5, width},
		{width, width},
		{width - 1, width + 1, 3},
		{0, 5, 0},
	} {
		var lines []string
		expected := 0
		for _, n := range lengths {
			lines = append(lines, strings.Repeat("x", n))
			expected += actualWrappedRows(app, strings.Repeat("x", n))
		}
		text := strings.Join(lines, "\n")
		app.input.SetValue(text)
		app.syncPromptSize()
		if got := app.input.Height(); got != expected {
			t.Errorf("lines %v: box sized for %d rows, want %d", lengths, got, expected)
		}
	}
}
