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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	db *chromem.DB
	// dir is chromem-go's persistence directory — the same path Open was
	// given. Kept because chromem-go exposes no way to ask a collection
	// where it lives on disk, and reporting/reclaiming per-project disk
	// usage needs exactly that (see collectionDirName).
	dir          string
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
		dir:          path,
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

// DeleteUnderPath removes every chunk indexed at scope or nested under it,
// returning how many were removed. It is the partial-cleanup counterpart to
// DeleteProject: dropping one subtree (a directory that moved, a package
// that was deleted, a vendored tree that should never have been indexed)
// without re-walking or re-embedding anything.
//
// scope matches exactly what HashesUnderPath's scope matches — the file
// itself, or anything below it — so a scope naming a single file removes
// just that file's chunks. An empty scope is refused rather than silently
// meaning "everything": that is DeleteProject's job, and a caller that
// arrives here with an accidentally-empty prefix means to delete far less
// than the whole project.
func (s *Store) DeleteUnderPath(ctx context.Context, projectID, scope string) (int, error) {
	scope = normalizeScope(scope)
	if scope == "" {
		return 0, fmt.Errorf("store: delete under path: scope is required (use DeleteProject to remove a whole project)")
	}
	ids := s.idsUnderPath(projectID, scope)
	if len(ids) == 0 {
		return 0, nil
	}
	if err := s.DeleteIDs(ctx, projectID, ids); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (s *Store) idsUnderPath(projectID, scope string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	prefix := projectID + "\x00"
	for key, entry := range s.manifest {
		if strings.HasPrefix(key, prefix) && underScope(entry.Path, scope) {
			ids = append(ids, strings.TrimPrefix(key, prefix))
		}
	}
	return ids
}

// normalizeScope puts a caller-supplied path prefix into the same shape the
// manifest stores paths in: slash-separated, no leading or trailing slash.
// Callers pass these from a shell or a JSON tool argument, where
// "internal/rag/" and `internal\rag` are both things a person reasonably
// types. Surrounding whitespace goes first, so that a prefix of only spaces
// and slashes normalizes to empty and is refused rather than matching the
// whole project.
func normalizeScope(scope string) string {
	return strings.Trim(strings.TrimSpace(filepath.ToSlash(scope)), "/")
}

// underScope reports whether a stored chunk path falls within scope: the
// path itself, or anything nested below it. An empty scope matches
// everything. Shared by HashesUnderPath and DeleteUnderPath so a scoped
// reindex and a scoped delete can never disagree about what "under this
// directory" means — the prefix check needs the trailing slash, or scope
// "internal/rag" would also swallow "internal/ragtest".
func underScope(path, scope string) bool {
	if scope == "" {
		return true
	}
	return path == scope || strings.HasPrefix(path, scope+"/")
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
		if !underScope(entry.Path, scope) {
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

// ---- Maintenance ----
//
// Everything above is driven by indexing: chunks arrive from a walk and
// leave when that walk stops producing them. That covers a project being
// re-indexed and nothing else, which leaves three kinds of data no amount
// of re-indexing can reach: a project whose directory is gone, a collection
// whose manifest rows were lost, and manifest rows whose collection was
// lost. The calls below exist for those — inspection and removal that never
// embeds anything, so they need no provider, no API key, and no network.

// ProjectStat describes one project's footprint in the store. It is
// deliberately reported per project rather than per chunk: chromem-go opens
// every collection eagerly on Open, so a stale project costs boot time and
// memory for every other project too, and "which projects exist and how big
// are they" is the question that actually precedes a cleanup decision.
type ProjectStat struct {
	ProjectID string `json:"projectId"`
	// Chunks is how many chunks the manifest records for this project.
	Chunks int `json:"chunks"`
	// Paths is how many distinct files those chunks came from.
	Paths int `json:"paths"`
	// Documents is chromem-go's own count for the collection. It normally
	// equals Chunks; a disagreement is the symptom Vacuum looks for.
	Documents int `json:"documents"`
	// Bytes is the collection directory's size on disk.
	Bytes int64 `json:"bytes"`
	// ModTime is when that directory was last written.
	ModTime time.Time `json:"modTime"`
	// Orphan means chromem-go holds a collection for this project but the
	// manifest has no rows for it. Nothing can reach these chunks: the
	// incremental diff reads the manifest, so an orphan is neither
	// re-embedded nor pruned, it just occupies disk and boot time forever.
	Orphan bool `json:"orphan,omitempty"`
	// Dangling is the mirror image: manifest rows whose collection is gone,
	// so the diff believes chunks are stored that cannot be searched.
	Dangling bool `json:"dangling,omitempty"`
	// RootMissing means the ProjectID names an absolute path that no longer
	// exists. Unlike Orphan and Dangling this is not corruption — the data
	// is perfectly consistent, it just describes a directory that is gone —
	// so it is reported for a human to judge and only acted on under
	// VacuumOptions.PruneMissing.
	RootMissing bool `json:"rootMissing,omitempty"`
}

// Projects reports every project the store knows about, largest first.
//
// "Knows about" is the union of two sources that are supposed to agree —
// chromem-go's collections and the manifest's rows — precisely so the cases
// where they don't are visible rather than silently unreachable.
func (s *Store) Projects(ctx context.Context) ([]ProjectStat, error) {
	s.mu.Lock()
	chunks := map[string]int{}
	paths := map[string]map[string]bool{}
	for key, entry := range s.manifest {
		id := entry.ProjectID
		if id == "" {
			// Pre-dates the ProjectID field, or a hand-edited manifest:
			// recover the id from the key rather than dropping the row.
			id, _, _ = strings.Cut(key, "\x00")
		}
		chunks[id]++
		if paths[id] == nil {
			paths[id] = map[string]bool{}
		}
		paths[id][entry.Path] = true
	}
	collections := make(map[string]*chromem.Collection, len(s.collections))
	for name, col := range s.collections {
		collections[name] = col
	}
	dir := s.dir
	s.mu.Unlock()

	ids := map[string]bool{}
	for id := range chunks {
		ids[id] = true
	}
	for id := range collections {
		ids[id] = true
	}

	out := make([]ProjectStat, 0, len(ids))
	for id := range ids {
		col, hasCollection := collections[id]
		stat := ProjectStat{
			ProjectID:   id,
			Chunks:      chunks[id],
			Paths:       len(paths[id]),
			Orphan:      hasCollection && chunks[id] == 0,
			Dangling:    !hasCollection && chunks[id] > 0,
			RootMissing: projectRootMissing(id),
		}
		if hasCollection {
			stat.Documents = col.Count()
		}
		size, modTime, err := dirStat(filepath.Join(dir, collectionDirName(id)))
		if err != nil {
			return nil, fmt.Errorf("store: stat project %q: %w", id, err)
		}
		stat.Bytes, stat.ModTime = size, modTime
		out = append(out, stat)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].ProjectID < out[j].ProjectID
	})
	return out, nil
}

// DeleteProject removes a project's collection and every manifest row for
// it, returning how many chunks were dropped.
//
// The two halves must both run even when one has nothing to do: chromem-go's
// DeleteCollection is a no-op for a name it doesn't hold, which is exactly
// the dangling case, and a collection with no manifest rows is exactly the
// orphan case. Doing only the half that "looks" necessary is what lets the
// two sources drift apart in the first place.
func (s *Store) DeleteProject(ctx context.Context, projectID string) (int, error) {
	if err := s.db.DeleteCollection(projectID); err != nil {
		return 0, fmt.Errorf("store: delete project %q: %w", projectID, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.collections, projectID)
	prefix := projectID + "\x00"
	removed := 0
	for key := range s.manifest {
		if strings.HasPrefix(key, prefix) {
			delete(s.manifest, key)
			removed++
		}
	}
	if err := s.saveManifest(); err != nil {
		return 0, err
	}
	return removed, nil
}

// Reset empties the store: every project, every chunk, for every directory
// sharing this database.
//
// chromem-go's own Reset removes and recreates the whole persistence
// directory — which contains manifest.json, since that sidecar lives
// alongside the collections. So the manifest is destroyed on disk here
// whether or not this code says so; clearing the in-memory copy first is
// what stops the next Put from writing all 30,000 stale rows straight back
// out.
func (s *Store) Reset(ctx context.Context) error {
	if err := s.db.Reset(); err != nil {
		return fmt.Errorf("store: reset: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collections = map[string]*chromem.Collection{}
	s.manifest = map[string]manifestEntry{}
	// Recreate the file rather than leaving it absent. Both states load
	// identically (loadManifest treats a missing file as empty), but an
	// empty manifest.json is the difference between "reset ran" and "this
	// database was never written to."
	return s.saveManifest()
}

// VacuumOptions configures a Vacuum pass.
type VacuumOptions struct {
	// DryRun reports what would be removed and removes nothing.
	DryRun bool
	// PruneMissing additionally drops projects whose ProjectID names a
	// directory that no longer exists. Off by default: it is the only part
	// of Vacuum that deletes data which is still perfectly consistent, and
	// a project root can be absent for entirely temporary reasons — an
	// unmounted volume, a detached external disk, a worktree that will be
	// checked out again tomorrow.
	PruneMissing bool
}

// VacuumReport is what one Vacuum pass found (and, unless DryRun, removed).
type VacuumReport struct {
	// OrphanCollections had a collection but no manifest rows.
	OrphanCollections []string `json:"orphanCollections,omitempty"`
	// DanglingProjects had manifest rows but no collection.
	DanglingProjects []string `json:"danglingProjects,omitempty"`
	// MissingProjects were consistent, but their root directory is gone.
	// Only ever populated when PruneMissing is set.
	MissingProjects []string `json:"missingProjects,omitempty"`
	ChunksRemoved   int      `json:"chunksRemoved"`
	BytesFreed      int64    `json:"bytesFreed"`
	// DryRun echoes back whether anything was actually removed, so a report
	// is never ambiguous about that on its own.
	DryRun bool `json:"dryRun,omitempty"`
}

// Empty reports whether the pass found nothing to do.
func (r VacuumReport) Empty() bool {
	return len(r.OrphanCollections) == 0 && len(r.DanglingProjects) == 0 && len(r.MissingProjects) == 0
}

// Vacuum reconciles the store against itself, dropping data no re-index can
// ever reach: collections with no manifest rows, and manifest rows with no
// collection. With PruneMissing it also drops projects whose root directory
// is gone.
func (s *Store) Vacuum(ctx context.Context, opts VacuumOptions) (VacuumReport, error) {
	projects, err := s.Projects(ctx)
	if err != nil {
		return VacuumReport{}, err
	}

	report := VacuumReport{DryRun: opts.DryRun}
	var doomed []ProjectStat
	for _, p := range projects {
		switch {
		case p.Orphan:
			report.OrphanCollections = append(report.OrphanCollections, p.ProjectID)
		case p.Dangling:
			report.DanglingProjects = append(report.DanglingProjects, p.ProjectID)
		case opts.PruneMissing && p.RootMissing:
			report.MissingProjects = append(report.MissingProjects, p.ProjectID)
		default:
			continue
		}
		doomed = append(doomed, p)
	}

	for _, p := range doomed {
		report.BytesFreed += p.Bytes
		if opts.DryRun {
			report.ChunksRemoved += p.Chunks
			continue
		}
		removed, err := s.DeleteProject(ctx, p.ProjectID)
		if err != nil {
			return report, err
		}
		report.ChunksRemoved += removed
	}
	return report, nil
}

// projectRootMissing reports whether a project id names a directory that is
// gone. Only absolute paths are judged: a project id is whatever the caller
// chose (the plugin uses the worktree path, but `rag-plugin index -project
// p1` and the fallback id "default" are not paths at all), and a relative
// or symbolic id must never be read as a filesystem claim that happens to
// miss.
func projectRootMissing(projectID string) bool {
	if !filepath.IsAbs(projectID) {
		return false
	}
	_, err := os.Stat(projectID)
	return os.IsNotExist(err)
}

// collectionDirName reproduces chromem-go's own collection-directory naming
// (sha256 of the name, first 4 bytes, hex). It is unexported there and there
// is no accessor for a collection's path, so sizing a project on disk means
// deriving the name the same way. Pinned by a test against a real
// persistence directory, so a change upstream fails loudly rather than
// silently reporting every project as 0 bytes.
func collectionDirName(projectID string) string {
	sum := sha256.Sum256([]byte(projectID))
	return hex.EncodeToString(sum[:4])
}

// dirStat totals a directory's file sizes and reports its mtime. A missing
// directory is not an error — that is the dangling case, and it reports as
// zero-sized, which is exactly right.
func dirStat(dir string) (int64, time.Time, error) {
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, time.Time{}, nil
		}
		return 0, time.Time{}, err
	}
	var total int64
	err = filepath.WalkDir(dir, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		fi, err := entry.Info()
		if err != nil {
			// The file vanished between the walk and the stat — another
			// process writing the same database. Skip it rather than
			// failing a read-only size report over a race.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		total += fi.Size()
		return nil
	})
	if err != nil {
		return 0, time.Time{}, err
	}
	return total, info.ModTime(), nil
}
