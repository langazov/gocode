package tui

import (
	"testing"
)

// Adding must work from the empty list — that is the state a user is in the
// first time they open the manager, and the state where a create action that
// needs a selected row would be dead.
func TestMemoryAddFromEmptyList(t *testing.T) {
	app, state := memoryTestApp(t)
	o := openMemoryDialog(t, app)
	if len(o.items) != 0 {
		t.Fatalf("expected an empty list, got %+v", o.items)
	}

	driveCmd(t, app, app.handleOverlayKey("ctrl+a"))
	if app.overlay == nil || app.overlay.kind != overlayInput {
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
	if app.overlay.title != "Memories" {
		t.Fatalf("overlay = %q, want the manager", app.overlay.title)
	}
	if len(app.overlay.items) != 3 {
		t.Errorf("reopened list has %d rows, want the 3 that now exist", len(app.overlay.items))
	}
}

func TestMemoryEditReopensTheManager(t *testing.T) {
	app, state := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)

	applyCmd(t, app, app.editMemoryAction(o.items[0]))
	if app.overlay == nil || app.overlay.kind != overlayInput {
		t.Fatal("edit did not open the input dialog")
	}
	// The input is prefilled with the current wording, so an edit is an edit
	// rather than a retype.
	if app.overlay.input != o.items[0].label {
		t.Errorf("input = %q, want it prefilled with %q", app.overlay.input, o.items[0].label)
	}

	app.overlay.input = "revised wording"
	applyCmd(t, app, app.handleOverlayKey("enter"))

	if app.overlay == nil || app.overlay.title != "Memories" {
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
	o.filter = "stdlib"
	o.applyFilter()
	if len(o.items) != 1 {
		t.Fatalf("filter matched %d rows, want 1", len(o.items))
	}

	driveCmd(t, app, app.toggleMemoryMutedAction(o.items[0]))

	if app.overlay == nil || app.overlay.title != "Memories" {
		t.Fatal("the manager closed on an in-place action")
	}
	if app.overlay.filter != "stdlib" {
		t.Errorf("filter = %q, want it preserved across the refresh", app.overlay.filter)
	}
}

// The footer must advertise the add action, or it is undiscoverable.
func TestMemoryDialogAdvertisesNew(t *testing.T) {
	app, _ := memoryTestApp(t)
	o := openMemoryDialog(t, app)
	var titles []string
	for _, action := range o.actions {
		titles = append(titles, action.title)
	}
	if len(titles) == 0 || titles[0] != "new" {
		t.Errorf("actions = %v, want \"new\" offered first", titles)
	}
	if !o.actions[0].standalone {
		t.Error("the new action must be standalone so it works with nothing selected")
	}
}

// ctrl+n stays "move down": binding the add action to it would have shadowed
// list movement inside this dialog.
func TestMemoryDialogCtrlNStillMoves(t *testing.T) {
	app, _ := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)
	before := o.selected

	driveCmd(t, app, app.handleOverlayKey("ctrl+n"))

	if app.overlay == nil || app.overlay.kind != overlayList {
		t.Fatal("ctrl+n opened something instead of moving the selection")
	}
	if app.overlay.selected == before {
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
