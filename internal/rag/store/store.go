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
// persistence directories. It is a bookkeeping index, not a second copy of
// the data: those directories remain the sole source of truth for
// embeddings and chunk content.
//
// A second trade-off shapes the on-disk layout: chromem-go's NewPersistentDB
// decodes every collection under whatever directory it's given, with no
// lazy-load option. A database sharing one such directory across every
// project (chromem-go's own examples do this) means opening any one project
// pays to decode all of them — for a database with a handful of large
// projects, seconds of eager work no matter which project a search or index
// call actually names. This package instead gives each project its own
// persistence directory under <dir>/projects/ (see projectDir), opened only
// when collection actually asks for that project, so a single-project
// operation never touches another project's data. Projects (and Vacuum,
// which depends on it) is the deliberate exception: "what does this whole
// database hold" has to open everything to answer, so it does, on demand
// rather than on every Open.
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
	// dir is the store's root directory — the same path Open was given.
	// Each project gets its own chromem-go persistence directory under
	// dir/projects/ (see projectDir); dir itself holds only that directory
	// and manifest.json.
	dir          string
	manifestPath string

	mu sync.Mutex
	// collections caches each project's chromem-go collection once opened.
	// Unlike the single-shared-DB layout this package used to keep, nothing
	// here is populated eagerly at Open: chromem-go's NewPersistentDB decodes
	// every collection under whatever directory it's given, so opening N
	// projects used to mean decoding all N regardless of which one a caller
	// actually wanted. Giving each project its own directory turns that
	// per-DB cost into a per-project one — Open stays cheap, and a project
	// is only ever decoded when collection(id) is actually asked for it.
	collections map[string]*chromem.Collection
	manifest    map[string]manifestEntry
}

// Open opens (creating if needed) the store's root directory at path,
// reloading the chunk manifest already there. No project's chromem-go
// collection is opened, and no old-layout directory is migrated, until that
// specific project is actually asked for — see collection's and
// ensureMigrated's doc comments for why both are lazy rather than done here.
func Open(ctx context.Context, path string) (*Store, error) {
	s := &Store{
		dir:          path,
		manifestPath: filepath.Join(path, "manifest.json"),
		collections:  map[string]*chromem.Collection{},
		manifest:     map[string]manifestEntry{},
	}
	if err := s.loadManifest(); err != nil {
		return nil, err
	}
	return s, nil
}

// projectsRoot is the directory holding one subdirectory per project.
func (s *Store) projectsRoot() string {
	return filepath.Join(s.dir, "projects")
}

// projectDir is projectID's own chromem-go persistence root: a directory
// holding exactly one collection, so opening it only ever decodes this one
// project's data, never any other project sharing this database.
func (s *Store) projectDir(projectID string) string {
	return filepath.Join(s.projectsRoot(), collectionDirName(projectID))
}

// ensureMigrated moves projectID's collection from the old layout (every
// project's collection directly under dir, all decoded by one shared
// chromem-go DB) into its own directory under projectsRoot, if it's still
// there. A plain directory rename is enough: chromem-go's own on-disk format
// for a collection never changes, only which directory tree holds it, so
// this never opens, decodes, or re-encodes a single document.
//
// Deliberately lazy — called from collection and collectionIfExists rather
// than once for every project at Open — for two reasons. First, it keeps
// Open itself cheap regardless of database size, same as the per-project
// layout it's migrating into. Second, and more subtly: a collection with no
// manifest rows at all (a true orphan — see Projects' doc comment) isn't
// discoverable by scanning the manifest, only by Projects opening it once it
// turns up in a directory listing (see discoverOrphanDirs) — so a
// migration pass that only knew about manifest-listed projects would leave
// pre-existing orphans permanently stranded on the old layout the moment
// discoverOrphanDirs started looking in the new one instead. Doing this at
// the point of access instead means whichever code path reaches a project
// first — a search, an index, or Projects/Vacuum discovering an orphan —
// is exactly the one that migrates it.
//
// Idempotent (a project already on the new layout is left untouched) and
// safe under concurrent callers: two callers racing to migrate the same
// never-yet-moved project cannot corrupt anything, because whichever one
// loses the rename finds its source already gone and treats that as
// success, not failure.
func (s *Store) ensureMigrated(projectID string) error {
	newDir := s.projectDir(projectID)
	if _, err := os.Stat(newDir); err == nil {
		return nil // already migrated
	}
	oldDir := filepath.Join(s.dir, collectionDirName(projectID))
	if _, err := os.Stat(oldDir); err != nil {
		return nil // nothing at the old location: a fresh project, or already moved
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return fmt.Errorf("store: migrate project %q: %w", projectID, err)
	}
	// The collection keeps its own name (the project id) unchanged, so its
	// persisted directory name — a hash of that same id — lands on exactly
	// the same value whether computed by chromem-go inside the new
	// per-project root or, as here, reproduced to place it there.
	err := os.Rename(oldDir, filepath.Join(newDir, collectionDirName(projectID)))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("store: migrate project %q: %w", projectID, err)
	}
	return nil
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

// collection returns the project's chromem-go collection, opening (and, if
// this is the first time this project has been written to or searched,
// creating) its own per-project persistence directory. One collection per
// project gives free isolation between projects — no manual filter needed,
// unlike a single shared collection would require — and one *directory* per
// project (rather than one shared directory holding every project's
// collection, as chromem-go's own examples do) is what keeps this cheap:
// NewPersistentDB decodes everything under whatever directory it's given,
// so a directory holding only this project only ever costs decoding this
// project, no matter how many other projects share the database.
func (s *Store) collection(projectID string) (*chromem.Collection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if col, ok := s.collections[projectID]; ok {
		return col, nil
	}
	if err := s.ensureMigrated(projectID); err != nil {
		return nil, err
	}
	db, err := chromem.NewPersistentDB(s.projectDir(projectID), false)
	if err != nil {
		return nil, fmt.Errorf("store: open project %q: %w", projectID, err)
	}
	col, err := db.GetOrCreateCollection(projectID, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("store: create collection: %w", err)
	}
	s.collections[projectID] = col
	return col, nil
}

// collectionIfExists is collection without the "create if missing" half: it
// returns a project's collection when one is already cached or a directory
// for it exists on disk, and (nil, false, nil) otherwise. Projects uses this
// to tell "no collection" apart from "collection", which collection's
// get-or-create semantics can't: calling collection for every candidate id
// would silently manufacture an empty collection (and its directory) for
// every dangling manifest entry Projects looks at.
func (s *Store) collectionIfExists(projectID string) (*chromem.Collection, bool, error) {
	s.mu.Lock()
	if col, ok := s.collections[projectID]; ok {
		s.mu.Unlock()
		return col, true, nil
	}
	s.mu.Unlock()

	if err := s.ensureMigrated(projectID); err != nil {
		return nil, false, err
	}
	if _, err := os.Stat(s.projectDir(projectID)); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("store: stat project %q: %w", projectID, err)
	}
	col, err := s.collection(projectID)
	if err != nil {
		return nil, false, err
	}
	return col, true, nil
}

// discoverOrphanDirs finds project collections that known (manifest-derived)
// ids don't already account for — a true orphan: one with no manifest rows
// at all, which nothing but a directory listing can find, since a
// collection's directory name is a hash of its project id that can't be
// reversed. It looks in both places such a collection could still be
// sitting.
//
// The two layouts need different treatment. Under projectsRoot (the current
// layout), each immediate subdirectory is one project's own root — the same
// shape collection itself opens — holding exactly one collection one level
// further down, so each unrecognized one is opened individually via
// NewPersistentDB to read back its real id. dir itself (the old,
// pre-migration layout) is different: there, a project's collection sits
// directly under dir, so dir is already the right root to hand
// NewPersistentDB in one call — it decodes every collection still there
// (ensureMigrated only ever moves one once something actually asks for it
// by id, and an orphan's id is exactly what nothing asks for until this
// finds it) and its own "projects" subdirectory is silently skipped:
// chromem-go treats a subdirectory with no metadata or documents as a
// user-added directory, not an error.
func (s *Store) discoverOrphanDirs(knownIDs map[string]bool) (map[string]bool, error) {
	discovered := map[string]bool{}

	knownDirs := make(map[string]bool, len(knownIDs))
	for id := range knownIDs {
		knownDirs[collectionDirName(id)] = true
	}
	entries, err := os.ReadDir(s.projectsRoot())
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("store: list project directories: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() || knownDirs[e.Name()] {
			continue
		}
		db, err := chromem.NewPersistentDB(filepath.Join(s.projectsRoot(), e.Name()), false)
		if err != nil {
			return nil, fmt.Errorf("store: open project directory %q: %w", e.Name(), err)
		}
		for name := range db.ListCollections() {
			discovered[name] = true
		}
	}

	oldDB, err := chromem.NewPersistentDB(s.dir, false)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", s.dir, err)
	}
	for name := range oldDB.ListCollections() {
		if !knownIDs[name] {
			discovered[name] = true
		}
	}

	return discovered, nil
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

// HasProject reports whether the store holds any data for projectID —
// either manifest rows or a chromem-go collection. It is a cheaper check
// than Projects for callers that only need to know "does this project have
// an index at all."
func (s *Store) HasProject(ctx context.Context, projectID string) bool {
	s.mu.Lock()
	prefix := projectID + "\x00"
	for key := range s.manifest {
		if strings.HasPrefix(key, prefix) {
			s.mu.Unlock()
			return true
		}
	}
	hasCollection := false
	if col, ok := s.collections[projectID]; ok && col.Count() > 0 {
		hasCollection = true
	}
	s.mu.Unlock()
	return hasCollection
}

// StaleCount is how many stored chunks are outdated: their content hash no
// longer matches what a fresh walk would produce (the file changed, or its
// line count shifted enough to move window boundaries), or their ID no
// longer appears in the walk at all (the file was deleted or excluded).
// Zero means the index is fully up to date.
//
// Computed by comparing the stored manifest hashes against a caller-supplied
// map of current chunk IDs to content hashes, so the store itself never needs
// to walk the filesystem — that is the plugin layer's job, since only it
// knows the project root, include/exclude filters, and gitignore settings.
func (s *Store) StaleCount(ctx context.Context, projectID string, currentHashes map[string]string) (int, error) {
	stored, err := s.Hashes(ctx, projectID)
	if err != nil {
		return 0, err
	}
	stale := 0
	for id, oldHash := range stored {
		newHash, exists := currentHashes[id]
		if !exists || newHash != oldHash {
			stale++
		}
	}
	return stale, nil
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
// deliberately reported per project rather than per chunk: an abandoned
// project still costs disk space and, the moment something asks Projects to
// look, decode time — and "which projects exist and how big are they" is
// the question that actually precedes a cleanup decision.
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
	// Indexed reports whether this project has any indexed chunks at all
	// (manifest rows or collection documents). It is true when either source
	// has data, false when both are empty — the latter means the project
	// has no index yet, or it was cleaned and never re-indexed.
	Indexed bool `json:"indexed"`
	// Stale is how many stored chunks are outdated relative to the current
	// files on disk: their content hash changed, or their chunk ID no longer
	// appears in a fresh walk (file deleted, excluded, or line boundaries
	// shifted). Populated only for the current project, where the plugin
	// can walk the filesystem; left zero for other projects, whose roots
	// may not even be on this machine.
	Stale int `json:"stale"`
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
// "Knows about" is the union of two sources that are supposed to agree — a
// project directory under projectsRoot and the manifest's rows for it —
// precisely so the cases where they don't are visible rather than silently
// unreachable. Unlike every other method in this file, Projects
// deliberately opens every project's collection rather than only the ones a
// caller already asked for: "which projects exist and how big are they" is
// the question that precedes a cleanup decision, so it needs the full
// picture. That cost is paid here, once, when this is actually called —
// not on every Open regardless of whether anyone asked.
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
	s.mu.Unlock()

	ids := map[string]bool{}
	for id := range chunks {
		ids[id] = true
	}
	orphanIDs, err := s.discoverOrphanDirs(ids)
	if err != nil {
		return nil, err
	}
	for id := range orphanIDs {
		ids[id] = true
	}

	out := make([]ProjectStat, 0, len(ids))
	for id := range ids {
		col, hasCollection, err := s.collectionIfExists(id)
		if err != nil {
			return nil, err
		}
		stat := ProjectStat{
			ProjectID:   id,
			Chunks:      chunks[id],
			Paths:       len(paths[id]),
			Indexed:     chunks[id] > 0 || hasCollection,
			Orphan:      hasCollection && chunks[id] == 0,
			Dangling:    !hasCollection && chunks[id] > 0,
			RootMissing: projectRootMissing(id),
		}
		if hasCollection {
			stat.Documents = col.Count()
		}
		size, modTime, err := dirStat(s.projectDir(id))
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
// The two halves must both run even when one has nothing to do: removing a
// directory that isn't there is a no-op, which is exactly the dangling
// case, and a directory with no manifest rows is exactly the orphan case.
// Doing only the half that "looks" necessary is what lets the two sources
// drift apart in the first place.
func (s *Store) DeleteProject(ctx context.Context, projectID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.projectDir(projectID)); err != nil {
		return 0, fmt.Errorf("store: delete project %q: %w", projectID, err)
	}
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
// This removes dir wholesale (every project's collection, migrated or not,
// plus manifest.json) and recreates it, rather than only clearing
// projectsRoot — a store mid-migration, or one still on the pre-per-project
// layout, must come out just as empty as one that already finished. Clearing
// the in-memory copy first is what stops the next Put from writing all
// 30,000 stale manifest rows straight back out.
func (s *Store) Reset(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("store: reset: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("store: reset: %w", err)
	}
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
