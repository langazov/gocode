package tui

import (
	"github.com/langazov/gocode-go/internal/tui/dialog"
	"testing"
)

// Adding must work from the empty list — that is the state a user is in the
// first time they open the manager, and the state where a create action that
// needs a selected row would be dead.
func TestMemoryAddFromEmptyList(t *testing.T) {
	app, state := memoryTestApp(t)
	o := openMemoryDialog(t, app)
	if len(o.Items()) != 0 {
		t.Fatalf("expected an empty list, got %+v", o.Items())
	}

	driveCmd(t, app, app.handleOverlayKey("ctrl+a"))
	if app.overlay == nil || app.overlay.Kind != dialog.KindInput {
		t.Fatal("ctrl+a on an empty list did not open the input dialog")
	}

	typeInto(t, app, "always run make check")
	applyCmd(t, app, app.handleOverlayKey("enter"))

	saved := state.snapshot()
	if len(saved) != 1 {
		t.Fatalf("got %d memories, want 1: %+v", len(saved), saved)
	}
	if saved[0].Content != "always run make check" {
		t.Errorf("content = %q, want what was typed", saved[0].Content)
	}
	if saved[0].Scope != "prj_1" {
		t.Errorf("scope = %q, want the project scope for a new memory", saved[0].Scope)
	}
}

// The input dialog closes the manager on submit, so the refresh has to put it
// back — otherwise saving drops the user out of the list they were working in.
func TestMemoryAddReopensTheManager(t *testing.T) {
	app, _ := memoryTestApp(t, sampleMemories()...)
	openMemoryDialog(t, app)

	driveCmd(t, app, app.handleOverlayKey("ctrl+a"))
	typeInto(t, app, "a new rule")
	applyCmd(t, app, app.handleOverlayKey("enter"))

	if app.overlay == nil {
		t.Fatal("the manager did not reopen after saving")
	}
	if app.overlay.Title != "Memories" {
		t.Fatalf("overlay = %q, want the manager", app.overlay.Title)
	}
	if len(app.overlay.Items()) != 3 {
		t.Errorf("reopened list has %d rows, want the 3 that now exist", len(app.overlay.Items()))
	}
}

func TestMemoryEditReopensTheManager(t *testing.T) {
	app, state := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)

	applyCmd(t, app, app.editMemoryAction(o.Items()[0]))
	if app.overlay == nil || app.overlay.Kind != dialog.KindInput {
		t.Fatal("edit did not open the input dialog")
	}
	// The input is prefilled with the current wording, so an edit is an edit
	// rather than a retype.
	if app.overlay.InputValue() != o.Items()[0].Label {
		t.Errorf("input = %q, want it prefilled with %q", app.overlay.InputValue(), o.Items()[0].Label)
	}

	app.overlay.SetInputValue("revised wording")
	applyCmd(t, app, app.handleOverlayKey("enter"))

	if app.overlay == nil || app.overlay.Title != "Memories" {
		t.Fatal("the manager did not reopen after editing")
	}
	var found bool
	for _, item := range state.snapshot() {
		if item.Content == "revised wording" {
			found = true
		}
	}
	if !found {
		t.Errorf("the edit was not saved: %+v", state.snapshot())
	}
}

// An in-place action leaves the manager open, so it must not be reopened on
// top of itself or lose the user's filter.
func TestMemoryInPlaceActionKeepsFilter(t *testing.T) {
	app, _ := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)
	o.SetFilter("stdlib")
	if len(o.Items()) != 1 {
		t.Fatalf("filter matched %d rows, want 1", len(o.Items()))
	}

	driveCmd(t, app, app.toggleMemoryMutedAction(o.Items()[0]))

	if app.overlay == nil || app.overlay.Title != "Memories" {
		t.Fatal("the manager closed on an in-place action")
	}
	if app.overlay.Filter() != "stdlib" {
		t.Errorf("filter = %q, want it preserved across the refresh", app.overlay.Filter())
	}
}

// The footer must advertise the add action, or it is undiscoverable.
func TestMemoryDialogAdvertisesNew(t *testing.T) {
	app, _ := memoryTestApp(t)
	o := openMemoryDialog(t, app)
	var titles []string
	for _, action := range o.Actions() {
		titles = append(titles, action.Title)
	}
	if len(titles) == 0 || titles[0] != "new" {
		t.Errorf("actions = %v, want \"new\" offered first", titles)
	}
	if !o.Actions()[0].Standalone {
		t.Error("the new action must be standalone so it works with nothing selected")
	}
}

// ctrl+n stays "move down": binding the add action to it would have shadowed
// list movement inside this dialog.
func TestMemoryDialogCtrlNStillMoves(t *testing.T) {
	app, _ := memoryTestApp(t, sampleMemories()...)
	openMemoryDialog(t, app)
	before := app.overlay.SelectedIndex()

	driveCmd(t, app, app.handleOverlayKey("ctrl+n"))

	if app.overlay == nil || app.overlay.Kind != dialog.KindList {
		t.Fatal("ctrl+n opened something instead of moving the selection")
	}
	if app.overlay.SelectedIndex() == before {
		t.Error("ctrl+n did not move the selection")
	}
}

func typeInto(t *testing.T, app *App, text string) {
	t.Helper()
	for _, r := range text {
		if cmd := app.handleOverlayKey(string(r)); cmd != nil {
			driveCmd(t, app, cmd)
		}
	}
}
