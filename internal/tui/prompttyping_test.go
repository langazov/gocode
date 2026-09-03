package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// typingApp drives the real update path, as a user's keystrokes do. The
// earlier prompt tests called SetValue, which skips the textarea's own key
// handling — and the scroll that handling performs was the bug, so those tests
// could not see it.
func typingApp(t *testing.T, width, height int) *App {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = width, height
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return app
}

func typeText(app *App, text string) {
	for _, r := range text {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// TestTypingKeepsFirstLineVisible is the regression for "it is still hiding
// the first line of text when typing and line ends".
//
// The height was already growing correctly; the problem was that the textarea
// had *already* scrolled during its own key handling, and repositionView only
// ever scrolls toward the cursor — growing the window afterwards never brings
// the top back.
func TestTypingKeepsFirstLineVisible(t *testing.T) {
	for _, terminal := range []int{80, 100, 160} {
		app := typingApp(t, terminal, 40)
		width := app.input.Width()

		const marker = "STARTHERE"
		typeText(app, marker)

		// Type well past the first wrap, checking after every keystroke.
		for n := len(marker); n < width*2+4; n++ {
			app.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
			box := app.promptBox(app.sessionPromptBoxWidth())
			if !strings.Contains(box, marker) {
				t.Fatalf("terminal %d: the start of the line disappeared at length %d (editor width %d, height %d)",
					terminal, n+1, width, app.input.Height())
			}
		}
	}
}

// TestTypingNewlinesKeepsEarlierLinesVisible: the same check for explicit
// lines rather than soft wrapping.
func TestTypingNewlinesKeepsEarlierLinesVisible(t *testing.T) {
	app := typingApp(t, 100, 40)

	var typed []string
	for i := 0; i < 5; i++ {
		marker := "LINE" + string(rune('A'+i))
		typed = append(typed, marker)
		typeText(app, marker)

		box := app.promptBox(app.sessionPromptBoxWidth())
		for _, want := range typed {
			if !strings.Contains(box, want) {
				t.Fatalf("after typing %d lines, %q is no longer visible:\n%s", i+1, want, box)
			}
		}
		// A newline in the prompt is inserted rather than submitting.
		app.input.InsertRune('\n')
		app.syncPromptSize()
	}
}

// TestTypingBeyondMaxHeightScrolls: past the cap the editor must scroll, and
// what has to stay visible is the cursor's line, not the first one.
func TestTypingBeyondMaxHeightScrolls(t *testing.T) {
	app := typingApp(t, 100, 30)
	max := app.promptMaxHeight()

	for i := 0; i < max+5; i++ {
		typeText(app, "row"+string(rune('a'+i%26)))
		app.input.InsertRune('\n')
		app.syncPromptSize()
	}
	typeText(app, "CURSORLINE")

	if got := app.input.Height(); got != max {
		t.Errorf("editor is %d rows, want the cap of %d", got, max)
	}
	box := app.promptBox(app.sessionPromptBoxWidth())
	if !strings.Contains(box, "CURSORLINE") {
		t.Errorf("the line being typed must stay visible once the editor scrolls:\n%s", box)
	}
}

// TestDeletingShrinksAndKeepsTextVisible: growing is only half of it.
func TestDeletingShrinksAndKeepsTextVisible(t *testing.T) {
	app := typingApp(t, 100, 40)
	width := app.input.Width()

	const marker = "STARTHERE"
	typeText(app, marker+strings.Repeat("x", width))
	if app.input.Height() < 2 {
		t.Fatal("precondition: the editor should have wrapped")
	}

	for i := 0; i < width; i++ {
		app.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
		box := app.promptBox(app.sessionPromptBoxWidth())
		if !strings.Contains(box, marker) {
			t.Fatalf("the start of the line disappeared while deleting, at %d deletions", i+1)
		}
	}
	if got := app.input.Height(); got != 1 {
		t.Errorf("editor is %d rows after deleting back to one line, want 1", got)
	}
}

// TestEditorNeverScrollsWhileContentFits pins the invariant directly: while
// everything fits, the viewport must sit at the top.
func TestEditorNeverScrollsWhileContentFits(t *testing.T) {
	app := typingApp(t, 100, 40)
	width := app.input.Width()

	for n := 0; n < width*3; n++ {
		app.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
		rows := wrappedRowCount(app.input.Value(), app.input.Width())
		if rows > app.promptMaxHeight() {
			break // past the cap, scrolling is correct
		}
		if offset := app.input.ScrollYOffset(); offset != 0 {
			t.Fatalf("editor scrolled to offset %d at length %d while its %d rows fit in %d",
				offset, n+1, rows, app.input.Height())
		}
	}
}
