package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/memory"
)

const testProject = "prj_server"

func newMemoryServer(t *testing.T) (*Server, *memory.Store) {
	t.Helper()
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	store := memory.New(database)
	return &Server{Memory: store, ProjectID: testProject}, store
}

func TestMemoryRoutesAbsentWithoutStore(t *testing.T) {
	server := &Server{}
	if rec := doJSON(t, server, http.MethodGet, "/api/memory", nil); rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/memory = %d without a store, want 404", rec.Code)
	}
}

func TestMemoryCreateListDelete(t *testing.T) {
	server, _ := newMemoryServer(t)

	rec := doJSON(t, server, http.MethodPost, "/api/memory", map[string]any{
		"content": "Run make check before pushing", "category": "workflow",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	var created memory.Memory
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Scope != testProject {
		t.Errorf("scope = %q, want the project scope by default", created.Scope)
	}
	// Anything created through the API is the user's; the agent writes through
	// the plugin's tools instead, and the interface shows the difference.
	if created.Origin != memory.OriginUser {
		t.Errorf("origin = %q, want %q", created.Origin, memory.OriginUser)
	}

	rec = doJSON(t, server, http.MethodGet, "/api/memory", nil)
	var listed []memory.Memory
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("list = %+v, want the created memory", listed)
	}

	rec = doJSON(t, server, http.MethodDelete, "/api/memory/"+created.ID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, http.MethodGet, "/api/memory", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Errorf("list after delete = %+v, want empty", listed)
	}
}

// A client sends the scope word, never a raw project id — that is an absolute
// filesystem path it has no business constructing.
func TestMemoryScopeResolution(t *testing.T) {
	server, _ := newMemoryServer(t)

	rec := doJSON(t, server, http.MethodPost, "/api/memory",
		map[string]any{"content": "Global rule", "scope": "global"})
	var global memory.Memory
	json.Unmarshal(rec.Body.Bytes(), &global)
	if global.Scope != memory.ScopeGlobal {
		t.Errorf("scope = %q, want %q", global.Scope, memory.ScopeGlobal)
	}

	// An unrecognized scope must narrow to the project, not widen to global:
	// widening a memory to every project should take saying so.
	rec = doJSON(t, server, http.MethodPost, "/api/memory",
		map[string]any{"content": "Odd scope", "scope": "somewhere-else"})
	var narrowed memory.Memory
	json.Unmarshal(rec.Body.Bytes(), &narrowed)
	if narrowed.Scope != testProject {
		t.Errorf("scope = %q, want the project scope for an unrecognized value", narrowed.Scope)
	}
}

func TestMemoryListScopeFilter(t *testing.T) {
	server, store := newMemoryServer(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, memory.Memory{Scope: testProject, Content: "Project rule"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, memory.Memory{Scope: memory.ScopeGlobal, Content: "Global rule"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, memory.Memory{Scope: "prj_other", Content: "Other rule"}); err != nil {
		t.Fatal(err)
	}

	cases := map[string][]string{
		"":        {"Project rule", "Global rule"},
		"project": {"Project rule"},
		"global":  {"Global rule"},
	}
	for query, want := range cases {
		path := "/api/memory"
		if query != "" {
			path += "?scope=" + query
		}
		rec := doJSON(t, server, http.MethodGet, path, nil)
		var listed []memory.Memory
		if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
			t.Fatal(err)
		}
		if len(listed) != len(want) {
			t.Errorf("scope=%q returned %d memories, want %d: %+v", query, len(listed), len(want), listed)
			continue
		}
		found := map[string]bool{}
		for _, item := range listed {
			found[item.Content] = true
		}
		for _, content := range want {
			if !found[content] {
				t.Errorf("scope=%q missing %q, got %v", query, content, found)
			}
		}
	}
}

// The management view must show silenced memories — one the user disabled is
// exactly the one they came here to find.
func TestMemoryListIncludesDisabled(t *testing.T) {
	server, store := newMemoryServer(t)
	ctx := context.Background()
	created, err := store.Create(ctx, memory.Memory{Scope: testProject, Content: "Silenced rule"})
	if err != nil {
		t.Fatal(err)
	}
	disabled := true
	if _, err := store.Update(ctx, created.ID, memory.Patch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, server, http.MethodGet, "/api/memory", nil)
	var listed []memory.Memory
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].Disabled {
		t.Errorf("list = %+v, want the disabled memory included", listed)
	}
}

func TestMemoryPatchIsPartial(t *testing.T) {
	server, store := newMemoryServer(t)
	created, err := store.Create(context.Background(),
		memory.Memory{Scope: testProject, Content: "Original", Category: "style"})
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, server, http.MethodPatch, "/api/memory/"+created.ID, map[string]any{"pinned": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", rec.Code, rec.Body.String())
	}
	var updated memory.Memory
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if !updated.Pinned {
		t.Error("pinned not applied")
	}
	if updated.Content != "Original" || updated.Category != "style" {
		t.Errorf("a pin-only patch changed other fields: %+v", updated)
	}

	// Scope arrives as the interface's word here too.
	rec = doJSON(t, server, http.MethodPatch, "/api/memory/"+created.ID, map[string]any{"scope": "global"})
	json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Scope != memory.ScopeGlobal {
		t.Errorf("scope = %q, want %q", updated.Scope, memory.ScopeGlobal)
	}
}

func TestMemoryMissingIDReports404(t *testing.T) {
	server, _ := newMemoryServer(t)
	if rec := doJSON(t, server, http.MethodDelete, "/api/memory/mem_nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE unknown = %d, want 404", rec.Code)
	}
	rec := doJSON(t, server, http.MethodPatch, "/api/memory/mem_nope", map[string]any{"pinned": true})
	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH unknown = %d, want 404", rec.Code)
	}
}

func TestMemoryCreateRejectsEmptyContent(t *testing.T) {
	server, _ := newMemoryServer(t)
	rec := doJSON(t, server, http.MethodPost, "/api/memory", map[string]any{"content": "  "})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST with blank content = %d, want 400", rec.Code)
	}
}

// With no project resolved there is nowhere narrower than global to put a
// memory, and refusing to store it would be worse than widening it.
func TestMemoryFallsBackToGlobalWithoutProject(t *testing.T) {
	server, _ := newMemoryServer(t)
	server.ProjectID = ""

	rec := doJSON(t, server, http.MethodPost, "/api/memory", map[string]any{"content": "Rule"})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	var created memory.Memory
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Scope != memory.ScopeGlobal {
		t.Errorf("scope = %q, want %q with no project resolved", created.Scope, memory.ScopeGlobal)
	}
}
