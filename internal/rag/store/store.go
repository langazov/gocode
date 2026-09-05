// Package store persists RAG chunks and serves nearest-neighbor search.
//
// Vector storage and search are github.com/philippgille/chromem-go: pure Go,
// zero third-party dependencies, and (verified directly, not assumed) the
// only one of three vector libraries evaluated for this project that
// actually survives replace/delete/reopen under test and cross-compiles for
// Windows. github.com/coder/hnsw and github.com/DotNetAge/govector (which
// itself wraps coder/hnsw for its HNSW mode) were both tried first and
// dropped: coder/hnsw panics on a same-key Add (its documented "replace"
// path), corrupts its own graph on Delete (a later Search on the surviving
// nodes segfaults), and its Export/Import machinery unconditionally imports
// github.com/google/renameio, which has no Windows implementation at all —
// so neither library can even compile for GOOS=windows, let alone run
// correctly. chromem-go has none of these problems: its Add is a genuine
// upsert, Delete leaves the collection searchable, and it has no
// platform-specific code.
//
// The trade-off: chromem-go's collections expose no way to enumerate their
// own documents (no ListIDs/GetAll — only Query-by-similarity and
// GetByID-by-known-id). Deciding what needs (re-)embedding on an incremental
// reindex needs exactly that enumeration (every stored chunk ID's content
// hash), so this package keeps a small side manifest — chunk ID and content
// hash only, no vectors or content — as plain JSON alongside chromem-go's own
// persistence directory. It is a bookkeeping index, not a second copy of the
// data: chromem-go's directory remains the sole source of truth for
// embeddings and chunk content.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	chromem "github.com/philippgille/chromem-go"
)

// Record is one stored chunk: its text, its embedding, and enough metadata to
// cite and filter it.
type Record struct {
	ID          string
	ProjectID   string
	Path        string
	StartLine   int
	EndLine     int
	Content     string
	ContentHash string
	Embedding   []float32
	UpdatedAt   int64
}

// Result is a Record ranked by a search.
type Result struct {
	Record
	// Score is cosine similarity in [-1, 1]; higher is more similar.
	Score float32
}

// manifestEntry is the side-index bookkeeping for one chunk: just enough to
// answer "does this chunk still need embedding" and "which paths exist for
// this project" without asking chromem-go to enumerate its own collection.
type manifestEntry struct {
	ProjectID   string `json:"projectId"`
	Path        string `json:"path"`
	ContentHash string `json:"contentHash"`
}

// manifestKey namespaces a chunk ID by project, matching chunk IDs that are
// only unique within one project's own walk.
func manifestKey(projectID, chunkID string) string {
	return projectID + "\x00" + chunkID
}

// Store is safe for concurrent use.
type Store struct {
	db           *chromem.DB
	manifestPath string

	mu          sync.Mutex
	collections map[string]*chromem.Collection
	manifest    map[string]manifestEntry
}

// Open opens (creating if needed) the chromem-go persistence directory at
// path, reloading any collections and the chunk manifest already there.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := chromem.NewPersistentDB(path, false)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}

	s := &Store{
		db:           db,
		manifestPath: filepath.Join(path, "manifest.json"),
		collections:  map[string]*chromem.Collection{},
		manifest:     map[string]manifestEntry{},
	}
	for name, col := range db.ListCollections() {
		s.collections[name] = col
	}
	if err := s.loadManifest(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadManifest() error {
	data, err := os.ReadFile(s.manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("store: read manifest: %w", err)
	}
	if err := json.Unmarshal(data, &s.manifest); err != nil {
		return fmt.Errorf("store: decode manifest: %w", err)
	}
	return nil
}

// saveManifest must be called with s.mu held.
func (s *Store) saveManifest() error {
	data, err := json.Marshal(s.manifest)
	if err != nil {
		return fmt.Errorf("store: encode manifest: %w", err)
	}
	if dir := filepath.Dir(s.manifestPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("store: create dir: %w", err)
		}
	}
	// Write to a temp file and rename, so a crash mid-write never leaves a
	// truncated manifest.json for the next Open to fail on.
	tmp := s.manifestPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("store: write manifest: %w", err)
	}
	if err := os.Rename(tmp, s.manifestPath); err != nil {
		return fmt.Errorf("store: rename manifest: %w", err)
	}
	return nil
}

// Close is a no-op: chromem-go persists synchronously on every write and
// exposes no handle to release. It exists so callers have a symmetric
// Open/Close pair regardless of which storage engine is behind it.
func (s *Store) Close() error {
	return nil
}

// collection returns the project's chromem-go collection, creating it if this
// is the first time this project has been written to or searched. One
// collection per project gives free isolation between projects — no manual
// filter needed, unlike a single shared collection would require.
func (s *Store) collection(projectID string) (*chromem.Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if col, ok := s.collections[projectID]; ok {
		return col, nil
	}
	col, err := s.db.GetOrCreateCollection(projectID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("store: create collection: %w", err)
	}
	s.collections[projectID] = col
	return col, nil
}

// Put inserts or updates records. chromem-go's Add is a genuine upsert
// (verified: replacing an existing ID keeps the collection's count and
// content correct, unlike coder/hnsw's equivalent).
func (s *Store) Put(ctx context.Context, records []Record) error {
	if len(records) == 0 {
		return nil
	}

	byProject := map[string][]Record{}
	for _, r := range records {
		byProject[r.ProjectID] = append(byProject[r.ProjectID], r)
	}

	for projectID, group := range byProject {
		col, err := s.collection(projectID)
		if err != nil {
			return err
		}
		ids := make([]string, len(group))
		embeddings := make([][]float32, len(group))
		metadatas := make([]map[string]string, len(group))
		contents := make([]string, len(group))
		for i, r := range group {
			ids[i] = r.ID
			embeddings[i] = r.Embedding
			metadatas[i] = map[string]string{
				"path":      r.Path,
				"startLine": strconv.Itoa(r.StartLine),
				"endLine":   strconv.Itoa(r.EndLine),
				"updatedAt": strconv.FormatInt(r.UpdatedAt, 10),
			}
			contents[i] = r.Content
		}
		if err := col.Add(ctx, ids, embeddings, metadatas, contents); err != nil {
			return fmt.Errorf("store: add to project %q: %w", projectID, err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range records {
		s.manifest[manifestKey(r.ProjectID, r.ID)] = manifestEntry{
			ProjectID:   r.ProjectID,
			Path:        r.Path,
			ContentHash: r.ContentHash,
		}
	}
	return s.saveManifest()
}

// DeleteIDs removes specific chunks by ID, e.g. ones whose window boundaries
// moved on reindex and are no longer produced by the chunker.
func (s *Store) DeleteIDs(ctx context.Context, projectID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	col, err := s.collection(projectID)
	if err != nil {
		return err
	}
	if err := col.Delete(ctx, nil, nil, ids...); err != nil {
		return fmt.Errorf("store: delete ids: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.manifest, manifestKey(projectID, id))
	}
	return s.saveManifest()
}

// DeletePath removes every chunk indexed for path (a file that was deleted or
// no longer matches the include/exclude filters), returning how many rows
// were removed.
func (s *Store) DeletePath(ctx context.Context, projectID, path string) (int, error) {
	ids := s.idsForPath(projectID, path)
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.DeleteIDs(ctx, projectID, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (s *Store) idsForPath(projectID, path string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	prefix := projectID + "\x00"
	for key, entry := range s.manifest {
		if strings.HasPrefix(key, prefix) && entry.Path == path {
			ids = append(ids, strings.TrimPrefix(key, prefix))
		}
	}
	return ids
}

// Hashes returns every stored chunk's content hash for a project, keyed by
// chunk ID. index.go diffs this against a fresh chunk walk to decide what
// needs (re-)embedding and what no longer exists.
func (s *Store) Hashes(ctx context.Context, projectID string) (map[string]string, error) {
	return s.HashesUnderPath(ctx, projectID, "")
}

// HashesUnderPath is Hashes restricted to chunks whose Path is scope itself
// or nested under it (scope + "/"). An empty scope returns every stored hash
// for the project, same as Hashes. index.go uses this to diff a scoped
// reindex (one subdirectory) only against chunks already stored under that
// subtree, so the rest of the project's chunks are never mistaken for stale.
func (s *Store) HashesUnderPath(ctx context.Context, projectID, scope string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	prefix := projectID + "\x00"
	for key, entry := range s.manifest {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if scope != "" && entry.Path != scope && !strings.HasPrefix(entry.Path, scope+"/") {
			continue
		}
		out[strings.TrimPrefix(key, prefix)] = entry.ContentHash
	}
	return out, nil
}

// IndexedPaths returns the distinct file paths currently stored for a
// project.
func (s *Store) IndexedPaths(ctx context.Context, projectID string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	prefix := projectID + "\x00"
	for key, entry := range s.manifest {
		if strings.HasPrefix(key, prefix) {
			out[entry.Path] = true
		}
	}
	return out, nil
}

// Search returns the k chunks closest to queryVector for a project, most
// similar first, optionally restricted to paths with the given prefix.
//
// chromem-go's metadata filter (where) is exact-match only, so a path prefix
// is applied by over-fetching candidates and filtering in Go — the same
// approach every chromem-go user needs for prefix/range filters, per its own
// docs on `where`.
func (s *Store) Search(ctx context.Context, projectID string, queryVector []float32, k int, pathPrefix string) ([]Result, error) {
	if k <= 0 {
		k = 8
	}
	col, err := s.collection(projectID)
	if err != nil {
		return nil, err
	}
	count := col.Count()
	if count == 0 {
		return nil, nil
	}

	fetch := k
	if pathPrefix != "" {
		fetch = k * 8
		if fetch < 64 {
			fetch = 64
		}
	}
	if fetch > count {
		fetch = count
	}

	matches, err := col.QueryEmbedding(ctx, queryVector, fetch, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("store: search: %w", err)
	}

	results := make([]Result, 0, len(matches))
	for _, m := range matches {
		path := m.Metadata["path"]
		if pathPrefix != "" && !strings.HasPrefix(path, pathPrefix) {
			continue
		}
		startLine, _ := strconv.Atoi(m.Metadata["startLine"])
		endLine, _ := strconv.Atoi(m.Metadata["endLine"])
		updatedAt, _ := strconv.ParseInt(m.Metadata["updatedAt"], 10, 64)
		results = append(results, Result{
			Record: Record{
				ID:        m.ID,
				ProjectID: projectID,
				Path:      path,
				StartLine: startLine,
				EndLine:   endLine,
				Content:   m.Content,
				UpdatedAt: updatedAt,
			},
			Score: m.Similarity,
		})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) > k {
		results = results[:k]
	}
	return results, nil
}
