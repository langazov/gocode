package tui

import (
	"github.com/langazov/gocode-go/internal/tui/dialog"
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

// The row box's gutter holds the ● in the same column the header title and
// the category labels sit in (dialog.PadX), and the title clears it by two.
// The bullet fills that gutter without shifting the title.
func TestListRowTitlesAlignAtTheSameColumn(t *testing.T) {
	app := listApp(t,
		overlayItem{Label: "alpha", Value: "a"},
		overlayItem{Label: "beta", Value: "b"},
	)
	app.overlay.SetCurrent("b")

	var plain, marked string
	for _, line := range panelLines(t, app) {
		if strings.Contains(line, "alpha") {
			plain = line
		}
		if strings.Contains(line, "beta") {
			marked = line
		}
	}
	if got := cellIndex(plain, "alpha"); got != dialog.PadX+2 {
		t.Fatalf("plain title starts at column %d, want %d", got, dialog.PadX+2)
	}
	if got := cellIndex(marked, "beta"); got != dialog.PadX+2 {
		t.Fatalf("the current row's title must not shift, starts at column %d, want %d",
			got, dialog.PadX+2)
	}
	if got := cellIndex(marked, "●"); got != dialog.PadX {
		t.Fatalf("the ● gutter sits at column %d, want %d — the column the header "+
			"title and the category labels share", got, dialog.PadX)
	}
}

// The background lives on the row box, so a highlighted row is filled edge to
// edge — including both paddings, which this port used to leave unstyled.
func TestSelectedRowHighlightSpansThePaddings(t *testing.T) {
	app := listApp(t, overlayItem{Label: "alpha", Value: "a"})
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
		overlayItem{Label: "alpha", Category: "Session", Value: "a"},
		overlayItem{Label: "beta", Category: "Model", Value: "b"},
	)
	lines := panelLines(t, app)
	found := false
	for i, line := range lines {
		if !strings.Contains(line, "SESSION") || strings.Contains(line, "Commands") {
			continue
		}
		found = true
		if got := cellIndex(line, "SESSION"); got != dialog.PadX {
			t.Fatalf("category header at column %d, want %d", got, dialog.PadX)
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
	app := listApp(t, overlayItem{Label: long, Value: "a"})
	app.overlay.SetSize(dialogXLarge)
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
	if got := len([]rune(trimmed)); got != 61 {
		t.Fatalf("title rendered %d runes, want %d", got, 61)
	}
}

func TestTruncateEllipsisMatchesLocaleTruncate(t *testing.T) {
	if got := dialog.TruncateEllipsis("hello", 10); got != "hello" {
		t.Fatalf("a short string is untouched, got %q", got)
	}
	if got := dialog.TruncateEllipsis("hello world", 5); got != "hell…" {
		t.Fatalf("truncate = %q, want %q", got, "hell…")
	}
}

// --- footer actions ---------------------------------------------------------

// The footer is a space-between row inside the shared content column: each
// action carries a cell of padding for the focus fill, so the left group's
// text starts at PadX and the right group's ends PadX short of the edge.
func TestFooterActionsSplitLeftAndRight(t *testing.T) {
	app := listApp(t, overlayItem{Label: "alpha", Value: "a"})
	app.overlay.SetActions([]dialogAction{
		{Title: "Select", Keys: "enter"},
		{Title: "Delete", Keys: "ctrl+d", Right: true},
	})
	var row string
	for _, line := range panelLines(t, app) {
		if strings.Contains(line, "Select") {
			row = line
		}
	}
	if got := cellIndex(row, "Select"); got != dialog.PadX {
		t.Fatalf("the left group starts at column %d, want %d", got, dialog.PadX)
	}
	if got := len(row) - strings.Index(row, "Delete ctrl+d"); got != len("Delete ctrl+d")+dialog.PadX {
		t.Fatalf("the right group should end %d columns short of the edge, got %q",
			dialog.PadX, row)
	}
}

// moveAction(): tab enters at the first action, shift+tab at the last, and
// stepping off either end releases focus back to the list.
func TestTabCyclesFooterActionFocusAndReleases(t *testing.T) {
	app := listApp(t, overlayItem{Label: "alpha", Value: "a"})
	app.overlay.SetActions([]dialogAction{{Title: "One", Keys: "1"}, {Title: "Two", Keys: "2"}})
	o := app.overlay

	app.handleOverlayKey("tab")
	if o.FocusedAction() != 0 {
		t.Fatalf("tab should focus the first action, got %d", o.FocusedAction())
	}
	app.handleOverlayKey("tab")
	if o.FocusedAction() != 1 {
		t.Fatalf("tab should advance, got %d", o.FocusedAction())
	}
	app.handleOverlayKey("tab")
	if o.FocusedAction() != -1 {
		t.Fatalf("stepping off the end releases focus, got %d", o.FocusedAction())
	}
	app.handleOverlayKey("shift+tab")
	if o.FocusedAction() != len(o.Actions())-1 {
		t.Fatalf("shift+tab should enter at the last action, got %d", o.FocusedAction())
	}
	// moveTo() clears the focused action.
	app.handleOverlayKey("down")
	if o.FocusedAction() != -1 {
		t.Fatalf("moving the selection releases action focus, got %d", o.FocusedAction())
	}
}

// While an action is focused the selected row steps back to backgroundElement
// and its text goes muted (Option's `muted` prop).
func TestFocusedActionMutesTheSelectedRow(t *testing.T) {
	app := listApp(t, overlayItem{Label: "alpha", Value: "a"})
	app.overlay.SetActions([]dialogAction{{Title: "One", Keys: "1"}})

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
	app := listApp(t, overlayItem{Label: "alpha", Value: "a", Action: func() tea.Msg { return nil }})
	app.overlay.SetActions([]dialogAction{{Title: "One", Keys: "1", OnTrigger: func(overlayItem) tea.Cmd {
		triggered = true
		return nil
	}}})

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
		overlayItem{Label: "jkl", Value: "a"},
		overlayItem{Label: "other", Value: "b"},
	)
	app.handleOverlayKey("j")
	app.handleOverlayKey("k")
	if app.overlay.Filter() != "jk" {
		t.Fatalf("j and k should type into the filter, got %q", app.overlay.Filter())
	}
}

func TestDialogNavigationKeys(t *testing.T) {
	items := make([]overlayItem, 30)
	for i := range items {
		items[i] = overlayItem{Label: string(rune('a' + i%26)), Value: string(rune('a' + i))}
	}
	app := listApp(t, items...)
	o := app.overlay

	app.handleOverlayKey("ctrl+n")
	if o.SelectedIndex() != 1 {
		t.Fatalf("ctrl+n should advance, got %d", o.SelectedIndex())
	}
	app.handleOverlayKey("ctrl+p")
	if o.SelectedIndex() != 0 {
		t.Fatalf("ctrl+p should go back, got %d", o.SelectedIndex())
	}
	app.handleOverlayKey("pagedown")
	if o.SelectedIndex() != 10 {
		t.Fatalf("pagedown moves ten, got %d", o.SelectedIndex())
	}
	app.handleOverlayKey("end")
	if o.SelectedIndex() != len(items)-1 {
		t.Fatalf("end goes to the last item, got %d", o.SelectedIndex())
	}
	app.handleOverlayKey("home")
	if o.SelectedIndex() != 0 {
		t.Fatalf("home goes to the first item, got %d", o.SelectedIndex())
	}
	// move() wraps at both ends.
	app.handleOverlayKey("up")
	if o.SelectedIndex() != len(items)-1 {
		t.Fatalf("moving up from the first item wraps, got %d", o.SelectedIndex())
	}
}

// --- scrolling --------------------------------------------------------------

// move() passes center=true, so the arrow keys recenter the selection; moveTo()
// (home/end, mouse hover) keeps its default and scrolls the minimum needed.
func TestArrowsCenterWhileHomeEndScrollMinimally(t *testing.T) {
	items := make([]overlayItem, 40)
	for i := range items {
		items[i] = overlayItem{Label: strings.Repeat("x", i%5+3), Value: string(rune('a' + i))}
	}
	app := listApp(t, items...)
	app.height = 30 // maxRows = 30/2 - 6 = 9
	app.overlay.SetGeometry(app.width, app.height, app.palette())
	o := app.overlay

	app.overlay.MoveTo(20)
	app.overlayPanel()
	minimal := o.ScrollPos()
	if minimal != 20-9+1 {
		t.Fatalf("moveTo should scroll just far enough, top = %d, want %d", minimal, 20-9+1)
	}

	app.overlay.Move(1)
	app.overlayPanel()
	if o.ScrollPos() != 21-9/2 {
		t.Fatalf("move() recenters, top = %d, want %d", o.ScrollPos(), 21-9/2)
	}
}

// --- backdrop ---------------------------------------------------------------

// ui/dialog.tsx's scrim is black at 150/255 over the whole screen. This port
// had recorded it as impossible and left the content behind a dialog at full
// brightness. The scrim is now applied cell by cell inside compositeDialog's
// canvas (see composite.go); these tests drive the same pass.
func TestBackdropDimsTheContentBehindTheDialog(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 90, 24

	bright := lipgloss.NewStyle().Foreground(lipgloss.Color("#c0caf5")).Render("hello")
	dimmed := app.compositeDialog(bright, "", 0, 0)

	if strings.Contains(dimmed, "192;202;245") {
		t.Fatalf("the source color should not survive the scrim: %q", dimmed)
	}
	// #c0caf5 scaled by 1 - 150/255.
	if !strings.Contains(dimmed, "79;83;101") {
		t.Fatalf("expected the blended color, got %q", dimmed)
	}
	// The canvas pads every cell to the terminal dimensions, so compare the
	// text of the first line rather than the whole frame.
	if first := strings.SplitN(ansi.Strip(dimmed), "\n", 2)[0]; strings.TrimRight(first, " ") != "hello" {
		t.Fatalf("dimming must not change the text, got %q", first)
	}
}

func TestBackdropDimsUnstyledCellsToTheThemeDefaults(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	dimmed := app.compositeDialog("plain", "", 0, 0)
	// An unstyled cell resolves to the theme's own colors, pre-blended, so
	// the whole frame reads dimmed rather than punching through the scrim.
	if !strings.Contains(dimmed, "38;2;") {
		t.Fatalf("an unstyled cell should carry the dimmed theme default, got %q", dimmed)
	}
}

func TestBackdropConvertsIndexedColours(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	// 231 is the top of the 6x6x6 cube: pure white.
	dimmed := app.compositeDialog("\x1b[38;5;231mx\x1b[m", "", 0, 0)
	if !strings.Contains(dimmed, "38;2;105;105;105") {
		t.Fatalf("a 256-colour index should be converted then blended, got %q", dimmed)
	}
}

// The dialog panel itself is drawn on top of the scrim and keeps full brightness.
func TestDialogPanelIsNotDimmed(t *testing.T) {
	app := listApp(t, overlayItem{Label: "alpha", Value: "a"})
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

// --- the frame ----------------------------------------------------------------

// Every row of every dialog is exactly the panel's width. A row that is
// short leaves the frame behind it showing through; a row that is long
// tears the line the compositor splices it into. Both have happened, and
// neither is visible in a test that only greps the panel for its text.
func TestEveryDialogRowIsExactlyPanelWide(t *testing.T) {
	long := strings.Repeat("a long session title ", 6)
	cases := map[string]func(*App){
		"list": func(a *App) {
			a.openList("Commands", []overlayItem{
				{Label: "New session", Value: "n", Category: "Session", Hint: "start fresh", Footer: "ctrl+x n"},
				{Label: long, Value: "l", Category: "Session", Hint: long},
				{Label: "Select model", Value: "m", Category: "Config"},
			})
			a.overlay.SetSize(dialogLarge)
			a.overlay.SetCurrent("m")
			a.overlay.SetActions([]dialogAction{
				{Title: "delete", Keys: "ctrl+d"},
				{Title: "close", Keys: "esc", Right: true},
			})
		},
		"list scrolled": func(a *App) {
			var items []overlayItem
			for i := 0; i < 40; i++ {
				items = append(items, overlayItem{
					Label: "session", Value: string(rune('a' + i%26)), Category: "Today", Footer: "2h",
				})
			}
			a.openList("Sessions", items)
			a.overlay.Move(20)
		},
		"list empty": func(a *App) { a.openList("Memories", nil) },
		"input":      func(a *App) { a.openInput("Rename session", long, "", nil) },
		"alert":      func(a *App) { a.openAlert("Retry error", long, nil) },
		"confirm":    func(a *App) { a.openConfirm("Delete session", long, "Keep", nil, nil) },
		"help":       func(a *App) { a.openHelpDialog("Shortcuts", []string{"ctrl+x n  new session", long}) },
		"status":     func(a *App) { a.openStatusDialog() },
		"stats":      func(a *App) { drive(t, a, a.openStatsOverlay()) },
		"list narrow": func(a *App) {
			a.width, a.height = 40, 16
			a.openList("Commands", []overlayItem{{Label: long, Hint: long, Footer: "ctrl+x n"}})
		},
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, "http://example.invalid")
			app.width, app.height = 120, 34
			open(app)
			panel, _ := app.overlayPanel()
			want := app.overlay.Width()
			for i, line := range strings.Split(panel, "\n") {
				if got := lipgloss.Width(line); got != want {
					t.Errorf("row %d is %d cells, want %d: %q", i, got, want, ansi.Strip(line))
				}
			}
		})
	}
}
