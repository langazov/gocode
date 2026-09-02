package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// panelLines renders the current dialog panel and returns its plain-text
// lines, the same content the compositor splices onto the screen.
func panelLines(t *testing.T, app *App) []string {
	t.Helper()
	panel, _ := app.overlayPanel()
	lines := strings.Split(panel, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = ansi.Strip(line)
	}
	return out
}

// blank reports whether a rendered panel row carries no text — the panel
// pads every line to its full width, so gap rows are runs of spaces.
func blank(line string) bool { return strings.TrimSpace(line) == "" }

// trimmed drops the panel's right-hand padding so a row can be compared
// against the text it is meant to end with.
func trimmed(line string) string { return strings.TrimRight(line, " ") }

// TestAlertDialogLayout pins ui/dialog-alert.tsx: the panel's own paddingTop,
// the title row with its esc hint, a blank gap, the muted message, the
// message box's paddingBottom plus the parent gap, then the ok button.
func TestAlertDialogLayout(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openAlert("Retry Error", "the model refused the request", nil)
	lines := panelLines(t, app)

	if len(lines) != 8 {
		t.Fatalf("alert panel has %d lines, want 8 (padTop, title, gap, message, "+
			"padBottom, gap, button, padBottom):\n%q", len(lines), lines)
	}
	if !blank(lines[0]) {
		t.Errorf("first row = %q, want the panel's paddingTop", lines[0])
	}
	if !strings.HasPrefix(lines[1], "  Retry Error") {
		t.Errorf("title row = %q, want it padded by 2 with the title", lines[1])
	}
	if !strings.HasSuffix(trimmed(lines[1]), "esc") {
		t.Errorf("title row = %q, want a right-aligned esc hint", lines[1])
	}
	if !blank(lines[2]) || !blank(lines[4]) || !blank(lines[5]) || !blank(lines[7]) {
		t.Errorf("gap rows = %q/%q/%q/%q, want all blank",
			lines[2], lines[4], lines[5], lines[7])
	}
	if trimmed(lines[3]) != "  the model refused the request" {
		t.Errorf("message row = %q", lines[3])
	}
	if !strings.HasSuffix(trimmed(lines[6]), "   ok") {
		t.Errorf("button row = %q, want a right-aligned ok padded by 3", lines[6])
	}
}

// TestAlertButtonIsRightAligned checks the ok button ends at the panel's
// right padding (justifyContent="flex-end" inside a box padded by 2).
func TestAlertButtonIsRightAligned(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openAlert("Title", "body", nil)
	panel, hits := app.overlayPanel()
	width := 0
	for _, line := range strings.Split(panel, "\n") {
		if w := len(ansi.Strip(line)); w > width {
			width = w
		}
	}
	if len(hits.buttons) != 1 {
		t.Fatalf("got %d button spans, want 1", len(hits.buttons))
	}
	if hits.buttons[0].end != width-2 {
		t.Errorf("ok button ends at col %d, want %d (panel width %d less padding 2)",
			hits.buttons[0].end, width-2, width)
	}
}

// TestConfirmDialogButtons pins ui/dialog-confirm.tsx: a cancel/confirm pair
// in that order, titlecased, with confirm active on open.
func TestConfirmDialogButtons(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openConfirm("Confirm Redo", "restore the reverted messages?", "", nil, nil)
	lines := panelLines(t, app)
	row := trimmed(lines[len(lines)-2])
	if !strings.HasSuffix(row, " Cancel  Confirm") {
		t.Fatalf("button row = %q, want cancel then confirm each padded by 1", row)
	}
	if !app.overlay.confirmActive {
		t.Error("confirm should start active, matching DialogConfirm's initial state")
	}
}

// TestConfirmCancelLabelOverride pins the label prop, which renames only the
// cancel button.
func TestConfirmCancelLabelOverride(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openConfirm("Delete", "sure?", "keep", nil, nil)
	lines := panelLines(t, app)
	if row := trimmed(lines[len(lines)-2]); !strings.HasSuffix(row, " Keep  Confirm") {
		t.Fatalf("button row = %q, want the titlecased label in place of Cancel", row)
	}
}

// TestConfirmArrowsToggleAndEnterRuns walks the keyboard contract: left/right
// swap the active button and enter runs only that branch.
func TestConfirmArrowsToggleAndEnterRuns(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keys      []string
		confirmed bool
		cancelled bool
	}{
		{name: "enter confirms", keys: []string{"enter"}, confirmed: true},
		{name: "left then enter cancels", keys: []string{"left", "enter"}, cancelled: true},
		{name: "left right then enter confirms", keys: []string{"left", "right", "enter"}, confirmed: true},
		{name: "escape runs neither", keys: []string{"esc"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newTestApp(t, "http://example.invalid")
			var confirmed, cancelled bool
			app.openConfirm("T", "m", "",
				func() tea.Msg { confirmed = true; return nil },
				func() tea.Msg { cancelled = true; return nil })
			for _, key := range tc.keys {
				app.handleOverlayKey(key)
			}
			if confirmed != tc.confirmed || cancelled != tc.cancelled {
				t.Errorf("confirmed=%v cancelled=%v, want %v/%v",
					confirmed, cancelled, tc.confirmed, tc.cancelled)
			}
			if app.overlay != nil {
				t.Error("dialog should be closed afterwards")
			}
		})
	}
}

// TestAlertEscapeRunsContinuation covers DialogAlert.show settling from the
// dialog's onClose as well as from the ok binding.
func TestAlertEscapeRunsContinuation(t *testing.T) {
	for _, key := range []string{"enter", "esc"} {
		app := newTestApp(t, "http://example.invalid")
		var ran bool
		app.openAlert("T", "m", func() tea.Msg { ran = true; return nil })
		app.handleOverlayKey(key)
		if !ran {
			t.Errorf("%q should run the alert's continuation", key)
		}
	}
}

// TestListDialogRendersFilterRow pins DialogSelect's filter input, which sits
// under the title inside the same padded box (paddingTop 1) and shows the
// placeholder while empty.
func TestListDialogRendersFilterRow(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Commands", []overlayItem{{label: "session.new"}})
	lines := panelLines(t, app)
	if !blank(lines[2]) {
		t.Errorf("row after the title = %q, want the filter box's paddingTop", lines[2])
	}
	if trimmed(lines[3]) != "    Search" {
		t.Errorf("filter row = %q, want the placeholder padded by 4", lines[3])
	}
	if !blank(lines[4]) {
		t.Errorf("row after the filter = %q, want the parent gap", lines[4])
	}

	app.overlay.filter = "ses"
	if got := trimmed(panelLines(t, app)[3]); !strings.HasPrefix(got, "    ses") {
		t.Errorf("filter row = %q, want the typed text", got)
	}
}

// TestListDialogCustomPlaceholder pins DialogSelect's placeholder prop.
func TestListDialogCustomPlaceholder(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Skills", []overlayItem{{label: "review"}})
	app.overlay.placeholder = "Search skills..."
	if got := trimmed(panelLines(t, app)[3]); got != "    Search skills..." {
		t.Errorf("filter row = %q", got)
	}
}

// TestListDialogHideFilter pins renderFilter={false}: the title is followed
// straight by the parent gap and the list.
func TestListDialogHideFilter(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Timeline", []overlayItem{{label: "first message"}})
	app.overlay.hideFilter = true
	lines := panelLines(t, app)
	for _, line := range lines {
		if strings.Contains(line, "Search") {
			t.Fatalf("hideFilter should suppress the filter row:\n%q", lines)
		}
	}
	if !blank(lines[2]) {
		t.Errorf("row after the title = %q, want the parent gap", lines[2])
	}
}

// TestListDialogEmptyView covers both the default "No results found" and the
// emptyView a caller supplies in its place.
func TestListDialogEmptyView(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Skills", nil)
	if !strings.Contains(strings.Join(panelLines(t, app), "\n"), "    No results found") {
		t.Error("an empty list should fall back to No results found")
	}

	app.overlay.emptyTitle = "Could not load skills"
	app.overlay.emptyBody = "connection refused"
	rendered := strings.Join(panelLines(t, app), "\n")
	if strings.Contains(rendered, "No results found") {
		t.Error("emptyView should replace the default fallback")
	}
	if !strings.Contains(rendered, "    Could not load skills") ||
		!strings.Contains(rendered, "    connection refused") {
		t.Errorf("emptyView not rendered:\n%s", rendered)
	}
}

// TestLockedListIgnoresInput pins DialogSelect's locked prop: filtering and
// movement are inert, and only escape still closes the dialog.
func TestLockedListIgnoresInput(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Skills", []overlayItem{{label: "a"}, {label: "b"}})
	app.overlay.locked = true
	for _, key := range []string{"down", "x", "enter"} {
		app.handleOverlayKey(key)
	}
	if app.overlay == nil {
		t.Fatal("a locked dialog should stay open")
	}
	if app.overlay.selected != 0 || app.overlay.filter != "" {
		t.Errorf("locked dialog moved to %d / filtered %q",
			app.overlay.selected, app.overlay.filter)
	}
	app.handleOverlayKey("esc")
	if app.overlay != nil {
		t.Error("escape should still close a locked dialog")
	}
}
