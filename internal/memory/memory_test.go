package memory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/db"
)

func testStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	// A file, not :memory: — the pool hands out separate connections and an
	// in-memory database is private to one of them. See go/AGENTS.md.
	database, err := db.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	// An open SQLite handle blocks t.TempDir cleanup on Windows.
	t.Cleanup(func() { database.Close() })
	return New(database), ctx
}

func create(t *testing.T, s *Store, ctx context.Context, scope, content string) Memory {
	t.Helper()
	item, err := s.Create(ctx, Memory{Scope: scope, Content: content, Origin: OriginUser})
	if err != nil {
		t.Fatalf("create %q: %v", content, err)
	}
	return item
}

func TestCreateAndGet(t *testing.T) {
	s, ctx := testStore(t)
	created := create(t, s, ctx, "prj_1", "Run make check before pushing")

	if !strings.HasPrefix(created.ID, "mem_") {
		t.Errorf("id = %q, want a mem_ prefix", created.ID)
	}
	if created.Origin != OriginUser {
		t.Errorf("origin = %q, want %q", created.Origin, OriginUser)
	}
	if created.TimeCreated == 0 || created.TimeUpdated == 0 {
		t.Error("timestamps not set")
	}

	got, err := s.Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != created.Content {
		t.Errorf("content = %q, want %q", got.Content, created.Content)
	}
}

// The upsert is the reason the unique index exists: a model that re-derives an
// instruction it already saved must refresh that row, keeping the id it has
// already shown the model, rather than accumulating duplicates.
func TestCreateUpsertsOnDuplicateContent(t *testing.T) {
	s, ctx := testStore(t)
	first := create(t, s, ctx, "prj_1", "Prefer table-driven tests")

	second, err := s.Create(ctx, Memory{
		Scope: "prj_1", Content: "Prefer table-driven tests",
		Category: "style", Origin: OriginAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("upsert changed the id: %q -> %q", first.ID, second.ID)
	}
	if second.Category != "style" {
		t.Errorf("category = %q, want the updated value", second.Category)
	}
	if second.TimeCreated != first.TimeCreated {
		t.Error("upsert should preserve the original creation time")
	}

	all, err := s.List(ctx, ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d memories, want 1 after the upsert", len(all))
	}
}

// Whitespace must not smuggle a duplicate past the unique index.
func TestCreateTrimsContent(t *testing.T) {
	s, ctx := testStore(t)
	first := create(t, s, ctx, "prj_1", "Use gofmt")
	second := create(t, s, ctx, "prj_1", "  Use gofmt\n")
	if second.ID != first.ID {
		t.Errorf("untrimmed content created a second row: %q vs %q", first.ID, second.ID)
	}
}

// A re-save is how the agent revives an instruction the user silenced through
// the interface, so the upsert clears `disabled` deliberately.
func TestCreateRevivesDisabled(t *testing.T) {
	s, ctx := testStore(t)
	created := create(t, s, ctx, "prj_1", "Never touch generated files")
	disabled := true
	if _, err := s.Update(ctx, created.ID, Patch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	revived, err := s.Create(ctx, Memory{Scope: "prj_1", Content: "Never touch generated files"})
	if err != nil {
		t.Fatal(err)
	}
	if revived.Disabled {
		t.Error("re-saving a disabled memory should re-enable it")
	}
}

func TestCreateRejectsEmpty(t *testing.T) {
	s, ctx := testStore(t)
	if _, err := s.Create(ctx, Memory{Scope: "prj_1", Content: "   "}); err == nil {
		t.Error("expected an error for blank content")
	}
	if _, err := s.Create(ctx, Memory{Content: "something"}); err == nil {
		t.Error("expected an error for a missing scope")
	}
}

func TestUpdatePartial(t *testing.T) {
	s, ctx := testStore(t)
	created := create(t, s, ctx, "prj_1", "Original")

	pinned := true
	updated, err := s.Update(ctx, created.ID, Patch{Pinned: &pinned})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Pinned {
		t.Error("pinned not applied")
	}
	if updated.Content != "Original" {
		t.Errorf("content = %q, want it left alone by a pin-only patch", updated.Content)
	}

	content := "Revised"
	scope := ScopeGlobal
	updated, err = s.Update(ctx, created.ID, Patch{Content: &content, Scope: &scope})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Content != "Revised" || updated.Scope != ScopeGlobal {
		t.Errorf("got %q in %q, want %q in %q", updated.Content, updated.Scope, "Revised", ScopeGlobal)
	}
	if !updated.Pinned {
		t.Error("a later patch cleared pinned it did not name")
	}
}

func TestUpdateConflictIsReported(t *testing.T) {
	s, ctx := testStore(t)
	create(t, s, ctx, "prj_1", "First")
	second := create(t, s, ctx, "prj_1", "Second")

	content := "First"
	_, err := s.Update(ctx, second.ID, Patch{Content: &content})
	if err == nil {
		t.Fatal("expected a conflict editing one memory into another's content")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want it to explain the collision", err)
	}
}

func TestUpdateAndDeleteReportMissing(t *testing.T) {
	s, ctx := testStore(t)
	content := "whatever"
	if _, err := s.Update(ctx, "mem_missing", Patch{Content: &content}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update error = %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "mem_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete error = %v, want ErrNotFound", err)
	}
	if _, err := s.Get(ctx, "mem_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("get error = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s, ctx := testStore(t)
	created := create(t, s, ctx, "prj_1", "Temporary")
	if err := s.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("memory survived the delete: %v", err)
	}
}

// Active is what the prompt block is built from, so its filtering is the part
// that decides what the model actually sees.
func TestActiveScopesAndFilters(t *testing.T) {
	s, ctx := testStore(t)
	create(t, s, ctx, ScopeGlobal, "Global rule")
	create(t, s, ctx, "prj_1", "Project one rule")
	create(t, s, ctx, "prj_2", "Project two rule")
	silenced := create(t, s, ctx, "prj_1", "Silenced rule")

	disabled := true
	if _, err := s.Update(ctx, silenced.ID, Patch{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}

	active, err := s.Active(ctx, "prj_1")
	if err != nil {
		t.Fatal(err)
	}
	got := contents(active)
	want := map[string]bool{"Global rule": true, "Project one rule": true}
	if len(got) != len(want) {
		t.Fatalf("active = %v, want exactly %v", got, want)
	}
	for _, content := range got {
		if !want[content] {
			t.Errorf("active included %q, which belongs to another scope or is disabled", content)
		}
	}

	// The interface needs the silenced ones back.
	all, err := s.List(ctx, ListInput{Scopes: []string{"prj_1"}, IncludeDisabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("got %d memories with IncludeDisabled, want 2", len(all))
	}
}

// List order is also render order, so what the dialog shows at the top is what
// survives the budget. Pinned first, then most recently updated.
func TestListOrdersPinnedFirst(t *testing.T) {
	s, ctx := testStore(t)
	oldest := create(t, s, ctx, "prj_1", "Oldest")
	create(t, s, ctx, "prj_1", "Middle")
	create(t, s, ctx, "prj_1", "Newest")

	pinned := true
	if _, err := s.Update(ctx, oldest.ID, Patch{Pinned: &pinned}); err != nil {
		t.Fatal(err)
	}
	items, err := s.List(ctx, ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Content != "Oldest" {
		t.Errorf("first = %q, want the pinned memory", items[0].Content)
	}
}

func contents(items []Memory) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Content)
	}
	return out
}
