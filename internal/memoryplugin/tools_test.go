package memoryplugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/memory"
	"github.com/langazov/gocode-go/internal/plugin"
	"github.com/langazov/gocode-go/internal/tool"
)

func toolByName(t *testing.T, hooks *plugin.Hooks, name string) plugin.Tool {
	t.Helper()
	for _, item := range hooks.Tools {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("plugin contributed no tool named %q", name)
	return plugin.Tool{}
}

func call(t *testing.T, item plugin.Tool, args map[string]any) plugin.ToolResult {
	t.Helper()
	out, err := item.Execute(context.Background(), args, plugin.ToolContext{SessionID: "ses_1"})
	if err != nil {
		t.Fatalf("%s(%v): %v", item.Name, args, err)
	}
	return out
}

func TestMemoryWriteSaves(t *testing.T) {
	hooks, store, ctx := setup(t)
	out := call(t, toolByName(t, hooks, "memory_write"), map[string]any{
		"content":  "Run make check before pushing",
		"category": "workflow",
	})

	if !strings.Contains(out.Output, "mem_") {
		t.Errorf("output should name the id so the model can revise it later: %q", out.Output)
	}
	saved, err := store.List(ctx, memory.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 1 {
		t.Fatalf("got %d memories, want 1", len(saved))
	}
	if saved[0].Scope != projectID {
		t.Errorf("scope = %q, want the project scope by default", saved[0].Scope)
	}
	if saved[0].Origin != memory.OriginAgent {
		t.Errorf("origin = %q, want %q", saved[0].Origin, memory.OriginAgent)
	}
	if saved[0].SessionID != "ses_1" {
		t.Errorf("sessionID = %q, want the calling session recorded as provenance", saved[0].SessionID)
	}
	if saved[0].Category != "workflow" {
		t.Errorf("category = %q, want %q", saved[0].Category, "workflow")
	}
}

func TestMemoryWriteGlobalScope(t *testing.T) {
	hooks, store, ctx := setup(t)
	call(t, toolByName(t, hooks, "memory_write"), map[string]any{
		"content": "Always use tabs",
		"scope":   "global",
	})
	saved, err := store.List(ctx, memory.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if saved[0].Scope != memory.ScopeGlobal {
		t.Errorf("scope = %q, want %q", saved[0].Scope, memory.ScopeGlobal)
	}
}

func TestMemoryWriteWithIDRevises(t *testing.T) {
	hooks, store, ctx := setup(t)
	existing, err := store.Create(ctx, memory.Memory{Scope: projectID, Content: "Old wording"})
	if err != nil {
		t.Fatal(err)
	}

	call(t, toolByName(t, hooks, "memory_write"), map[string]any{
		"id":      existing.ID,
		"content": "New wording",
	})

	all, err := store.List(ctx, memory.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("revision created a second row: %d memories", len(all))
	}
	if all[0].Content != "New wording" || all[0].ID != existing.ID {
		t.Errorf("got %q at id %q, want %q at id %q", all[0].Content, all[0].ID, "New wording", existing.ID)
	}
}

// A hallucinated id must produce a message the model can act on, not a bare
// "not found" that leaves it guessing.
func TestMemoryWriteUnknownIDExplains(t *testing.T) {
	hooks, _, _ := setup(t)
	_, err := toolByName(t, hooks, "memory_write").Execute(context.Background(),
		map[string]any{"id": "mem_nope", "content": "Something"}, plugin.ToolContext{})
	if err == nil {
		t.Fatal("expected an error for an unknown id")
	}
	if !strings.Contains(err.Error(), "omit the id") {
		t.Errorf("error = %q, want it to say how to recover", err)
	}
}

func TestMemoryWriteRejectsEmptyContent(t *testing.T) {
	hooks, _, _ := setup(t)
	_, err := toolByName(t, hooks, "memory_write").Execute(context.Background(),
		map[string]any{"content": "   "}, plugin.ToolContext{})
	if err == nil {
		t.Error("expected an error for blank content")
	}
}

func TestMemoryWriteRejectsUnknownScope(t *testing.T) {
	hooks, _, _ := setup(t)
	_, err := toolByName(t, hooks, "memory_write").Execute(context.Background(),
		map[string]any{"content": "Something", "scope": "everywhere"}, plugin.ToolContext{})
	if err == nil {
		t.Error("expected an error for an unknown scope")
	}
}

func TestMemoryDelete(t *testing.T) {
	hooks, store, ctx := setup(t)
	existing, err := store.Create(ctx, memory.Memory{Scope: projectID, Content: "Temporary rule"})
	if err != nil {
		t.Fatal(err)
	}

	out := call(t, toolByName(t, hooks, "memory_delete"), map[string]any{"id": existing.ID})
	// The output names what went: in the TUI this is the user's only chance to
	// catch a wrong delete.
	if !strings.Contains(out.Output, "Temporary rule") {
		t.Errorf("output should name the deleted memory: %q", out.Output)
	}
	if _, err := store.Get(ctx, existing.ID); !errors.Is(err, memory.ErrNotFound) {
		t.Errorf("memory survived the delete: %v", err)
	}
}

func TestMemoryDeleteUnknownID(t *testing.T) {
	hooks, _, _ := setup(t)
	_, err := toolByName(t, hooks, "memory_delete").Execute(context.Background(),
		map[string]any{"id": "mem_nope"}, plugin.ToolContext{})
	if err == nil {
		t.Error("expected an error deleting an unknown id")
	}
}

// The tools must survive the bridge into the runtime registry, since that is
// how the session runner reaches them — not through plugin.Tool directly.
func TestToolsBridgeIntoRegistry(t *testing.T) {
	hooks, store, ctx := setup(t)
	registry := tool.NewRegistry()
	plugin.RegisterTools(registry, host(t, hooks), t.TempDir(), t.TempDir())

	for _, name := range []string{"memory_write", "memory_delete"} {
		registered, ok := registry.Get(name)
		if !ok {
			t.Fatalf("registry has no tool %q", name)
		}
		if registered.Description() == "" {
			t.Errorf("%s has no description", name)
		}
		schema := registered.InputSchema()
		if schema["type"] != "object" {
			t.Errorf("%s schema is not an object: %v", name, schema)
		}
	}

	// The permission action for a tool is its name (session.permissionAction),
	// so exercising it through the registry is what proves the whole path.
	write, _ := registry.Get("memory_write")
	sessionAware, ok := write.(tool.SessionAware)
	if !ok {
		t.Fatal("bridged tool should be session-aware")
	}
	if _, err := sessionAware.ExecuteWithContext(ctx,
		map[string]any{"content": "Bridged rule"}, tool.ExecContext{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	all, err := store.List(ctx, memory.ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Content != "Bridged rule" {
		t.Errorf("execution through the registry did not persist: %+v", all)
	}
}
