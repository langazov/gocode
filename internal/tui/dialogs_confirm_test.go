package tui

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/tui/dialog"
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

// isRule reports whether a row is one of the dialog frame's ─ rules.
func isRule(line string) bool {
	return strings.Contains(line, "─")
}

// blank reports whether a rendered panel row carries no text — the panel
// pads every line to its full width, so gap rows are runs of spaces.
func blank(line string) bool { return strings.TrimSpace(line) == "" }

// trimmed drops the panel's right-hand padding so a row can be compared
// against the text it is meant to end with.
func trimmed(line string) string { return strings.TrimRight(line, " ") }

// TestAlertDialogLayout pins the shared dialog frame on an alert: the
// panel's paddingTop, the title row with its esc hint, the rule under the
// header, a gap, the muted message, a gap, the rule above the footer, the
// button row and the panel's trailing blank.
func TestAlertDialogLayout(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openAlert("Retry Error", "the model refused the request", nil)
	lines := panelLines(t, app)

	if len(lines) != 9 {
		t.Fatalf("alert panel has %d lines, want 9 (padTop, title, rule, gap, "+
			"message, gap, rule, button, padBottom):\n%q", len(lines), lines)
	}
	if !blank(lines[0]) {
		t.Errorf("first row = %q, want the panel's paddingTop", lines[0])
	}
	if !strings.HasPrefix(lines[1], "   Retry Error") {
		t.Errorf("title row = %q, want it padded by dialog.PadX with the title", lines[1])
	}
	if !strings.HasSuffix(trimmed(lines[1]), "esc") {
		t.Errorf("title row = %q, want a right-aligned esc hint", lines[1])
	}
	if !isRule(lines[2]) || !isRule(lines[6]) {
		t.Errorf("rule rows = %q/%q, want the header and footer rules", lines[2], lines[6])
	}
	if !blank(lines[3]) || !blank(lines[5]) || !blank(lines[8]) {
		t.Errorf("gap rows = %q/%q/%q, want all blank", lines[3], lines[5], lines[8])
	}
	if trimmed(lines[4]) != "   the model refused the request" {
		t.Errorf("message row = %q", lines[4])
	}
	if !strings.HasSuffix(trimmed(lines[7]), "  Ok") {
		t.Errorf("button row = %q, want a right-aligned Ok padded by 2", lines[7])
	}
}

// TestAlertButtonIsRightAligned checks the ok button ends at the panel's
// shared content column rather than at an inset of its own.
func TestAlertButtonIsRightAligned(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openAlert("Title", "body", nil)
	panel, hits := app.overlayPanel()
	// Cells, not bytes: the frame's ─ rules are three bytes each.
	width := lipgloss.Width(panel)
	if len(hits.Buttons) != 1 {
		t.Fatalf("got %d button spans, want 1", len(hits.Buttons))
	}
	if hits.Buttons[0].End != width-dialog.PadX {
		t.Errorf("ok button ends at col %d, want %d (panel width %d less PadX)",
			hits.Buttons[0].End, width-dialog.PadX, width)
	}
}

// TestConfirmDialogButtons pins the cancel/confirm pair: that order,
// titlecased, each padded by dialog's single button padding, with a cell
// between them and confirm active on open.
func TestConfirmDialogButtons(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openConfirm("Confirm Redo", "restore the reverted messages?", "", nil, nil)
	lines := panelLines(t, app)
	row := trimmed(lines[len(lines)-2])
	if !strings.HasSuffix(row, "  Cancel     Confirm") {
		t.Fatalf("button row = %q, want cancel then confirm, each padded by 2", row)
	}
	if !app.overlay.ConfirmActive() {
		t.Error("confirm should start active, matching DialogConfirm's initial state")
	}
}

// TestConfirmCancelLabelOverride pins the label prop, which renames only the
// cancel button.
func TestConfirmCancelLabelOverride(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openConfirm("Delete", "sure?", "keep", nil, nil)
	lines := panelLines(t, app)
	if row := trimmed(lines[len(lines)-2]); !strings.HasSuffix(row, "  Keep     Confirm") {
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

// TestListDialogRendersFilterRow pins the filter input: it sits directly
// under the header rule, carries the ⌕ in the gutter lane, and shows the
// placeholder while empty.
func TestListDialogRendersFilterRow(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Commands", []overlayItem{{Label: "session.new"}})
	lines := panelLines(t, app)
	if !isRule(lines[2]) {
		t.Errorf("row after the title = %q, want the header rule", lines[2])
	}
	if trimmed(lines[3]) != "   ⌕ Search" {
		t.Errorf("filter row = %q, want the ⌕ at PadX and the placeholder after it", lines[3])
	}
	if !blank(lines[4]) {
		t.Errorf("row after the filter = %q, want the parent gap", lines[4])
	}

	app.overlay.SetFilter("ses")
	if got := trimmed(panelLines(t, app)[3]); !strings.HasPrefix(got, "   ⌕ ses") {
		t.Errorf("filter row = %q, want the typed text", got)
	}
}

// TestListDialogCustomPlaceholder pins DialogSelect's placeholder prop.
func TestListDialogCustomPlaceholder(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Skills", []overlayItem{{Label: "review"}})
	app.overlay.SetPlaceholder("Search skills...")
	if got := trimmed(panelLines(t, app)[3]); got != "   ⌕ Search skills..." {
		t.Errorf("filter row = %q", got)
	}
}

// TestListDialogHideFilter pins renderFilter={false}: the header rule is
// followed straight by the parent gap and the list.
func TestListDialogHideFilter(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Timeline", []overlayItem{{Label: "first message"}})
	app.overlay.SetHideFilter(true)
	lines := panelLines(t, app)
	for _, line := range lines {
		if strings.Contains(line, "Search") {
			t.Fatalf("hideFilter should suppress the filter row:\n%q", lines)
		}
	}
	if !isRule(lines[2]) {
		t.Errorf("row after the title = %q, want the header rule", lines[2])
	}
	if !blank(lines[3]) {
		t.Errorf("row after the rule = %q, want the parent gap", lines[3])
	}
}

// TestListDialogEmptyView covers both the default "No results found" and the
// emptyView a caller supplies in its place.
func TestListDialogEmptyView(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Skills", nil)
	if !strings.Contains(strings.Join(panelLines(t, app), "\n"), "   No results found") {
		t.Error("an empty list should fall back to No results found")
	}

	app.overlay.SetEmptyTitle("Could not load skills")
	app.overlay.SetEmptyBody("connection refused")
	rendered := strings.Join(panelLines(t, app), "\n")
	if strings.Contains(rendered, "No results found") {
		t.Error("emptyView should replace the default fallback")
	}
	if !strings.Contains(rendered, "   Could not load skills") ||
		!strings.Contains(rendered, "   connection refused") {
		t.Errorf("emptyView not rendered:\n%s", rendered)
	}
}

// TestEmptyViewIsRedOnlyWhenTheLoadFailed pins §9.5's distinction: a list
// that is genuinely empty is not an error, and must not be colored as one.
func TestEmptyViewIsRedOnlyWhenTheLoadFailed(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Memories", nil)
	app.overlay.SetEmptyView("No memories", "Add one with the memory tool.")
	// The foreground SGR parameters alone: the title also carries bold and
	// a background, so the rendered prefix is not a single sequence.
	errSeq := ansiForeground(t, app.theme.Error)

	panel, _ := app.overlayPanel()
	if strings.Contains(panel, errSeq) {
		t.Error("an empty list is not a failed one; its title must not be red")
	}

	app.overlay.SetLocked(true)
	if panel, _ = app.overlayPanel(); !strings.Contains(panel, errSeq) {
		t.Error("a locked list reports a failed load, and its title is red")
	}
}

// TestLockedListIgnoresInput pins DialogSelect's locked prop: filtering and
// movement are inert, and only escape still closes the dialog.
func TestLockedListIgnoresInput(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.openList("Skills", []overlayItem{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}})
	app.overlay.SetLocked(true)
	for _, key := range []string{"down", "x", "enter"} {
		app.handleOverlayKey(key)
	}
	if app.overlay == nil {
		t.Fatal("a locked dialog should stay open")
	}
	if item, _ := app.overlay.SelectedItem(); item.Value != "a" || app.overlay.Filter() != "" {
		t.Errorf("locked dialog moved to %+v / filtered %q",
			item.Value, app.overlay.Filter())
	}
	app.handleOverlayKey("esc")
	if app.overlay != nil {
		t.Error("escape should still close a locked dialog")
	}
}

// ansiForeground returns the "38;2;r;g;b" parameters a color renders as, for
// asserting that a span is painted in a given theme token.
func ansiForeground(t *testing.T, c color.Color) string {
	t.Helper()
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("38;2;%d;%d;%d", r>>8, g>>8, b>>8)
}
