package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
)

func promptApp(t *testing.T, width, height int, chat bool) *App {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = width, height
	if chat {
		app.view = viewChat
		app.active = &client.Session{ID: "ses_1", Title: "Test"}
	}
	app.syncPromptSize()
	return app
}

// TestPromptFillsItsBoxWidth is the regression for "the width of text is not
// taking the whole prompt box".
//
// inputWidth used to return the minimum of the home box and the session box so
// one editor could live in either. The home box is capped at 75 columns and
// the session box is not, so on a wide terminal the session editor came out
// ~20 columns short of its own right edge.
func TestPromptFillsItsBoxWidth(t *testing.T) {
	for _, width := range []int{80, 100, 140, 200} {
		app := promptApp(t, width, 40, true)
		// promptBox declares sessionPromptBoxWidth() and pads 2 on each side.
		want := app.sessionPromptBoxWidth() - 4
		if got := app.inputWidth(); got != want {
			t.Errorf("session at width %d: editor is %d columns, box content area is %d", width, got, want)
		}
	}
}

func TestPromptFillsHomeBoxWidth(t *testing.T) {
	for _, width := range []int{80, 100, 140, 200} {
		app := promptApp(t, width, 40, false)
		want := promptMaxWidth(width) - 4
		if got := app.inputWidth(); got != want {
			t.Errorf("home at width %d: editor is %d columns, box content area is %d", width, got, want)
		}
	}
}

// TestPromptWidthFollowsTheView: one editor serves both boxes, so switching
// views has to resize it.
func TestPromptWidthFollowsTheView(t *testing.T) {
	app := promptApp(t, 200, 40, false)
	home := app.inputWidth()

	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "T"}
	app.syncPromptSize()
	session := app.inputWidth()

	if home == session {
		t.Fatalf("both views sized the editor at %d; the boxes are different widths", home)
	}
	// Width() reports the text area, which is one column less than what
	// SetWidth was given: the textarea reserves that for its own prompt
	// (input.Prompt is " ").
	if want := session - 1; app.input.Width() != want {
		t.Errorf("the editor text area is %d columns after switching views, want %d", app.input.Width(), want)
	}
}

// TestPromptGrowsToFitContent is the regression for "only one line is
// visualized". The editor was pinned at SetHeight(1) and never resized, so a
// second line was invisible. The original sets minHeight={1} with a maxHeight
// and grows between them.
//
// It asserts against the rendered box rather than the height field, so it
// holds even if wrappedRowCount and the textarea's own wrapping drift apart.
func TestPromptGrowsToFitContent(t *testing.T) {
	app := promptApp(t, 140, 40, true)

	for _, lines := range []int{1, 2, 3, 5} {
		var typed []string
		for i := 0; i < lines; i++ {
			typed = append(typed, "unique-line-"+string(rune('a'+i)))
		}
		app.input.SetValue(strings.Join(typed, "\n"))
		app.syncPromptSize()

		box := app.promptBox(app.sessionPromptBoxWidth())
		for _, want := range typed {
			if !strings.Contains(box, want) {
				t.Errorf("with %d lines typed, %q is not visible in the prompt box:\n%s", lines, want, box)
			}
		}
	}
}

// TestPromptBoxGrowsUpward: the box gets taller as lines are added, which is
// what pushes its top edge up the screen.
func TestPromptBoxGrowsUpward(t *testing.T) {
	app := promptApp(t, 140, 40, true)

	app.input.SetValue("one line")
	app.syncPromptSize()
	short := strings.Count(app.promptBox(app.sessionPromptBoxWidth()), "\n")

	app.input.SetValue("one line\ntwo lines\nthree lines")
	app.syncPromptSize()
	tall := strings.Count(app.promptBox(app.sessionPromptBoxWidth()), "\n")

	if tall <= short {
		t.Errorf("box is %d rows for 3 lines and %d for 1; it must grow", tall+1, short+1)
	}
	if tall-short != 2 {
		t.Errorf("box grew by %d rows for 2 extra lines, want 2", tall-short)
	}
}

// TestPromptRespectsMaxHeight ports maxHeight: max(6, height/3). Without a cap
// a pasted wall of text would swallow the whole screen.
func TestPromptRespectsMaxHeight(t *testing.T) {
	for _, height := range []int{12, 30, 60} {
		app := promptApp(t, 140, height, true)
		var lines []string
		for i := 0; i < 200; i++ {
			lines = append(lines, "line")
		}
		app.input.SetValue(strings.Join(lines, "\n"))
		app.syncPromptSize()

		want := app.promptMaxHeight()
		if got := app.input.Height(); got != want {
			t.Errorf("terminal height %d: editor grew to %d rows, want the cap of %d", height, got, want)
		}
	}
}

func TestPromptMaxHeightMatchesOriginalFormula(t *testing.T) {
	cases := map[int]int{
		12: 6,  // max(6, 4) — the floor applies
		18: 6,  // max(6, 6)
		30: 10, // max(6, 10)
		60: 20, // max(6, 20)
	}
	for height, want := range cases {
		app := promptApp(t, 100, height, true)
		if got := app.promptMaxHeight(); got != want {
			t.Errorf("terminal height %d: max prompt height %d, want %d", height, got, want)
		}
	}
}

// TestPromptShrinksBackWhenCleared: growing is only half of it — sending a
// message must give the rows back.
func TestPromptShrinksBackWhenCleared(t *testing.T) {
	app := promptApp(t, 140, 40, true)
	app.input.SetValue("one\ntwo\nthree\nfour")
	app.syncPromptSize()
	if app.input.Height() == 1 {
		t.Fatal("expected the editor to have grown")
	}

	app.input.SetValue("")
	app.syncPromptSize()
	if got := app.input.Height(); got != 1 {
		t.Errorf("editor is %d rows after clearing, want 1", got)
	}
}

// TestPromptGrowsOnSoftWrap: a single long line that wraps needs the rows too,
// not just explicit newlines.
func TestPromptGrowsOnSoftWrap(t *testing.T) {
	app := promptApp(t, 140, 40, true)
	width := app.inputWidth()

	var words []string
	for i := 0; i < width/4+10; i++ {
		words = append(words, "word")
	}
	app.input.SetValue(strings.Join(words, " "))
	app.syncPromptSize()

	if got := app.input.Height(); got < 2 {
		t.Errorf("a line of %d columns in a %d-column editor rendered %d rows, want it to wrap",
			len(strings.Join(words, " ")), width, got)
	}
}

// TestSyncRunsFromUpdate: the resize has to happen on the real update path,
// not only when a test calls it.
func TestSyncRunsFromUpdate(t *testing.T) {
	app := promptApp(t, 140, 40, true)
	app.input.SetValue("alpha\nbeta\ngamma")
	if app.input.Height() != 1 {
		t.Fatal("precondition: the editor should still be at its old height")
	}
	app.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	if app.input.Height() != 3 {
		t.Errorf("Update left the editor at %d rows, want 3", app.input.Height())
	}
}

// TestWrappedRowCount pins the wrapping rules against known cases.
//
// Every expectation here was read off the real bubbles/textarea rather than
// reasoned about — two of them were originally written from the obvious
// mental model and were wrong, which is the bug this file exists for. The
// authority is TestWrapAgreesExhaustively, which compares against the library
// across every length; these are the readable summary of what it enforces.
func TestWrappedRowCount(t *testing.T) {
	cases := []struct {
		name  string
		text  string
		width int
		want  int
	}{
		{"empty", "", 20, 1},
		{"one short line", "hello", 20, 1},
		{"explicit newlines", "a\nb\nc", 20, 3},
		// A line reaching exactly the width wraps: the library's final flush
		// compares with >=, not >.
		{"exactly the width", "12345", 5, 2},
		{"just under the width", "1234", 5, 1},
		// "aaa " + "bbb " fills 8 > 7 so bbb wraps; "ccc" then lands exactly
		// on the width and wraps again.
		{"wraps by word", "aaa bbb ccc", 7, 3},
		{"a word longer than the width breaks", "aaaaaaaaaaaa", 5, 3},
		{"blank lines count", "a\n\nb", 20, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wrappedRowCount(c.text, c.width); got != c.want {
				t.Errorf("wrappedRowCount(%q, %d) = %d, want %d", c.text, c.width, got, c.want)
			}
		})
	}
}
