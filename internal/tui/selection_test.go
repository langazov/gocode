package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/anomalyco/opencode-go/internal/tui/client"
)

func selectionApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 140, 24
	app.view = viewChat
	app.sidebar = true
	app.active = &client.Session{ID: "ses_1", Title: "Test", Directory: "/tmp"}
	app.timeline = []client.Message{
		settledAssistant(t, "m1", "alpha beta gamma\n\ndelta epsilon zeta"),
	}
	app.currentFrame() // lays out the columns, recording chatColumnEnd
	return app
}

// A multi-row drag used to take the whole screen width for every row between
// its ends, so copying two lines of an answer came back holding the sidebar's
// text and none of the answer.
func TestMultiRowSelectionStaysInTheChatColumn(t *testing.T) {
	app := selectionApp(t)
	if app.chatColumnEnd <= 0 || app.chatColumnEnd >= app.width {
		t.Fatalf("chatColumnEnd = %d, want it inside the screen (width %d)", app.chatColumnEnd, app.width)
	}

	// Drag from the far left of the chat column to a column still inside it,
	// spanning several rows.
	app.handleMouse(tea.MouseClickMsg{X: 2, Y: 4, Button: tea.MouseLeft})
	app.handleMouse(tea.MouseMotionMsg{X: 30, Y: 12, Button: tea.MouseLeft})

	got := app.selectedText()
	for _, leaked := range []string{"spent", "LSP", "Context", "OpenCode"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("the sidebar leaked into a chat selection (%q):\n%s", leaked, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if width := len([]rune(line)); width > app.chatColumnEnd {
			t.Fatalf("line %q is %d cells, past the chat column's %d", line, width, app.chatColumnEnd)
		}
	}
}

// A drag that starts in the sidebar belongs to the sidebar.
func TestSelectionStartedInTheSidebarStaysThere(t *testing.T) {
	app := selectionApp(t)
	start := app.chatColumnEnd + 4

	app.handleMouse(tea.MouseClickMsg{X: start, Y: 3, Button: tea.MouseLeft})
	app.handleMouse(tea.MouseMotionMsg{X: app.width - 3, Y: 8, Button: tea.MouseLeft})

	minCol, maxCol := app.selectionColumnBounds()
	if minCol != app.chatColumnEnd || maxCol != app.width {
		t.Fatalf("bounds = [%d,%d), want the sidebar's [%d,%d)",
			minCol, maxCol, app.chatColumnEnd, app.width)
	}
	if strings.Contains(app.selectedText(), "alpha") {
		t.Fatal("a sidebar selection should not reach into the chat column")
	}
}

// With no docked sidebar there is nothing to split at, so the whole width is
// selectable as before.
func TestSelectionSpansTheFullWidthWithoutASidebar(t *testing.T) {
	app := selectionApp(t)
	app.sidebar = false
	app.currentFrame()

	minCol, maxCol := app.selectionColumnBounds()
	if minCol != 0 || maxCol != app.width {
		t.Fatalf("bounds = [%d,%d), want the full [0,%d)", minCol, maxCol, app.width)
	}
}

// The drag still yields the text actually under it.
func TestSelectionReturnsTheTextUnderIt(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 80, 20
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp"}
	app.timeline = []client.Message{settledAssistant(t, "m1", "hello selectable world")}

	row, col := -1, -1
	for i, line := range strings.Split(app.currentFrame(), "\n") {
		if at := strings.Index(ansi.Strip(line), "selectable"); at >= 0 {
			row, col = i, at
		}
	}
	if row < 0 {
		t.Fatal("the target text did not render")
	}

	app.handleMouse(tea.MouseClickMsg{X: col, Y: row, Button: tea.MouseLeft})
	app.handleMouse(tea.MouseMotionMsg{X: col + len("selectable") - 1, Y: row, Button: tea.MouseLeft})
	if got := app.selectedText(); got != "selectable" {
		t.Fatalf("selected %q, want %q", got, "selectable")
	}
}

// Releasing a drag copies and clears, matching copy-on-select in
// ui/dialog.tsx (`clipboard.write(text)` then `renderer.clearSelection()`).
func TestReleaseCopiesAndClears(t *testing.T) {
	app := selectionApp(t)
	row, col := -1, -1
	for i, line := range strings.Split(app.currentFrame(), "\n") {
		if at := strings.Index(ansi.Strip(line), "alpha"); at >= 0 {
			row, col = i, at
		}
	}
	if row < 0 {
		t.Fatal("the target text did not render")
	}

	app.handleMouse(tea.MouseClickMsg{X: col, Y: row, Button: tea.MouseLeft})
	app.handleMouse(tea.MouseMotionMsg{X: col + 4, Y: row, Button: tea.MouseLeft})

	cmd := app.handleMouse(tea.MouseReleaseMsg{X: col + 4, Y: row, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("releasing a drag should copy")
	}
	if app.selection.hasRange() {
		t.Fatal("the selection is cleared once copied")
	}
	if app.toast == nil || !strings.Contains(app.toast.text, "Copied") {
		t.Fatalf("expected the copied toast, got %+v", app.toast)
	}
}
