package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// --- textSelection / column math (pure) -----------------------------------

func TestTextSelectionHasRange(t *testing.T) {
	var s textSelection
	s.begin(2, 3)
	if s.hasRange() {
		t.Fatalf("hasRange() = true right after begin, want false (a plain click)")
	}
	s.extend(2, 4)
	if !s.hasRange() {
		t.Fatalf("hasRange() = false after extending, want true")
	}
}

func TestTextSelectionOrderedNormalizesDragDirection(t *testing.T) {
	var s textSelection
	s.begin(5, 10)
	s.extend(2, 3)
	sr, sc, er, ec := s.ordered()
	if sr != 2 || sc != 3 || er != 5 || ec != 10 {
		t.Fatalf("ordered() = (%d,%d,%d,%d), want (2,3,5,10)", sr, sc, er, ec)
	}
}

func TestTextSelectionOrderedSameRow(t *testing.T) {
	var s textSelection
	s.begin(4, 10)
	s.extend(4, 2)
	sr, sc, er, ec := s.ordered()
	if sr != 4 || sc != 2 || er != 4 || ec != 10 {
		t.Fatalf("ordered() = (%d,%d,%d,%d), want (4,2,4,10)", sr, sc, er, ec)
	}
}

func TestSelectionColsSingleLine(t *testing.T) {
	cs, ce := selectionCols(3, 3, 5, 3, 9, 20)
	if cs != 5 || ce != 10 {
		t.Fatalf("selectionCols single-line = (%d,%d), want (5,10)", cs, ce)
	}
}

func TestSelectionColsMultiLine(t *testing.T) {
	// A selection spanning rows 1..3: the first row opens from its start
	// column through the line's end, the middle row is fully selected, and
	// the last row runs from 0 through its end column (inclusive).
	if cs, ce := selectionCols(1, 1, 5, 3, 2, 20); cs != 5 || ce != 20 {
		t.Fatalf("first row = (%d,%d), want (5,20)", cs, ce)
	}
	if cs, ce := selectionCols(2, 1, 5, 3, 2, 20); cs != 0 || ce != 20 {
		t.Fatalf("middle row = (%d,%d), want (0,20)", cs, ce)
	}
	if cs, ce := selectionCols(3, 1, 5, 3, 2, 20); cs != 0 || ce != 3 {
		t.Fatalf("last row = (%d,%d), want (0,3)", cs, ce)
	}
}

func TestSelectionColsClampsToLineWidth(t *testing.T) {
	// Dragging past a short line's end must not request an out-of-range cut.
	cs, ce := selectionCols(0, 0, 2, 0, 50, 8)
	if cs != 2 || ce != 8 {
		t.Fatalf("selectionCols past line end = (%d,%d), want (2,8)", cs, ce)
	}
}

// --- applySelectionHighlight / extractSelection ---------------------------

func TestApplySelectionHighlightWrapsExactRange(t *testing.T) {
	app := &App{width: 80, height: 24}
	app.selection.begin(0, 2)
	app.selection.extend(0, 4)
	got := app.applySelectionHighlight("abcdef")
	want := "ab" + "\x1b[7m" + "cde" + "\x1b[27m" + "f"
	if got != want {
		t.Fatalf("applySelectionHighlight = %q, want %q", got, want)
	}
}

// TestApplySelectionHighlightSurvivesEmbeddedResets is the regression for
// "mouse selection doesn't properly select rendered code blocks": glamour's
// chroma-highlighted code emits a full SGR reset (\x1b[0m) after nearly
// every token, and each one previously canceled the outer \x1b[7m
// reverse-video wrap the moment the selection crossed a second token,
// leaving only the first token visibly highlighted.
func TestApplySelectionHighlightSurvivesEmbeddedResets(t *testing.T) {
	app := &App{width: 80, height: 24}
	// "fmt.Println" as three separately colored-and-reset chroma tokens,
	// same shape terminal16m output actually has (see markdown.go).
	line := "\x1b[38;2;100;100;100mfmt\x1b[0m\x1b[38;2;200;200;200m.\x1b[0m\x1b[38;2;50;150;250mPrintln\x1b[0m"
	app.selection.begin(0, 0)
	app.selection.extend(0, 10) // whole "fmt.Println" (11 visible chars)
	got := app.applySelectionHighlight(line)

	start := strings.Index(got, "\x1b[7m")
	end := strings.Index(got, "\x1b[27m")
	if start == -1 || end == -1 || end < start {
		t.Fatalf("expected a \\x1b[7m..\\x1b[27m wrap, got %q", got)
	}
	middle := got[start+len("\x1b[7m") : end]
	if strings.Contains(middle, "\x1b[0m") {
		t.Fatalf("an embedded reset inside the highlighted span cancels reverse-video partway through, got middle %q", middle)
	}
	if middle != "fmt.Println" {
		t.Fatalf("highlighted span text = %q, want the full unstyled %q", middle, "fmt.Println")
	}
}

func TestApplySelectionHighlightNoRangeIsNoop(t *testing.T) {
	app := &App{width: 80, height: 24}
	app.selection.begin(0, 2) // press with no drag: not a range yet
	content := "unchanged"
	if got := app.applySelectionHighlight(content); got != content {
		t.Fatalf("applySelectionHighlight with no range = %q, want unchanged %q", got, content)
	}
}

func TestExtractSelectionSingleLine(t *testing.T) {
	var s textSelection
	s.begin(0, 2)
	s.extend(0, 4)
	if got := extractSelection("abcdef", s, 0, 1<<30); got != "cde" {
		t.Fatalf("extractSelection = %q, want %q", got, "cde")
	}
}

func TestExtractSelectionMultiLineTrimsTrailingSpace(t *testing.T) {
	var s textSelection
	s.begin(0, 2)
	s.extend(1, 1)
	content := "hello world  \ngoodbye"
	got := extractSelection(content, s, 0, 1<<30)
	want := "llo world\ngo"
	if got != want {
		t.Fatalf("extractSelection = %q, want %q", got, want)
	}
}

// --- overlay hit-testing ---------------------------------------------------

func testListOverlay(items ...overlayItem) *overlay {
	return &overlay{kind: overlayList, title: "Test", items: items, all: items}
}

// rowForItem finds the panel-relative row hits.rowItem assigns to item index.
func rowForItem(hits *overlayHits, item int) int {
	for row, idx := range hits.rowItem {
		if idx == item {
			return row
		}
	}
	return -1
}

func TestOverlayMouseTargetResolvesItemEscAndBackdrop(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.overlay = testListOverlay(
		overlayItem{label: "Alpha", value: "a"},
		overlayItem{label: "Bravo", value: "b"},
	)

	panel, hits := app.overlayPanel()
	top, left := app.overlayOrigin(lipgloss.Width(panel))

	row := rowForItem(hits, 1)
	if row < 0 {
		t.Fatalf("hits.rowItem has no row for item 1: %v", hits.rowItem)
	}
	if target := app.overlayMouseTarget(top+row, left+5); target.kind != overlayTargetItem || target.item != 1 {
		t.Fatalf("overlayMouseTarget on Bravo's row = %+v, want item 1", target)
	}

	if hits.escRow < 0 {
		t.Fatalf("list overlay should have an esc hint row")
	}
	if target := app.overlayMouseTarget(top+hits.escRow, left+hits.escStart); target.kind != overlayTargetEsc {
		t.Fatalf("overlayMouseTarget on the esc hint = %+v, want overlayTargetEsc", target)
	}

	if target := app.overlayMouseTarget(0, 0); target.kind != overlayTargetBackdrop {
		t.Fatalf("overlayMouseTarget at (0,0) = %+v, want overlayTargetBackdrop (top=%d)", target, top)
	}
}

func TestOverlayMouseTargetResolvesFooterAction(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.overlay = testListOverlay(overlayItem{label: "Alpha", value: "a"})
	app.overlay.actions = []dialogAction{{title: "delete", keys: "ctrl+d"}}

	panel, hits := app.overlayPanel()
	top, left := app.overlayOrigin(lipgloss.Width(panel))
	if hits.actionRow < 0 || len(hits.actions) == 0 {
		t.Fatalf("expected a footer action row, hits=%+v", hits)
	}
	span := hits.actions[0]
	target := app.overlayMouseTarget(top+hits.actionRow, left+span.start)
	if target.kind != overlayTargetAction || target.action != 0 {
		t.Fatalf("overlayMouseTarget on the action label = %+v, want action 0", target)
	}
}

// --- end-to-end mouse dispatch ---------------------------------------------

func TestMouseHoverPreselectsWithoutActivating(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	activated := false
	app.overlay = testListOverlay(
		overlayItem{label: "Alpha", value: "a"},
		overlayItem{label: "Bravo", value: "b", action: func() tea.Msg { activated = true; return nil }},
	)

	panel, hits := app.overlayPanel()
	top, left := app.overlayOrigin(lipgloss.Width(panel))
	row := rowForItem(hits, 1)

	drive(t, app, tea.MouseMotionMsg{X: left + 5, Y: top + row})
	if app.overlay == nil || app.overlay.selected != 1 {
		t.Fatalf("hovering item 1 should preselect it without activating, overlay=%+v", app.overlay)
	}
	if activated {
		t.Fatalf("hover must not activate the item")
	}
}

func TestMousePlainClickActivatesRowAndClosesDialog(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	activated := false
	app.overlay = testListOverlay(
		overlayItem{label: "Alpha", value: "a"},
		overlayItem{label: "Bravo", value: "b", action: func() tea.Msg { activated = true; return nil }},
	)

	panel, hits := app.overlayPanel()
	top, left := app.overlayOrigin(lipgloss.Width(panel))
	row := rowForItem(hits, 1)

	drive(t, app, tea.MouseClickMsg{X: left + 5, Y: top + row, Button: tea.MouseLeft})
	drive(t, app, tea.MouseReleaseMsg{X: left + 5, Y: top + row, Button: tea.MouseLeft})

	if !activated {
		t.Fatalf("a plain click on the row should activate it")
	}
	if app.overlay != nil {
		t.Fatalf("activating an item should close the dialog")
	}
}

func TestMouseClickBackdropClosesDialog(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.overlay = testListOverlay(overlayItem{label: "Alpha", value: "a"})

	drive(t, app, tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	drive(t, app, tea.MouseReleaseMsg{X: 0, Y: 0, Button: tea.MouseLeft})

	if app.overlay != nil {
		t.Fatalf("clicking the backdrop should close the dialog")
	}
}

func TestMouseDragSelectsAndCopiesWithoutActivating(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	activated := false
	app.overlay = testListOverlay(overlayItem{label: "Alpha", value: "a", action: func() tea.Msg { activated = true; return nil }})

	// Drag across the dialog's own "Test" title (the esc-hint row, pad 4) so
	// there's real, non-blank text under the selection to copy.
	panel, hits := app.overlayPanel()
	top, left := app.overlayOrigin(lipgloss.Width(panel))
	titleRow := top + hits.escRow
	// Update directly (not drive): the release's returned Cmd is the toast's
	// real-time expiry tick, which a full drive would run to completion.
	app.Update(tea.MouseClickMsg{X: left + 4, Y: titleRow, Button: tea.MouseLeft})
	app.Update(tea.MouseMotionMsg{X: left + 8, Y: titleRow})
	app.Update(tea.MouseReleaseMsg{X: left + 8, Y: titleRow, Button: tea.MouseLeft})

	if activated {
		t.Fatalf("a drag-release must not activate whatever is under the cursor")
	}
	if app.selection.hasRange() {
		t.Fatalf("the selection should be cleared once the drag is copied")
	}
	if app.toast == nil || !strings.Contains(app.toast.text, "Copied") {
		t.Fatalf("a real drag-release should copy and toast, toast=%+v", app.toast)
	}
}

func TestMouseWheelScrollsChatAndOverlayList(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.view = viewChat
	app.scrollOffset = 5

	drive(t, app, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if app.scrollOffset != 2 {
		t.Fatalf("wheel down should decrease scrollOffset by %d, got %d", wheelScrollLines, app.scrollOffset)
	}
	drive(t, app, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if app.scrollOffset != 5 {
		t.Fatalf("wheel up should increase scrollOffset by %d, got %d", wheelScrollLines, app.scrollOffset)
	}

	app.overlay = testListOverlay(
		overlayItem{value: "a"}, overlayItem{value: "b"}, overlayItem{value: "c"},
	)
	drive(t, app, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if app.overlay.selected != 1 {
		t.Fatalf("wheel down over an open list dialog should move the selection, got %d", app.overlay.selected)
	}
	drive(t, app, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if app.overlay.selected != 0 {
		t.Fatalf("wheel up over an open list dialog should move the selection back, got %d", app.overlay.selected)
	}
}

// --- keyboard interaction with a pending selection -------------------------

func TestCtrlCCopiesSelectionInsteadOfQuitting(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.overlay = testListOverlay(overlayItem{label: "Alpha", value: "a"})
	panel, hits := app.overlayPanel()
	top, left := app.overlayOrigin(lipgloss.Width(panel))
	titleRow := top + hits.escRow
	app.selection.begin(titleRow, left+4) // over the dialog's "Test" title
	app.selection.extend(titleRow, left+8)

	// Update directly (not drive): see the note in the drag-copy test above.
	app.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	if app.quitting {
		t.Fatalf("ctrl+c with a pending selection should copy, not quit")
	}
	if app.toast == nil || !strings.Contains(app.toast.text, "Copied") {
		t.Fatalf("ctrl+c with a pending selection should copy and toast, toast=%+v", app.toast)
	}
}

func TestEscapeClearsSelectionBeforeAnythingElse(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 100, 40
	app.selection.begin(0, 0)
	app.selection.extend(0, 3)

	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})

	if app.selection.hasRange() {
		t.Fatalf("escape should clear a pending selection")
	}
}
