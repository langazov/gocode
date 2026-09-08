package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Every BlockTool panel must fit inside the chat column: the panel's own
// Width() soft-wraps rather than truncates, so anything wider than the
// interior shows as wrapped rows whose leading cells look like more content
// (a wrapped diff "-" row reads as a continuation of the removal). These
// cases each construct content wider than the panel at every width tested.
func TestBlockToolPanelsFitChatColumn(t *testing.T) {
	for _, w := range []int{26, 30, 40, 60, 80, 120, 160} {
		app := benchApp(t, 0)
		app.width, app.height = w, 40
		app.sidebar = false
		app.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		panel := lipgloss.Width(app.blockToolStyle().Render("x"))
		interior := app.blockToolInterior()
		inner := app.blockToolInnerWidth()
		long := strings.Repeat("x", interior+40)

		blocks := map[string]func() string{
			"bash": func() string {
				b, _ := app.bashBlock("a", &toolState{Status: "completed",
					Input:  map[string]any{"command": strings.Repeat("go run ", 20)},
					Output: long + "\n" + strings.Repeat("second ", 30)})
				return b
			},
			"read": func() string {
				b, _ := app.readBlock("b", &toolState{Status: "completed",
					Input:  map[string]any{"path": "/x/main.go"},
					Output: "1: " + long + "\n2: short"})
				return b
			},
			"write": func() string {
				b, _ := app.writeBlock("c", &toolState{Status: "completed",
					Input: map[string]any{"path": "/x/main.go", "content": "package main\n" + long}})
				return b
			},
			"edit": func() string {
				return app.editDiffBlock(&toolState{Status: "completed",
					Input:  map[string]any{"path": "/x/main.go"},
					Output: "```diff\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n-" + long + "\n+" + long + "\n```"})
			},
			"todo": func() string {
				return app.todoWriteBlock(&toolState{Status: "completed",
					Input:  map[string]any{},
					Output: `[{"content":"` + strings.Repeat("todo ", 40) + `","status":"in_progress"}]`})
			},
			"error": func() string {
				b, _ := app.collapsibleBlock("d", app.onPanelText("$ cmd"), "body", inner, app.wrappedBody,
					&toolState{Status: "error", Error: strings.Repeat("boom ", 60), Input: map[string]any{}})
				return b
			},
		}
		for name, build := range blocks {
			got := lipgloss.Width(build())
			if got > panel {
				t.Errorf("term=%d %s: %d cells > panel %d", w, name, got, panel)
			}
		}
	}
}

// The collapsed summary's "click to expand" hint must share the row the
// click target covers — a hint wrapped onto a second row is visible where
// clicking does nothing.
func TestCollapsedHintStaysOnClickableRow(t *testing.T) {
	for _, w := range []int{26, 30, 40, 60, 100} {
		app := benchApp(t, 0)
		app.width, app.height = w, 40
		app.sidebar = false
		app.Update(tea.WindowSizeMsg{Width: w, Height: 40})
		inner := app.blockToolInnerWidth()
		body := strings.Repeat("abcdefgh", 12) + "\nsecond\nthird"
		blk, ref := app.collapsibleBlock("t9", app.onPanelText("$ cmd"), body, inner, app.wrappedBody,
			&toolState{Status: "completed", Input: map[string]any{}})
		rows := strings.Split(blk, "\n")
		if ref == nil {
			t.Fatalf("term=%d: no click target recorded", w)
		}
		if ref.lineEnd >= len(rows) {
			t.Errorf("term=%d: click target row %d beyond block rows %d", w, ref.lineEnd, len(rows))
		}
		// The clicked row must be the one carrying the hint.
		clicked := ansi.Strip(rows[ref.lineStart])
		if !strings.Contains(clicked, "expand") && !strings.Contains(clicked, "+2") {
			t.Errorf("term=%d: clicked row %q carries neither the hint nor the count", w, clicked)
		}
		// No row after the clicked one may be part of the same wrapped hint.
		for i := ref.lineEnd + 1; i < len(rows); i++ {
			if tail := ansi.Strip(rows[i]); strings.Contains(tail, "expand)") {
				t.Errorf("term=%d: hint spilled onto uncovered row %d: %q", w, i, tail)
			}
		}
	}
}

// A diff row must never wrap: the gutter's leading "+ "/"- " is what makes a
// row readable as an addition or a removal.
func TestDiffRowsNeverWrap(t *testing.T) {
	app := benchApp(t, 0)
	app.width, app.height = 80, 40
	app.sidebar = false
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	interior := app.blockToolInterior()
	long := strings.Repeat("z", interior+60)
	blk := app.editDiffBlock(&toolState{Status: "completed",
		Input:  map[string]any{"path": "/x/main.go"},
		Output: "```diff\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n-" + long + "\n+" + long + "\n```"})
	rows := strings.Split(blk, "\n")
	if len(rows) != 6 { // padding + title + hunk + 2 diff rows + padding
		t.Fatalf("expected 6 rows, got %d (diff wrapped?):\n%s", len(rows), ansi.Strip(blk))
	}
	for i, row := range rows {
		if i == 3 && !strings.Contains(ansi.Strip(row), "- z") {
			t.Errorf("removal row lost its gutter: %q", ansi.Strip(row))
		}
		if i == 4 && !strings.Contains(ansi.Strip(row), "+ z") {
			t.Errorf("addition row lost its gutter: %q", ansi.Strip(row))
		}
	}
}
