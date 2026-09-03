package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/tui/client"
)

func autocompleteApp(t *testing.T, commands int) *App {
	t.Helper()
	app := typingApp(t, 100, 40)
	app.commands = nil
	for i := 0; i < commands; i++ {
		app.commands = append(app.commands, client.Command{
			Name:        fmt.Sprintf("cmd%02d", i),
			Description: fmt.Sprintf("description %d", i),
		})
	}
	return app
}

// TestAutocompletePopupDoesNotGetCroppedWithLongHistory is the regression
// for "when the session has content bigger than one page, the / commands
// menu is truncated at the bottom". viewportHeight() budgeted the timeline
// window without reserving any rows for the autocomplete popup — unlike the
// permission banner, which it does account for — so with a short history
// there was always slack below the timeline's window to silently absorb
// the popup's extra rows, but once history actually filled the viewport
// the popup pushed the total rendered height past frame()'s MaxHeight crop
// and lost its own bottom rows along with the prompt box and footer
// beneath it.
func TestAutocompletePopupDoesNotGetCroppedWithLongHistory(t *testing.T) {
	app := autocompleteApp(t, 8) // enough rows that the popup has real height
	for i := 0; i < 80; i++ {
		app.timeline = append(app.timeline, client.Message{
			ID:   fmt.Sprintf("msg_%d", i),
			Type: "user",
			Data: []byte(fmt.Sprintf(`{"text":"line %d"}`, i)),
		})
	}
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !app.autocomplete.visible() {
		t.Fatal("expected the popup to be open")
	}

	view := app.View()
	if lines := strings.Split(view, "\n"); len(lines) > app.height {
		t.Fatalf("view is %d lines tall, want at most app.height=%d — frame() cropped something away", len(lines), app.height)
	}

	plain := stripANSI(view)
	// The popup is capped at 10 rows (autocompleteMaxRows); cmd00-07 plus
	// the two real slash commands autocompleteApp's App also registers
	// (new, sessions) hit that cap exactly, so the last item's row is the
	// tightest check that nothing at the bottom of the popup got cropped.
	if !strings.Contains(plain, "cmd07") {
		t.Fatalf("the last autocomplete row (cmd07) is missing — the popup got cropped:\n%s", plain)
	}
}

// TestViewportHeightReservesRoomForAutocompletePopup pins the actual fix
// directly: viewportHeight()'s budget has to shrink by exactly the popup's
// rendered height once the popup opens, the same way it already does for
// the permission banner. Checking this arithmetic avoids the end-to-end
// test above having to also fight the chat footer's own independent
// width-based segment dropping to prove nothing lower got cropped.
func TestViewportHeightReservesRoomForAutocompletePopup(t *testing.T) {
	app := autocompleteApp(t, 8)
	before := app.viewportHeight()

	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if !app.autocomplete.visible() {
		t.Fatal("expected the popup to be open")
	}

	popupHeight := app.autocompletePopupHeight()
	if popupHeight == 0 {
		t.Fatal("expected a non-zero popup height once the popup is open")
	}
	if after := app.viewportHeight(); before-after != popupHeight {
		t.Fatalf("viewportHeight shrank by %d after the popup opened (popup is %d rows), want it to shrink by exactly the popup's height", before-after, popupHeight)
	}
}

// TestAutocompleteIsNotADialog is the regression for "the dialog after typing
// / is not looking ok". The original is an inline dropdown anchored to the
// prompt, not the centred modal surface this port first reused — which came
// with a title bar, a search field and a footer action row.
func TestAutocompleteIsNotADialog(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	if app.overlay != nil {
		t.Fatal("completion must not open the modal dialog surface")
	}
	if !app.autocomplete.visible() {
		t.Fatal("the inline popup should be open")
	}

	// The dialog surface would contribute a search placeholder and a title
	// row; neither belongs to the inline popup. ("esc" is not a usable marker
	// here — the ordinary footer carries it too.)
	popup := stripANSI(app.autocompleteView(60))
	for _, unwanted := range []string{"Search", "Commands\n"} {
		if strings.Contains(popup, unwanted) {
			t.Errorf("the popup should not carry dialog chrome (%q):\n%s", unwanted, popup)
		}
	}
}

// TestAutocompleteSitsAboveThePrompt: the popup is anchored to the prompt's
// top edge, so it reads as part of the same stack.
func TestAutocompleteSitsAboveThePrompt(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.view = viewChat
	app.active = &client.Session{ID: "s", Title: "T"}
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	lines := strings.Split(app.View(), "\n")
	popupRow, promptRow := -1, -1
	for i, line := range lines {
		if strings.Contains(line, "/cmd00") {
			popupRow = i
		}
		// The placeholder is gone once "/" is typed, so the prompt is located
		// by its meta row instead.
		if strings.Contains(stripANSI(line), "Build") && promptRow == -1 {
			promptRow = i
		}
	}
	if popupRow == -1 {
		t.Fatal("the popup did not render")
	}
	if promptRow == -1 {
		t.Fatal("the prompt did not render")
	}
	if popupRow >= promptRow {
		t.Errorf("popup is at row %d and the prompt at %d; it must sit above", popupRow, promptRow)
	}
}

// TestAutocompleteMatchesPromptWidth: same left edge and width as the prompt,
// which is what makes it look attached rather than floating.
func TestAutocompleteMatchesPromptWidth(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.view = viewChat
	app.active = &client.Session{ID: "s", Title: "T"}
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	width := app.sessionPromptBoxWidth()
	popup := app.autocompleteView(width)
	prompt := app.promptBox(width)

	popupWidth := lipglossWidthOfFirstLine(popup)
	promptWidth := lipglossWidthOfFirstLine(prompt)
	if popupWidth != promptWidth {
		t.Errorf("popup is %d columns and the prompt %d; they must match", popupWidth, promptWidth)
	}
}

// stripANSI removes SGR sequences so a rendered block can be inspected as
// plain text.
func stripANSI(value string) string { return ansi.Strip(value) }

func lipglossWidthOfFirstLine(block string) int {
	lines := strings.Split(block, "\n")
	widest := 0
	for _, line := range lines {
		if w := len([]rune(stripANSI(line))); w > widest {
			widest = w
		}
	}
	return widest
}

// TestAutocompleteCapsAtTenRows ports Math.min(10, count).
func TestAutocompleteCapsAtTenRows(t *testing.T) {
	app := autocompleteApp(t, 40)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	if got := app.autocomplete.height(); got != autocompleteMaxRows {
		t.Errorf("height = %d, want the cap of %d", got, autocompleteMaxRows)
	}
	rows := strings.Count(app.autocompleteView(60), "\n") + 1
	if rows != autocompleteMaxRows {
		t.Errorf("rendered %d rows, want %d", rows, autocompleteMaxRows)
	}
}

func TestAutocompleteShortListIsNotPadded(t *testing.T) {
	app := autocompleteApp(t, 2)
	app.commands = app.commands[:2]
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	// Only the interface commands plus the two prompt commands; the height
	// must be the item count, not a fixed ten.
	if app.autocomplete.height() > len(app.autocomplete.items) {
		t.Errorf("height %d exceeds %d items", app.autocomplete.height(), len(app.autocomplete.items))
	}
}

// TestTypingFiltersInPlace: the prompt is the query. The original has no
// search field — what you type keeps going into the editor.
func TestTypingFiltersInPlace(t *testing.T) {
	app := autocompleteApp(t, 20)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	before := len(app.autocomplete.items)

	for _, r := range "cmd1" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}

	if app.input.Value() != "/cmd1" {
		t.Errorf("prompt is %q; typing must keep going into the editor", app.input.Value())
	}
	after := len(app.autocomplete.items)
	if after >= before {
		t.Errorf("list went from %d to %d items; typing must narrow it", before, after)
	}
	for _, item := range app.autocomplete.items {
		if !strings.Contains(item.display, "cmd1") {
			t.Errorf("%q does not match the query", item.display)
		}
	}
}

// TestSpaceClosesThePopup: the token has ended, so there is nothing left to
// complete.
func TestSpaceClosesThePopup(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, r := range "cmd00" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	app.Update(tea.KeyPressMsg{Code: ' ', Text: " "})

	if app.autocomplete.visible() {
		t.Error("a space should close the popup")
	}
	if app.input.Value() != "/cmd00 " {
		t.Errorf("prompt is %q, want the space inserted", app.input.Value())
	}
}

func TestArrowKeysNavigate(t *testing.T) {
	app := autocompleteApp(t, 5)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	if app.autocomplete.selected != 0 {
		t.Fatalf("selection starts at %d, want 0", app.autocomplete.selected)
	}

	app.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if app.autocomplete.selected != 1 {
		t.Errorf("down moved to %d, want 1", app.autocomplete.selected)
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	app.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if got := app.autocomplete.selected; got != len(app.autocomplete.items)-1 {
		t.Errorf("up past the top went to %d, want it to wrap to the last row", got)
	}
	// Arrows must not reach the editor while the popup is open.
	if app.input.Value() != "/" {
		t.Errorf("prompt is %q, want arrows consumed by the popup", app.input.Value())
	}
}

// TestEscapeClosesAndClearsTheCommand: hide() clears a half-typed command,
// escape included, so a dismissed popup does not leave "/mod" behind.
func TestEscapeClosesWithoutSelecting(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, r := range "cmd0" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if app.autocomplete.visible() {
		t.Error("escape should close the popup")
	}
	if app.input.Value() != "" {
		t.Errorf("prompt is %q, want the half-typed command cleared", app.input.Value())
	}
}

// TestEnterInsertsTheCommand: choosing a prompt command inserts "/name " so
// arguments can be typed, matching the original.
func TestEnterInsertsTheCommand(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, r := range "cmd01" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if app.autocomplete.visible() {
		t.Error("choosing a row should close the popup")
	}
	if app.input.Value() != "/cmd01 " {
		t.Errorf("prompt is %q, want %q", app.input.Value(), "/cmd01 ")
	}
}

// TestEmptyStateRenders ports the "No matching items" fallback.
func TestEmptyStateRenders(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, r := range "zzzznomatch" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if !app.autocomplete.visible() {
		t.Fatal("the popup should stay open with no matches")
	}
	if !strings.Contains(stripANSI(app.autocompleteView(60)), "No matching items") {
		t.Errorf("expected the empty state:\n%s", app.autocompleteView(60))
	}
}

// TestSelectedRowIsHighlighted: the selected row is filled, the others are not.
func TestSelectedRowIsHighlighted(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	view := app.autocompleteView(60)
	lines := strings.Split(view, "\n")
	if len(lines) < 2 {
		t.Fatal("expected several rows")
	}
	// The first row carries the primary background; the second does not.
	// 48;2;250;178;131 is #fab283, this app's default theme's primary color
	// (see theme.Dark).
	if !strings.Contains(lines[0], "48;2;250;178;131") {
		t.Errorf("the selected row should be filled with the primary color:\n%q", lines[0])
	}
	if strings.Contains(lines[1], "48;2;250;178;131") {
		t.Errorf("an unselected row should not be filled:\n%q", lines[1])
	}
}

// TestCommandTextIsClearedWhenTheCommandRuns is the regression for "after
// closing the dialog, it is not cleaning the / command from the prompt".
//
// The original's hide() deletes the half-typed command before the action
// runs, so a dialog never opens over a prompt still holding "/models".
func TestCommandTextIsClearedWhenTheCommandRuns(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 40

	for _, r := range "/models" {
		drive(t, app, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if !app.autocomplete.visible() {
		t.Fatal("the popup should be open")
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if app.overlay == nil {
		t.Fatal("/models should have opened its dialog")
	}
	if app.input.Value() != "" {
		t.Errorf("prompt still holds %q after running the command", app.input.Value())
	}

	// And it is still empty once the dialog is dismissed.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if app.input.Value() != "" {
		t.Errorf("prompt holds %q after closing the dialog", app.input.Value())
	}
}

// TestPromptCommandLeavesItsOwnText: a command that takes arguments is
// inserted for the user to complete, so clearing must not eat it.
func TestPromptCommandLeavesItsOwnText(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, r := range "cmd01" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if app.input.Value() != "/cmd01 " {
		t.Errorf("prompt is %q, want the command inserted for arguments", app.input.Value())
	}
}

// TestCompletedCommandWithArgumentsSurvives ports the `!text.endsWith(" ")`
// guard: once a command has been chosen and arguments are being typed, the
// text belongs to the user and closing the popup must not discard it.
func TestCompletedCommandWithArgumentsSurvives(t *testing.T) {
	app := autocompleteApp(t, 3)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	for _, r := range "cmd01" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // inserts "/cmd01 "

	// The popup is closed; typing arguments must not be disturbed.
	for _, r := range "staging" {
		app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := app.input.Value(); got != "/cmd01 staging" {
		t.Errorf("prompt is %q, want the arguments preserved", got)
	}

	// Explicitly: hiding with a trailing space present leaves the text alone.
	app.input.SetValue("/cmd01 ")
	app.autocomplete = autocompleteState{kind: autocompleteSlash}
	app.hideAutocomplete()
	if app.input.Value() != "/cmd01 " {
		t.Errorf("a completed command was cleared: %q", app.input.Value())
	}
}

// TestUnmatchedCommandStillReports: with nothing matching, enter falls through
// so the prompt is submitted and an unknown command says so, rather than the
// keypress being swallowed.
func TestUnmatchedCommandStillReports(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 40

	for _, r := range "/nosuchthing" {
		drive(t, app, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !strings.Contains(app.statusMsg, "unknown command") {
		t.Errorf("status is %q, want an unknown-command report", app.statusMsg)
	}
}

// TestMentionPopupDoesNotClear: the clearing rule is specific to "/". An "@"
// mention is part of a sentence the user is writing.
func TestMentionPopupDoesNotClear(t *testing.T) {
	app := autocompleteApp(t, 3)
	typeText(app, "look at ")
	app.Update(tea.KeyPressMsg{Code: '@', Text: "@"})
	if !app.autocomplete.visible() {
		t.Skip("no workspace files to complete")
	}
	app.Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if got := app.input.Value(); !strings.HasPrefix(got, "look at ") {
		t.Errorf("prompt is %q; dismissing an @ popup must not clear the sentence", got)
	}
}
