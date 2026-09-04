package store

import (
	"context"
	"path/filepath"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rag.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func vec(vals ...float32) []float32 { return vals }

func TestPutAndSearchReturnsNearestFirst(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	records := []Record{
		{ID: "a", ProjectID: "p1", Path: "a.go", StartLine: 1, EndLine: 10, Content: "alpha", ContentHash: "h1", Embedding: vec(1, 0, 0)},
		{ID: "b", ProjectID: "p1", Path: "b.go", StartLine: 1, EndLine: 10, Content: "beta", ContentHash: "h2", Embedding: vec(0, 1, 0)},
		{ID: "c", ProjectID: "p1", Path: "c.go", StartLine: 1, EndLine: 10, Content: "gamma", ContentHash: "h3", Embedding: vec(0.9, 0.1, 0)},
	}
	if err := s.Put(ctx, records); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "p1", vec(1, 0, 0), 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}
	if results[0].ID != "a" {
		t.Errorf("nearest result: got %s, want a", results[0].ID)
	}
	if results[1].ID != "c" {
		t.Errorf("second result: got %s, want c", results[1].ID)
	}
	if results[0].Score < results[1].Score {
		t.Errorf("results not sorted by descending score: %v then %v", results[0].Score, results[1].Score)
	}
}

func TestSearchIsolatesProjects(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Put(ctx, []Record{
		{ID: "x", ProjectID: "p1", Path: "x.go", Content: "x", ContentHash: "h", Embedding: vec(1, 0)},
		{ID: "x", ProjectID: "p2", Path: "x.go", Content: "x", ContentHash: "h", Embedding: vec(1, 0)},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "p1", vec(1, 0), 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ProjectID != "p1" {
		t.Fatalf("expected exactly one p1 result, got %+v", results)
	}
}

func TestSearchFiltersByPathPrefix(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Put(ctx, []Record{
		{ID: "a", ProjectID: "p1", Path: "src/a.go", Content: "a", ContentHash: "h", Embedding: vec(1, 0)},
		{ID: "b", ProjectID: "p1", Path: "docs/b.md", Content: "b", ContentHash: "h", Embedding: vec(1, 0)},
	}); err != nil {
		t.Fatal(err)
	}

	results, err := s.Search(ctx, "p1", vec(1, 0), 10, "src/")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "a" {
		t.Fatalf("expected only src/a.go, got %+v", results)
	}
}

func TestPutUpdatesExistingRecord(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Put(ctx, []Record{
		{ID: "a", ProjectID: "p1", Path: "a.go", Content: "old", ContentHash: "h1", Embedding: vec(1, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, []Record{
		{ID: "a", ProjectID: "p1", Path: "a.go", Content: "new", ContentHash: "h2", Embedding: vec(0, 1)},
	}); err != nil {
		t.Fatal(err)
	}

	hashes, err := s.Hashes(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 1 || hashes["a"] != "h2" {
		t.Fatalf("got %v, want {a: h2}", hashes)
	}

	results, err := s.Search(ctx, "p1", vec(0, 1), 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Content != "new" {
		t.Fatalf("expected updated content, got %+v", results)
	}
}

func TestDeletePathRemovesAllChunks(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Put(ctx, []Record{
		{ID: "a1", ProjectID: "p1", Path: "a.go", Content: "1", ContentHash: "h", Embedding: vec(1, 0)},
		{ID: "a2", ProjectID: "p1", Path: "a.go", Content: "2", ContentHash: "h", Embedding: vec(0, 1)},
		{ID: "b1", ProjectID: "p1", Path: "b.go", Content: "3", ContentHash: "h", Embedding: vec(1, 1)},
	}); err != nil {
		t.Fatal(err)
	}

	removed, err := s.DeletePath(ctx, "p1", "a.go")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("got removed=%d, want 2", removed)
	}

	paths, err := s.IndexedPaths(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !paths["b.go"] {
		t.Fatalf("got %v, want only b.go", paths)
	}

	results, err := s.Search(ctx, "p1", vec(1, 0), 10, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Path == "a.go" {
			t.Errorf("a.go should have been removed from the search index, found %+v", r)
		}
	}
}

func TestReopenRebuildsGraphFromDisk(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rag.db")

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Put(ctx, []Record{
		{ID: "a", ProjectID: "p1", Path: "a.go", Content: "alpha", ContentHash: "h", Embedding: vec(1, 0, 0)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	results, err := s2.Search(ctx, "p1", vec(1, 0, 0), 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "a" {
		t.Fatalf("expected the persisted chunk to reload, got %+v", results)
	}
}
