package store

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

func TestDeleteUnderPathRemovesAllChunksForAFile(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if err := s.Put(ctx, []Record{
		{ID: "a1", ProjectID: "p1", Path: "a.go", Content: "1", ContentHash: "h", Embedding: vec(1, 0)},
		{ID: "a2", ProjectID: "p1", Path: "a.go", Content: "2", ContentHash: "h", Embedding: vec(0, 1)},
		{ID: "b1", ProjectID: "p1", Path: "b.go", Content: "3", ContentHash: "h", Embedding: vec(1, 1)},
	}); err != nil {
		t.Fatal(err)
	}

	removed, err := s.DeleteUnderPath(ctx, "p1", "a.go")
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

// ---- Maintenance ----

// putProject is shorthand for seeding n chunks under one project.
func putProject(t *testing.T, s *Store, projectID string, paths ...string) {
	t.Helper()
	records := make([]Record, len(paths))
	for i, p := range paths {
		records[i] = Record{
			ID: p + "#" + strconv.Itoa(i), ProjectID: projectID, Path: p,
			Content: p, ContentHash: "h", Embedding: vec(1, 0),
		}
	}
	if err := s.Put(context.Background(), records); err != nil {
		t.Fatal(err)
	}
}

func projectByID(t *testing.T, stats []ProjectStat, id string) ProjectStat {
	t.Helper()
	for _, p := range stats {
		if p.ProjectID == id {
			return p
		}
	}
	t.Fatalf("project %q not in %+v", id, stats)
	return ProjectStat{}
}

// TestCollectionDirNameMatchesChromem pins the one piece of chromem-go
// internals this package reproduces rather than calls. collectionDirName
// derives a collection's on-disk directory the way chromem-go's own
// unexported hash2hex does; if that scheme ever changes upstream, every
// project would silently report 0 bytes instead of failing. Comparing
// against a real persistence directory is what makes that loud.
func TestCollectionDirNameMatchesChromem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rag.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putProject(t, s, "some/project/id", "a.go")

	dir := filepath.Join(path, collectionDirName("some/project/id"))
	if _, err := os.Stat(dir); err != nil {
		entries, _ := os.ReadDir(path)
		var got []string
		for _, e := range entries {
			got = append(got, e.Name())
		}
		t.Fatalf("collectionDirName gave %q, which does not exist; directory holds %v", filepath.Base(dir), got)
	}
}

func TestProjectsReportsPerProjectStats(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	putProject(t, s, "p1", "a.go", "b.go", "b.go")
	putProject(t, s, "p2", "c.go")

	stats, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(stats), stats)
	}

	p1 := projectByID(t, stats, "p1")
	if p1.Chunks != 3 {
		t.Errorf("p1 chunks: got %d, want 3", p1.Chunks)
	}
	if p1.Paths != 2 {
		t.Errorf("p1 distinct paths: got %d, want 2", p1.Paths)
	}
	if p1.Documents != 3 {
		t.Errorf("p1 documents: got %d, want 3", p1.Documents)
	}
	if p1.Bytes <= 0 {
		t.Errorf("p1 bytes: got %d, want a positive on-disk size", p1.Bytes)
	}
	if p1.Orphan || p1.Dangling {
		t.Errorf("p1 should be healthy, got orphan=%v dangling=%v", p1.Orphan, p1.Dangling)
	}
	if projectByID(t, stats, "p2").Chunks != 1 {
		t.Errorf("p2 chunks: got %d, want 1", projectByID(t, stats, "p2").Chunks)
	}

	// Largest first, so `rag-plugin list` leads with the project actually
	// worth reclaiming.
	if stats[0].Bytes < stats[1].Bytes {
		t.Errorf("projects not sorted by descending size: %d then %d", stats[0].Bytes, stats[1].Bytes)
	}
}

func TestDeleteProjectRemovesCollectionAndManifest(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	putProject(t, s, "p1", "a.go", "b.go")
	putProject(t, s, "p2", "c.go")

	removed, err := s.DeleteProject(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("got removed=%d, want 2", removed)
	}

	stats, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].ProjectID != "p2" {
		t.Fatalf("after deleting p1, got %+v, want only p2", stats)
	}

	// The collection directory must be gone too, not merely unreferenced —
	// reclaiming the disk is the entire point.
	if _, err := os.Stat(filepath.Join(s.dir, collectionDirName("p1"))); !os.IsNotExist(err) {
		t.Errorf("p1's collection directory survived: stat err = %v", err)
	}

	// And p2 must still be searchable, i.e. deleting one project did not
	// disturb the collection next to it.
	results, err := s.Search(ctx, "p2", vec(1, 0), 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("p2 lost data: got %d results, want 1", len(results))
	}
}

// TestDeleteUnderPathRemovesSubtree covers the partial-cleanup case: one
// directory dropped, its siblings and the prefix-adjacent "internal/ragtest"
// left alone.
func TestDeleteUnderPathRemovesSubtree(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	putProject(t, s, "p1",
		"internal/rag/index.go",
		"internal/rag/store/store.go",
		"internal/ragtest/main.go",
		"internal/lsp/client.go",
	)

	removed, err := s.DeleteUnderPath(ctx, "p1", "internal/rag")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("got removed=%d, want 2 (index.go and store/store.go)", removed)
	}

	paths, err := s.IndexedPaths(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !paths["internal/ragtest/main.go"] || !paths["internal/lsp/client.go"] {
		t.Fatalf("got %v, want ragtest and lsp to survive", paths)
	}
}

// TestDeleteUnderPathNormalizesScope: these prefixes arrive from a shell
// argument or a JSON tool call, so a trailing slash (or a Windows separator)
// must not silently match nothing.
func TestDeleteUnderPathNormalizesScope(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	putProject(t, s, "p1", "internal/rag/index.go", "cmd/main.go")

	removed, err := s.DeleteUnderPath(ctx, "p1", "internal/rag/")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("trailing slash: got removed=%d, want 1", removed)
	}
}

// TestDeleteUnderPathRefusesEmptyScope: an empty prefix must not quietly
// mean "delete the whole project" — that is DeleteProject's job, and the
// caller most likely passed an unset flag.
func TestDeleteUnderPathRefusesEmptyScope(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	putProject(t, s, "p1", "a.go")

	if _, err := s.DeleteUnderPath(ctx, "p1", "  /  "); err == nil {
		t.Fatal("expected an error for an empty scope")
	}
	if paths, _ := s.IndexedPaths(ctx, "p1"); len(paths) != 1 {
		t.Errorf("the refused call still deleted something: %v", paths)
	}
}

func TestResetEmptiesEverything(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rag.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putProject(t, s, "p1", "a.go")
	putProject(t, s, "p2", "b.go")

	if err := s.Reset(ctx); err != nil {
		t.Fatal(err)
	}

	stats, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Fatalf("after reset, got %+v, want no projects", stats)
	}

	// chromem-go's Reset removes the whole persistence directory, manifest
	// and all. Writing an empty one back is what keeps a reset store
	// distinguishable from a never-used one.
	if _, err := os.Stat(s.manifestPath); err != nil {
		t.Errorf("reset left no manifest.json: %v", err)
	}

	// The store must stay usable, and a subsequent write must not resurrect
	// the pre-reset manifest rows from the in-memory map.
	putProject(t, s, "p3", "c.go")
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	stats, err = reopened.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].ProjectID != "p3" {
		t.Fatalf("after reset+write+reopen, got %+v, want only p3", stats)
	}
}

// TestVacuumDropsOrphanAndDanglingProjects builds both kinds of drift the
// way they actually occur — a manifest that lost rows (concurrent writers
// clobbering the whole-file rewrite) and a collection directory that went
// missing — and checks Vacuum reclaims each without touching the healthy
// project between them.
func TestVacuumDropsOrphanAndDanglingProjects(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rag.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	putProject(t, s, "healthy", "a.go")
	putProject(t, s, "orphan", "b.go")
	putProject(t, s, "dangling", "c.go")

	// Orphan: collection on disk, manifest rows gone.
	s.mu.Lock()
	for key := range s.manifest {
		if strings.HasPrefix(key, "orphan\x00") {
			delete(s.manifest, key)
		}
	}
	err = s.saveManifest()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	// Dangling: manifest rows kept, collection directory removed behind the
	// store's back.
	if err := os.RemoveAll(filepath.Join(path, collectionDirName("dangling"))); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	delete(s.collections, "dangling")
	s.mu.Unlock()

	report, err := s.Vacuum(ctx, VacuumOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OrphanCollections) != 1 || report.OrphanCollections[0] != "orphan" {
		t.Errorf("orphans: got %v, want [orphan]", report.OrphanCollections)
	}
	if len(report.DanglingProjects) != 1 || report.DanglingProjects[0] != "dangling" {
		t.Errorf("dangling: got %v, want [dangling]", report.DanglingProjects)
	}
	if len(report.MissingProjects) != 0 {
		t.Errorf("missing: got %v, want none without PruneMissing", report.MissingProjects)
	}
	if report.Empty() {
		t.Error("report claims nothing was found")
	}

	stats, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].ProjectID != "healthy" {
		t.Fatalf("after vacuum, got %+v, want only healthy", stats)
	}
	if _, err := os.Stat(filepath.Join(path, collectionDirName("orphan"))); !os.IsNotExist(err) {
		t.Errorf("the orphan's directory survived: stat err = %v", err)
	}
}

func TestVacuumDryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rag.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putProject(t, s, "orphan", "b.go")
	s.mu.Lock()
	for key := range s.manifest {
		delete(s.manifest, key)
	}
	err = s.saveManifest()
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	report, err := s.Vacuum(ctx, VacuumOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.OrphanCollections) != 1 {
		t.Fatalf("dry run should still report: got %+v", report)
	}
	if !report.DryRun {
		t.Error("report.DryRun should echo back that nothing was removed")
	}
	if report.BytesFreed <= 0 {
		t.Errorf("dry run should still project a saving, got %d bytes", report.BytesFreed)
	}
	if _, err := os.Stat(filepath.Join(path, collectionDirName("orphan"))); err != nil {
		t.Errorf("dry run deleted the collection: %v", err)
	}
}

// TestVacuumPruneMissingOnlyJudgesAbsolutePaths: a project id is whatever
// the caller chose. Only the plugin's default (a worktree path) is a
// filesystem claim; ids like "p1" or the "default" fallback must never be
// stat'd and found wanting.
func TestVacuumPruneMissingOnlyJudgesAbsolutePaths(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	gone := filepath.Join(t.TempDir(), "deleted-worktree")
	putProject(t, s, gone, "a.go")
	putProject(t, s, "p1", "b.go")

	live := t.TempDir()
	putProject(t, s, live, "c.go")

	report, err := s.Vacuum(ctx, VacuumOptions{PruneMissing: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.MissingProjects) != 1 || report.MissingProjects[0] != gone {
		t.Fatalf("missing: got %v, want [%s]", report.MissingProjects, gone)
	}

	stats, err := s.Projects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %+v, want the relative id and the live worktree to survive", stats)
	}
	for _, p := range stats {
		if p.ProjectID == gone {
			t.Errorf("the missing worktree survived: %+v", p)
		}
	}
}

// ---- Staleness ----

func TestStaleCount(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	putProject(t, s, "p1", "a.go", "b.go", "c.go")

	// All three chunks have hash "h" in the store. A walk that returns the
	// same hashes for all three IDs should report zero stale.
	current := map[string]string{
		"a.go#0": "h",
		"b.go#1": "h",
		"c.go#2": "h",
	}
	stale, err := s.StaleCount(ctx, "p1", current)
	if err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Errorf("all hashes match: got %d stale, want 0", stale)
	}

	// Change one chunk's content hash: it should count as stale.
	current["a.go#0"] = "changed"
	stale, err = s.StaleCount(ctx, "p1", current)
	if err != nil {
		t.Fatal(err)
	}
	if stale != 1 {
		t.Errorf("one changed hash: got %d stale, want 1", stale)
	}

	// Drop one chunk from the current walk entirely (file deleted): it
	// should also count as stale.
	delete(current, "b.go#1")
	stale, err = s.StaleCount(ctx, "p1", current)
	if err != nil {
		t.Fatal(err)
	}
	if stale != 2 {
		t.Errorf("one changed + one missing: got %d stale, want 2", stale)
	}

	// An empty current map means "everything is stale" (or nothing is
	// stored). With three stored chunks, all three are stale.
	stale, err = s.StaleCount(ctx, "p1", map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if stale != 3 {
		t.Errorf("empty current map: got %d stale, want 3", stale)
	}

	// A project with no stored chunks has zero stale regardless of the
	// current map.
	stale, err = s.StaleCount(ctx, "no-such-project", current)
	if err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Errorf("unknown project: got %d stale, want 0", stale)
	}
}

func TestHasProject(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)

	if s.HasProject(ctx, "p1") {
		t.Error("before any Put, HasProject should be false")
	}

	putProject(t, s, "p1", "a.go")

	if !s.HasProject(ctx, "p1") {
		t.Error("after Put, HasProject should be true")
	}

	if s.HasProject(ctx, "p2") {
		t.Error("HasProject for a non-existent project should be false")
	}

	if _, err := s.DeleteProject(ctx, "p1"); err != nil {
		t.Fatal(err)
	}

	if s.HasProject(ctx, "p1") {
		t.Error("after DeleteProject, HasProject should be false")
	}
}
