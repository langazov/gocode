package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func listApp(t *testing.T, items ...overlayItem) *App {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 34
	app.openList("Commands", items)
	return app
}

// --- geometry ---------------------------------------------------------------

// DialogSelect's row box pads 1 (current) or 3, and the *title text inside it*
// pads another 3, so a title always starts at column 6 of the scrollbox — and
// the scrollbox itself pads 1. The bullet fills the gutter of a current row
// without shifting its title.
func TestListRowTitlesAlignAtTheSameColumn(t *testing.T) {
	app := listApp(t,
		overlayItem{label: "alpha", value: "a"},
		overlayItem{label: "beta", value: "b"},
	)
	app.overlay.current = "b"

	var plain, marked string
	for _, line := range panelLines(t, app) {
		if strings.Contains(line, "alpha") {
			plain = line
		}
		if strings.Contains(line, "beta") {
			marked = line
		}
	}
	if got := cellIndex(plain, "alpha"); got != 7 {
		t.Fatalf("plain title starts at column %d, want 7 (scrollbox 1 + row 3 + text 3)", got)
	}
	if got := cellIndex(marked, "beta"); got != 7 {
		t.Fatalf("the current row's title must not shift, starts at column %d, want 7", got)
	}
	if got := cellIndex(marked, "●"); got != 2 {
		t.Fatalf("the ● gutter sits at column %d, want 2 (scrollbox 1 + row paddingLeft 1)", got)
	}
}

// The background lives on the row box, so a highlighted row is filled edge to
// edge — including both paddings, which this port used to leave unstyled.
func TestSelectedRowHighlightSpansThePaddings(t *testing.T) {
	app := listApp(t, overlayItem{label: "alpha", value: "a"})
	panel, _ := app.overlayPanel()

	var row string
	for _, line := range strings.Split(panel, "\n") {
		if strings.Contains(ansi.Strip(line), "alpha") {
			row = line
		}
	}
	if row == "" {
		t.Fatal("no row rendered")
	}
	// The scrollbox pads 1 in the panel color; every cell from column 1 to
	// the last content column carries the primary fill.
	primary := lipgloss.NewStyle().Background(app.theme.Primary).Render(" ")
	fillSeq := primary[:strings.Index(primary, "m")+1]
	if !strings.Contains(row, fillSeq) {
		t.Fatalf("the selected row should carry the primary fill, got %q", row)
	}
	plain := ansi.Strip(row)
	if !strings.HasPrefix(plain, " ") || !strings.HasSuffix(plain, " ") {
		t.Fatalf("the scrollbox pads 1 outside the fill, got %q", plain)
	}
}

// Category headers get the scrollbox's pad plus their own paddingLeft={3}.
func TestCategoryHeadersIndentByFour(t *testing.T) {
	app := listApp(t,
		overlayItem{label: "alpha", category: "Session", value: "a"},
		overlayItem{label: "beta", category: "Model", value: "b"},
	)
	lines := panelLines(t, app)
	found := false
	for i, line := range lines {
		if !strings.Contains(line, "Session") || strings.Contains(line, "Commands") {
			continue
		}
		found = true
		if got := cellIndex(line, "Session"); got != 4 {
			t.Fatalf("category header at column %d, want 4", got)
		}
		// paddingTop={index > 0 ? 1 : 0}: the first group has no blank above.
		if strings.TrimSpace(lines[i-1]) != "" {
			t.Fatalf("expected the gap row above the first category, got %q", lines[i-1])
		}
	}
	if !found {
		t.Fatal("no category header rendered")
	}
}

// Locale.truncate(title, titleWidth ?? 61) runs before layout, so the ellipsis
// appears even when the dialog is wide enough to hold the whole title.
func TestListRowTruncatesTitlesAtSixtyOne(t *testing.T) {
	long := strings.Repeat("x", 80)
	app := listApp(t, overlayItem{label: long, value: "a"})
	app.overlay.size = dialogXLarge
	app.width = 140

	var row string
	for _, line := range panelLines(t, app) {
		if strings.Contains(line, "xxx") {
			row = line
		}
	}
	trimmed := strings.TrimSpace(row)
	if !strings.HasSuffix(trimmed, "…") {
		t.Fatalf("a long title should carry the ellipsis, got %q", trimmed)
	}
	if got := len([]rune(trimmed)); got != dialogTitleWidth {
		t.Fatalf("title rendered %d runes, want %d", got, dialogTitleWidth)
	}
}

func TestTruncateEllipsisMatchesLocaleTruncate(t *testing.T) {
	if got := truncateEllipsis("hello", 10); got != "hello" {
		t.Fatalf("a short string is untouched, got %q", got)
	}
	if got := truncateEllipsis("hello world", 5); got != "hell…" {
		t.Fatalf("truncate = %q, want %q", got, "hell…")
	}
}

// --- footer actions ---------------------------------------------------------

// The footer is a space-between row: `paddingLeft={4}` on the left group,
// `paddingRight={2}` on the right one.
func TestFooterActionsSplitLeftAndRight(t *testing.T) {
	app := listApp(t, overlayItem{label: "alpha", value: "a"})
	app.overlay.actions = []dialogAction{
		{title: "Select", keys: "enter"},
		{title: "Delete", keys: "ctrl+d", right: true},
	}
	var row string
	for _, line := range panelLines(t, app) {
		if strings.Contains(line, "Select") {
			row = line
		}
	}
	if got := cellIndex(row, "Select"); got != 4 {
		t.Fatalf("the left group starts at column %d, want 4", got)
	}
	if got := len(row) - strings.Index(row, "Delete ctrl+d"); got != len("Delete ctrl+d")+2 {
		t.Fatalf("the right group should end 2 columns short of the edge, got %q", row)
	}
}

// moveAction(): tab enters at the first action, shift+tab at the last, and
// stepping off either end releases focus back to the list.
func TestTabCyclesFooterActionFocusAndReleases(t *testing.T) {
	app := listApp(t, overlayItem{label: "alpha", value: "a"})
	app.overlay.actions = []dialogAction{{title: "One", keys: "1"}, {title: "Two", keys: "2"}}
	o := app.overlay

	app.handleOverlayKey("tab")
	if o.focusedAction != 0 {
		t.Fatalf("tab should focus the first action, got %d", o.focusedAction)
	}
	app.handleOverlayKey("tab")
	if o.focusedAction != 1 {
		t.Fatalf("tab should advance, got %d", o.focusedAction)
	}
	app.handleOverlayKey("tab")
	if o.focusedAction != -1 {
		t.Fatalf("stepping off the end releases focus, got %d", o.focusedAction)
	}
	app.handleOverlayKey("shift+tab")
	if o.focusedAction != len(o.actions)-1 {
		t.Fatalf("shift+tab should enter at the last action, got %d", o.focusedAction)
	}
	// moveTo() clears the focused action.
	app.handleOverlayKey("down")
	if o.focusedAction != -1 {
		t.Fatalf("moving the selection releases action focus, got %d", o.focusedAction)
	}
}

// While an action is focused the selected row steps back to backgroundElement
// and its text goes muted (Option's `muted` prop).
func TestFocusedActionMutesTheSelectedRow(t *testing.T) {
	app := listApp(t, overlayItem{label: "alpha", value: "a"})
	app.overlay.actions = []dialogAction{{title: "One", keys: "1"}}

	rowOf := func() string {
		panel, _ := app.overlayPanel()
		for _, line := range strings.Split(panel, "\n") {
			if strings.Contains(ansi.Strip(line), "alpha") {
				return line
			}
		}
		return ""
	}
	primary := lipgloss.NewStyle().Background(app.theme.Primary).Render(" ")
	element := lipgloss.NewStyle().Background(app.theme.BackgroundElement).Render(" ")
	primarySeq := primary[:strings.Index(primary, "m")+1]
	elementSeq := element[:strings.Index(element, "m")+1]

	if !strings.Contains(rowOf(), primarySeq) {
		t.Fatal("an unfocused footer leaves the selection on the primary fill")
	}
	app.handleOverlayKey("tab")
	row := rowOf()
	if strings.Contains(row, primarySeq) {
		t.Fatal("a focused action should take the primary fill off the row")
	}
	if !strings.Contains(row, elementSeq) {
		t.Fatal("the muted selection sits on backgroundElement")
	}
}

// enter triggers the focused action instead of the selected item (submit()).
func TestEnterTriggersTheFocusedAction(t *testing.T) {
	triggered := false
	app := listApp(t, overlayItem{label: "alpha", value: "a", action: func() tea.Msg { return nil }})
	app.overlay.actions = []dialogAction{{title: "One", keys: "1", onTrigger: func(overlayItem) tea.Cmd {
		triggered = true
		return nil
	}}}

	app.handleOverlayKey("tab")
	app.handleOverlayKey("enter")
	if !triggered {
		t.Fatal("enter should trigger the focused footer action")
	}
}

// --- keybindings ------------------------------------------------------------

// config/keybind.ts binds prev/next to up,ctrl+p / down,ctrl+n. j and k are
// NOT navigation: the filter input owns the keyboard, so they are characters
// to type — binding them to movement made those letters unsearchable.
func TestFilterAcceptsLettersThatArePagerKeysElsewhere(t *testing.T) {
	app := listApp(t,
		overlayItem{label: "jkl", value: "a"},
		overlayItem{label: "other", value: "b"},
	)
	app.handleOverlayKey("j")
	app.handleOverlayKey("k")
	if app.overlay.filter != "jk" {
		t.Fatalf("j and k should type into the filter, got %q", app.overlay.filter)
	}
}

func TestDialogNavigationKeys(t *testing.T) {
	items := make([]overlayItem, 30)
	for i := range items {
		items[i] = overlayItem{label: string(rune('a' + i%26)), value: string(rune('a' + i))}
	}
	app := listApp(t, items...)
	o := app.overlay

	app.handleOverlayKey("ctrl+n")
	if o.selected != 1 {
		t.Fatalf("ctrl+n should advance, got %d", o.selected)
	}
	app.handleOverlayKey("ctrl+p")
	if o.selected != 0 {
		t.Fatalf("ctrl+p should go back, got %d", o.selected)
	}
	app.handleOverlayKey("pagedown")
	if o.selected != 10 {
		t.Fatalf("pagedown moves ten, got %d", o.selected)
	}
	app.handleOverlayKey("end")
	if o.selected != len(items)-1 {
		t.Fatalf("end goes to the last item, got %d", o.selected)
	}
	app.handleOverlayKey("home")
	if o.selected != 0 {
		t.Fatalf("home goes to the first item, got %d", o.selected)
	}
	// move() wraps at both ends.
	app.handleOverlayKey("up")
	if o.selected != len(items)-1 {
		t.Fatalf("moving up from the first item wraps, got %d", o.selected)
	}
}

// --- scrolling --------------------------------------------------------------

// move() passes center=true, so the arrow keys recenter the selection; moveTo()
// (home/end, mouse hover) keeps its default and scrolls the minimum needed.
func TestArrowsCenterWhileHomeEndScrollMinimally(t *testing.T) {
	items := make([]overlayItem, 40)
	for i := range items {
		items[i] = overlayItem{label: strings.Repeat("x", i%5+3), value: string(rune('a' + i))}
	}
	app := listApp(t, items...)
	app.height = 30 // maxRows = 30/2 - 6 = 9
	o := app.overlay

	app.moveSelectionTo(o, 20)
	app.overlayPanel()
	minimal := o.scrollTop
	if minimal != 20-9+1 {
		t.Fatalf("moveTo should scroll just far enough, top = %d, want %d", minimal, 20-9+1)
	}

	app.moveSelection(o, 1)
	app.overlayPanel()
	if o.scrollTop != 21-9/2 {
		t.Fatalf("move() recenters, top = %d, want %d", o.scrollTop, 21-9/2)
	}
}

// --- backdrop ---------------------------------------------------------------

// ui/dialog.tsx's scrim is black at 150/255 over the whole screen. This port
// had recorded it as impossible and left the content behind a dialog at full
// brightness.
func TestBackdropDimsTheContentBehindTheDialog(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 90, 24

	bright := lipgloss.NewStyle().Foreground(lipgloss.Color("#c0caf5")).Render("hello")
	dimmed := app.dimBackdrop(bright)

	if strings.Contains(dimmed, "192;202;245") {
		t.Fatalf("the source color should not survive the scrim: %q", dimmed)
	}
	// #c0caf5 scaled by 1 - 150/255.
	if !strings.Contains(dimmed, "79;83;101") {
		t.Fatalf("expected the blended color, got %q", dimmed)
	}
	if ansi.Strip(dimmed) != "hello" {
		t.Fatalf("dimming must not change the text, got %q", ansi.Strip(dimmed))
	}
}

func TestBackdropDimsUnstyledCellsToTheThemeDefaults(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	dimmed := app.dimBackdrop("plain")
	if !strings.HasPrefix(dimmed, "\x1b[38;2;") {
		t.Fatalf("an unstyled line should open with the dimmed defaults, got %q", dimmed)
	}
}

func TestBackdropConvertsIndexedColours(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	// 231 is the top of the 6x6x6 cube: pure white.
	dimmed := app.dimBackdrop("\x1b[38;5;231mx\x1b[m")
	if !strings.Contains(dimmed, "38;2;105;105;105") {
		t.Fatalf("a 256-colour index should be converted then blended, got %q", dimmed)
	}
}

// The dialog panel itself is spliced over the scrim and keeps full brightness.
func TestDialogPanelIsNotDimmed(t *testing.T) {
	app := listApp(t, overlayItem{label: "alpha", value: "a"})
	app.view = viewChat
	view := app.viewOverlay()
	// 238;238;238 is #eeeeee, this app's default theme's text color (see
	// theme.Dark).
	if !strings.Contains(view, "238;238;238") {
		t.Fatal("the panel's own text should keep its undimmed colour")
	}
}

// sliceCells used to keep only the escapes immediately before the window, so a
// line-level style opened further left (the scrim opens one per line) was lost
// for everything after the spliced panel.
func TestSliceCellsCarriesTheStyleActiveAtTheWindow(t *testing.T) {
	line := "\x1b[38;2;1;2;3mabcdefgh\x1b[m"
	got := sliceCells(line, 4, 8)
	if !strings.HasPrefix(got, "\x1b[38;2;1;2;3m") {
		t.Fatalf("the slice should re-open the active style, got %q", got)
	}
	if ansi.Strip(got) != "efgh" {
		t.Fatalf("sliced text = %q, want %q", ansi.Strip(got), "efgh")
	}
}

// cellIndex is strings.Index in screen columns rather than bytes — the ● in a
// current row's gutter is three bytes wide but one cell.
func cellIndex(line, substr string) int {
	at := strings.Index(line, substr)
	if at < 0 {
		return -1
	}
	return lipgloss.Width(line[:at])
}
