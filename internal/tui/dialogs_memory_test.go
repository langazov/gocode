package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// memoryServer is a stub of the memory API: enough to exercise the dialog's
// round trips without standing up the real store.
type memoryServer struct {
	mu       sync.Mutex
	memories []client.Memory
	// requests records method+path for asserting what the dialog actually
	// sent, and patches records the decoded PATCH bodies.
	requests []string
	patches  []client.MemoryPatch
}

func newMemoryServer(t *testing.T, initial ...client.Memory) (*memoryServer, string) {
	t.Helper()
	state := &memoryServer{memories: initial}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.requests = append(state.requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		id := strings.TrimPrefix(r.URL.Path, "/api/memory/")
		switch {
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(state.memories)
		case r.Method == http.MethodPost:
			var body struct{ Content, Scope string }
			json.NewDecoder(r.Body).Decode(&body)
			scope := "prj_1"
			if body.Scope == "global" {
				scope = "global"
			}
			created := client.Memory{ID: "mem_new", Content: body.Content, Scope: scope, Origin: "user"}
			state.memories = append(state.memories, created)
			json.NewEncoder(w).Encode(created)
		case r.Method == http.MethodPatch:
			var patch client.MemoryPatch
			json.NewDecoder(r.Body).Decode(&patch)
			state.patches = append(state.patches, patch)
			for i := range state.memories {
				if state.memories[i].ID != id {
					continue
				}
				if patch.Content != nil {
					state.memories[i].Content = *patch.Content
				}
				if patch.Scope != nil {
					state.memories[i].Scope = *patch.Scope
				}
				if patch.Pinned != nil {
					state.memories[i].Pinned = *patch.Pinned
				}
				if patch.Disabled != nil {
					state.memories[i].Disabled = *patch.Disabled
				}
				json.NewEncoder(w).Encode(state.memories[i])
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodDelete:
			kept := state.memories[:0]
			for _, item := range state.memories {
				if item.ID != id {
					kept = append(kept, item)
				}
			}
			state.memories = kept
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}
	}))
	t.Cleanup(server.Close)
	return state, server.URL
}

func (m *memoryServer) snapshot() []client.Memory {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]client.Memory(nil), m.memories...)
}

func memoryTestApp(t *testing.T, initial ...client.Memory) (*App, *memoryServer) {
	t.Helper()
	state, url := newMemoryServer(t, initial...)
	app := newTestApp(t, url)
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	return app, state
}

func sampleMemories() []client.Memory {
	return []client.Memory{
		{ID: "mem_a", Content: "Run make check", Scope: "prj_1", Origin: "user", Category: "workflow"},
		{ID: "mem_b", Content: "Prefer stdlib", Scope: "global", Origin: "agent", Pinned: true},
	}
}

// applyCmd runs a command chain and applies each message, stopping at the
// status message that ends a mutation.
//
// driveCmd would keep going into the toast that follows, and the toast
// schedules a 5-second tea.Tick that the test helper executes *synchronously* —
// five real seconds per assertion, in the slowest package in the tree. The
// toast is not what any of these tests are about.
func applyCmd(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	for depth := 0; cmd != nil && depth < 6; depth++ {
		msg := cmd()
		if msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, inner := range batch {
				applyCmd(t, app, inner)
			}
			return
		}
		next := app.Update(msg)
		if _, done := msg.(statusMsg); done {
			return
		}
		cmd = next
	}
}

func openMemoryDialog(t *testing.T, app *App) *overlay {
	t.Helper()
	driveCmd(t, app, app.memoriesOverlay())
	if app.overlay == nil || app.overlay.kind != overlayList || app.overlay.title != "Memories" {
		t.Fatal("memoriesOverlay did not open the memory list")
	}
	return app.overlay
}

func TestMemoryDialogListsAndGroupsByScope(t *testing.T) {
	app, _ := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)

	if len(o.items) != 2 {
		t.Fatalf("dialog has %d rows, want 2: %+v", len(o.items), o.items)
	}
	byValue := map[string]overlayItem{}
	for _, item := range o.items {
		byValue[item.value] = item
	}
	if got := byValue["mem_a"].category; got != "Project" {
		t.Errorf("mem_a category = %q, want Project", got)
	}
	if got := byValue["mem_b"].category; got != "Global" {
		t.Errorf("mem_b category = %q, want Global", got)
	}
	if byValue["mem_b"].gutter == "" {
		t.Error("a pinned memory should carry a gutter glyph")
	}
	if !strings.Contains(byValue["mem_a"].hint, "workflow") {
		t.Errorf("hint = %q, want it to mention the category", byValue["mem_a"].hint)
	}
}

// A muted memory stays in the manager — that is the whole point of muting
// rather than deleting — and must say so.
func TestMemoryDialogShowsMuted(t *testing.T) {
	app, _ := memoryTestApp(t, client.Memory{
		ID: "mem_a", Content: "Silenced", Scope: "prj_1", Disabled: true,
	})
	o := openMemoryDialog(t, app)
	if !strings.Contains(o.items[0].hint, "muted") {
		t.Errorf("hint = %q, want it to mark the memory muted", o.items[0].hint)
	}
}

func TestMemoryDialogEmptyState(t *testing.T) {
	app, _ := memoryTestApp(t)
	o := openMemoryDialog(t, app)
	if len(o.items) != 0 {
		t.Fatalf("want no rows, got %+v", o.items)
	}
	if !strings.Contains(o.emptyBody, "/memory") {
		t.Errorf("empty state = %q, want it to say how to save one", o.emptyBody)
	}
}

// Delete arms on the first press and commits on the second. Memories are
// permanent, so one stray keystroke must not end one.
func TestMemoryDeleteRequiresConfirmation(t *testing.T) {
	app, state := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)
	target := o.items[0]

	if cmd := app.deleteMemoryAction(target); cmd != nil {
		t.Error("the first press should arm, not delete")
	}
	if app.overlay.armValue != target.value {
		t.Errorf("armValue = %q, want %q", app.overlay.armValue, target.value)
	}
	if len(state.snapshot()) != 2 {
		t.Fatal("the armed press already deleted something")
	}

	applyCmd(t, app, app.deleteMemoryAction(target))
	remaining := state.snapshot()
	if len(remaining) != 1 {
		t.Fatalf("after confirming, %d memories remain, want 1", len(remaining))
	}
	if remaining[0].ID == target.value {
		t.Error("the wrong memory was deleted")
	}
}

func TestMemoryToggleScope(t *testing.T) {
	app, state := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)

	var project overlayItem
	for _, item := range o.items {
		if item.value == "mem_a" {
			project = item
		}
	}
	applyCmd(t, app, app.toggleMemoryScopeAction(project))

	for _, item := range state.snapshot() {
		if item.ID == "mem_a" && item.Scope != "global" {
			t.Errorf("scope = %q, want it toggled to global", item.Scope)
		}
	}
}

func TestMemoryToggleMuted(t *testing.T) {
	app, state := memoryTestApp(t, sampleMemories()...)
	o := openMemoryDialog(t, app)
	applyCmd(t, app, app.toggleMemoryMutedAction(o.items[0]))

	state.mu.Lock()
	patches := append([]client.MemoryPatch(nil), state.patches...)
	state.mu.Unlock()
	if len(patches) != 1 || patches[0].Disabled == nil || !*patches[0].Disabled {
		t.Fatalf("patches = %+v, want a single disabled:true patch", patches)
	}
	// The patch must not carry fields the user did not touch.
	if patches[0].Content != nil || patches[0].Scope != nil {
		t.Errorf("mute patch changed unrelated fields: %+v", patches[0])
	}
}

// "/memory <text>" saves without opening the dialog: the common case is
// recording something the user just said.
func TestSlashMemoryWithArgumentsQuickAdds(t *testing.T) {
	app, state := memoryTestApp(t)
	applyCmd(t, app, app.runSlashCommand("/memory always run make check"))

	saved := state.snapshot()
	if len(saved) != 1 {
		t.Fatalf("got %d memories, want 1: %+v", len(saved), saved)
	}
	if saved[0].Content != "always run make check" {
		t.Errorf("content = %q, want the arguments verbatim", saved[0].Content)
	}
	if app.overlay != nil {
		t.Error("a quick add should not open the dialog")
	}
}

func TestSlashMemoryWithoutArgumentsOpensDialog(t *testing.T) {
	app, _ := memoryTestApp(t, sampleMemories()...)
	applyCmd(t, app, app.runSlashCommand("/memory"))
	if app.overlay == nil || app.overlay.title != "Memories" {
		t.Fatal("/memory with no arguments should open the manager")
	}
}

// Every interface command that predates argAction must ignore arguments
// exactly as it did before, rather than newly refusing or misrouting them.
func TestSlashArgumentsIgnoredByArgumentlessCommands(t *testing.T) {
	app, _ := memoryTestApp(t)
	applyCmd(t, app, app.runSlashCommand("/help some stray arguments"))
	if app.overlay == nil || app.overlay.kind != overlayHelp {
		t.Fatal("/help with arguments should still open help")
	}
}

// A failed request must report rather than leave the dialog silently empty.
func TestMemoryDialogReportsLoadFailure(t *testing.T) {
	app := newTestApp(t, "http://127.0.0.1:1")
	driveCmd(t, app, app.memoriesOverlay())
	if app.memoryListErr == "" {
		t.Fatal("a failed load should be recorded")
	}
	app.openMemoryDialog(nil)
	if app.overlay.emptyTitle != "Could not load memories" {
		t.Errorf("empty title = %q, want the error state", app.overlay.emptyTitle)
	}
}
