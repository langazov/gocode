// Command rag-plugin is a gocode process plugin (see examples/plugin-echo
// for the protocol) that indexes a project's files for semantic search. It
// exposes rag_index and rag_search for that, plus rag_status, rag_clean and
// rag_vacuum for keeping the shared index database from accumulating data
// nothing can reach.
//
// It speaks newline-delimited JSON-RPC 2.0 on stdin/stdout when launched by
// the host, exactly like examples/plugin-echo. It can also run directly from
// the shell — `rag-plugin index ...`, which exists because the host bounds a
// tool call it makes with no deadline of its own to 30s
// (internal/plugin/process.go's DefaultCallTimeout), too short for a large
// repo's first index. The manifest (gocode-plugin.json) declares
// callTimeoutSeconds: 300 to raise that to 5 minutes for this plugin, but a
// repo whose first index exceeds even that can use the CLI directly. The CLI
// also covers `list`/`clean`/`vacuum`, where a person can type the
// irreversible ones. Both paths share the same runtime construction and
// the same internal/rag orchestration.
//
// The runtime is built in two tiers, and which tier a command or tool needs
// is a real distinction rather than an optimization: indexing and searching
// need an embeddings provider, while inspecting and deleting stored chunks
// must keep working without one.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/langazov/gocode-go/internal/lsp"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/provider"
	"github.com/langazov/gocode-go/internal/rag"
	"github.com/langazov/gocode-go/internal/rag/chunk"
	"github.com/langazov/gocode-go/internal/rag/embed"
	"github.com/langazov/gocode-go/internal/rag/store"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "index":
			if err := runCLIIndex(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "rag-plugin index:", err)
				os.Exit(1)
			}
			return
		case "search":
			if err := runCLISearch(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "rag-plugin search:", err)
				os.Exit(1)
			}
			return
		case "scan":
			if err := runCLIScan(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "rag-plugin scan:", err)
				os.Exit(1)
			}
			return
		case "list":
			if err := runCLIList(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "rag-plugin list:", err)
				os.Exit(1)
			}
			return
		case "clean":
			if err := runCLIClean(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "rag-plugin clean:", err)
				os.Exit(1)
			}
			return
		case "vacuum":
			if err := runCLIVacuum(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "rag-plugin vacuum:", err)
				os.Exit(1)
			}
			return
		}
	}
	runPlugin()
}

// runtimeOptions is what both the plugin handshake and the CLI mode resolve
// into before building a runtime.
type runtimeOptions struct {
	Directory string
	Worktree  string
	ProjectID string

	DBPath            string
	EmbeddingProvider string
	EmbeddingModel    string
	EmbeddingBaseURL  string
	Include           []string
	Exclude           []string
	ChunkLines        int
	ChunkOverlap      int
	TopK              int
	DisableGitignore  bool
}

// defaultExclude keeps the common dependency/output directories out of an
// index by default; .git is already skipped unconditionally by chunk.Walk.
var defaultExclude = []string{"**/node_modules/**", "**/vendor/**", "**/dist/**", "**/.venv/**"}

// defaultIndexTimeoutSeconds is the default per-call timeout for rag_index,
// applied as a context deadline inside the plugin. The host's own
// CallTimeout (300s from the manifest) bounds how long it waits for the
// JSON-RPC reply; this bounds the actual indexing work. They match so the
// plugin gives up just before the host would, rather than the host timing
// out first and leaving the plugin grinding.
const defaultIndexTimeoutSeconds = 300

// runtime is the live set of services one plugin process (or one CLI
// invocation) uses. There is exactly one per process, so it is a package
// value rather than threaded through every call — the same shape
// examples/plugin-echo uses for its "banner" option.
type runtime struct {
	store    *store.Store
	indexer  *rag.Indexer
	searcher *rag.Searcher
	lsp      *lsp.Service
	opts     runtimeOptions
}

// close releases every resource the runtime opened. Every exit path (CLI
// mode's defer, and the plugin loop's shutdown/EOF handlers) must call this;
// skipping the lsp.Shutdown half would leak a spawned language server
// process past the rag-plugin process's own lifetime. The nil check is not
// defensive padding: the maintenance commands and tools stop at the store
// tier and never build an LSP service at all.
func (r *runtime) close() {
	if r.lsp != nil {
		r.lsp.Shutdown()
	}
	r.store.Close()
}

// rt is the process's single runtime, and pending holds the options the
// handshake resolved it from. Both are guarded by rtMu: the plugin loop is
// single-goroutine today, but the lazy build below is the kind of thing that
// silently breaks the day a second reader appears.
var (
	rtMu    sync.Mutex
	rt      *runtime
	pending *runtimeOptions
)

// ensureStore opens the store on first use, from the options the handshake
// stashed, and stops there — no embeddings provider, no LSP service.
//
// It is deliberately not built during `initialize`. The host blocks boot on
// that handshake (internal/plugin/process.go's Spawn), and store.Open eagerly
// gob-decodes every collection in the vector DB into memory — several seconds
// for a large index, paid by every `gocode tui`/`serve` start whether or not
// the session ever searches anything. Deferring it to the first tool call
// moves that cost onto the caller that actually wants it.
//
// This is the tier the maintenance tools (rag_status, rag_clean, rag_vacuum)
// run on. They inspect and delete stored chunks and never embed anything, so
// making them resolve an embeddings provider would fail them on exactly the
// setup most likely to need cleaning up: a project whose credentials or
// provider config have since gone away.
func ensureStore() (*runtime, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	if rt != nil {
		return rt, nil
	}
	if pending == nil {
		return nil, fmt.Errorf("rag-plugin: not initialized")
	}
	opts := resolveDefaults(*pending)
	db, err := store.Open(context.Background(), opts.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	rt = &runtime{store: db, opts: opts}
	return rt, nil
}

// ensureRuntime is ensureStore plus the embedding tier: the provider client
// and the LSP service, and the indexer/searcher built on them. Only
// rag_index and rag_search need it.
//
// A failed embedding build is not cached: a provider that was unreachable at
// the first call may well answer the second. The store half stays built
// either way — it cost seconds to decode and nothing about it failed.
func ensureRuntime() (*runtime, error) {
	r, err := ensureStore()
	if err != nil {
		return nil, err
	}
	rtMu.Lock()
	defer rtMu.Unlock()
	if r.indexer != nil {
		return r, nil
	}
	if err := r.buildEmbedding(context.Background()); err != nil {
		return nil, err
	}
	return r, nil
}

// closeRuntime releases the runtime if one was ever built. Safe to call when
// the handshake happened but no tool call ever did.
func closeRuntime() {
	rtMu.Lock()
	defer rtMu.Unlock()
	if rt != nil {
		rt.close()
		rt = nil
	}
}

// resolveDefaults fills in the two options that have to be derived rather
// than configured: which project this is, and where the database lives. It
// is split out of buildRuntime because every entry point needs it —
// including the maintenance commands, which need to know exactly which
// project id and database path the indexing path would have used, or they
// would inspect and clean the wrong thing.
func resolveDefaults(opts runtimeOptions) runtimeOptions {
	if opts.ProjectID == "" {
		// No project-identity subsystem exists outside internal/session
		// (project.ts's git-remote-hash scheme), and pulling that in would
		// mean depending on more of the main binary than this plugin needs.
		// The worktree path is a stable, good-enough per-project key: it is
		// this plugin's whole notion of "which project," and it is namespaced
		// again by directory anyway (see rag_chunks' path column).
		switch {
		case opts.Worktree != "":
			opts.ProjectID = opts.Worktree
		case opts.Directory != "":
			opts.ProjectID = opts.Directory
		default:
			opts.ProjectID = "default"
		}
	}
	if opts.DBPath == "" {
		dataDir, err := defaultDataDir()
		if err != nil {
			// The only failure here is an unresolvable home directory, on a
			// path where returning an error would force every caller to
			// handle a case that cannot occur in practice. Falling back to a
			// relative path keeps the plugin working in the odd environment
			// that has no home (a bare container), and the CLI's -db flag is
			// the escape hatch either way.
			dataDir = "."
		}
		opts.DBPath = filepath.Join(dataDir, "rag.db")
	}
	return opts
}

// buildEmbedding adds the embedding tier to an already-opened runtime: the
// provider client, the LSP service, and the indexer/searcher over them.
// Must be called with rtMu held (or on a runtime not yet shared, as in CLI
// mode).
func (r *runtime) buildEmbedding(ctx context.Context) error {
	providers := provider.New(modelsdev.New())
	embedder, err := embed.Resolve(ctx, embed.Config{
		Provider: r.opts.EmbeddingProvider,
		Model:    r.opts.EmbeddingModel,
		BaseURL:  r.opts.EmbeddingBaseURL,
	}, providers)
	if err != nil {
		return fmt.Errorf("resolve embeddings provider: %w", err)
	}

	// lsp.New spawns nothing by itself — servers start lazily, the first time
	// a file of a language they handle is actually walked — so building this
	// unconditionally costs nothing when indexing never runs (e.g. a
	// search-only session) or when no server for the project's languages is
	// installed. A nil config means every built-in server, none disabled;
	// this plugin has no opencode.json of its own to read one from.
	lspRoot := r.opts.Worktree
	if lspRoot == "" {
		lspRoot = r.opts.Directory
	}
	r.lsp = lsp.New(lspRoot, nil)
	r.indexer = &rag.Indexer{Store: r.store, Embedder: embedder, ProjectID: r.opts.ProjectID, LSP: r.lsp}
	r.searcher = &rag.Searcher{Store: r.store, Embedder: embedder, ProjectID: r.opts.ProjectID}
	return nil
}

// openStore builds a store-only runtime for CLI mode — the counterpart to
// ensureStore on the plugin side.
func openStore(ctx context.Context, opts runtimeOptions) (*runtime, error) {
	opts = resolveDefaults(opts)
	db, err := store.Open(ctx, opts.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	return &runtime{store: db, opts: opts}, nil
}

// buildRuntime is openStore plus the embedding tier, for the CLI paths that
// index or search.
func buildRuntime(ctx context.Context, opts runtimeOptions) (*runtime, error) {
	r, err := openStore(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := r.buildEmbedding(ctx); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

// defaultDataDir avoids importing internal/global just for one path join:
// the plugin process has no dependency on the host's XDG resolution beyond
// "put it somewhere stable under the user's data directory," so it derives
// the same ~/.local/share/gocode (or GOCODE_DATA/XDG_DATA_HOME override)
// path independently rather than sharing global.Resolve().
func defaultDataDir() (string, error) {
	if dir := os.Getenv("GOCODE_DATA"); dir != "" {
		return dir, nil
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "gocode"), nil
}

func (rt *runtime) indexRoot(path string) string {
	if path == "" {
		if rt.opts.Worktree != "" {
			return rt.opts.Worktree
		}
		return rt.opts.Directory
	}
	if filepath.IsAbs(path) {
		return path
	}
	base := rt.opts.Worktree
	if base == "" {
		base = rt.opts.Directory
	}
	return filepath.Join(base, path)
}

func (rt *runtime) indexOptions(path string, force bool) rag.IndexOptions {
	include := rt.opts.Include
	exclude := rt.opts.Exclude
	if exclude == nil {
		exclude = defaultExclude
	}
	root := rt.indexRoot(path)
	base := rt.opts.Worktree
	if base == "" {
		base = rt.opts.Directory
	}
	return rag.IndexOptions{
		Root:             root,
		Scope:            rt.relativeScope(root),
		Base:             base,
		DisableGitignore: rt.opts.DisableGitignore,
		Force:            force,
		Include:          include,
		Exclude:          exclude,
		ChunkLines:       rt.opts.ChunkLines,
		ChunkOverlap:     rt.opts.ChunkOverlap,
	}
}

// relativeScope reports root's path relative to the project base (Worktree,
// falling back to Directory), for IndexOptions.Scope. It returns "" — meaning
// "whole project, don't scope the stale-chunk diff" — whenever root isn't a
// proper subdirectory of the base: root equals the base (a full-project
// index), no base is known, or root somehow falls outside it. That last case
// shouldn't happen given indexRoot's own resolution, but falling back to the
// old unscoped behavior there is safer than scoping against a nonsensical
// relative path.
func (rt *runtime) relativeScope(root string) string {
	base := rt.opts.Worktree
	if base == "" {
		base = rt.opts.Directory
	}
	if base == "" {
		return ""
	}
	rel, err := filepath.Rel(base, root)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (rt *runtime) topK(k int) int {
	if k > 0 {
		return k
	}
	if rt.opts.TopK > 0 {
		return rt.opts.TopK
	}
	return 8
}

// ---- CLI mode ----

func runCLIIndex(args []string) error {
	fs := flag.NewFlagSet("rag-plugin index", flag.ContinueOnError)
	root := fs.String("root", ".", "directory to index")
	project := fs.String("project", "", "project id; defaults to the resolved absolute root")
	force := fs.Bool("force", false, "re-embed every chunk, ignoring stored content hashes")
	dbPath := fs.String("db", "", "sqlite path; defaults to $GOCODE_DATA/rag.db")
	embProvider := fs.String("embedding-provider", "", "models.dev provider id, or gocoder (default gocoder when signed in to gocoder.org, else openai)")
	embModel := fs.String("embedding-model", "", "embedding model id (default text-embedding-3-small)")
	embBaseURL := fs.String("embedding-base-url", "", "override the embeddings endpoint")
	include := fs.String("include", "", "comma-separated include globs")
	exclude := fs.String("exclude", "", "comma-separated exclude globs")
	disableGitignore := fs.Bool("no-gitignore", false, "don't honor .gitignore/.ignore files")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	projectID := *project
	if projectID == "" {
		projectID = absRoot
	}

	ctx := context.Background()
	r, err := buildRuntime(ctx, runtimeOptions{
		Directory:         absRoot,
		Worktree:          absRoot,
		ProjectID:         projectID,
		DBPath:            *dbPath,
		EmbeddingProvider: *embProvider,
		EmbeddingModel:    *embModel,
		EmbeddingBaseURL:  *embBaseURL,
		Include:           splitCSV(*include),
		Exclude:           splitCSV(*exclude),
		DisableGitignore:  *disableGitignore,
	})
	if err != nil {
		return err
	}
	defer r.close()

	summary, err := r.indexer.Index(ctx, r.indexOptions("", *force))
	if err != nil {
		return err
	}
	fmt.Println(summary.String())
	return nil
}

// runCLISearch is the manual-testing counterpart to runCLIIndex: query an
// already-indexed project without going through the JSON-RPC protocol.
func runCLISearch(args []string) error {
	fs := flag.NewFlagSet("rag-plugin search", flag.ContinueOnError)
	root := fs.String("root", ".", "project root; must match the root used to index")
	project := fs.String("project", "", "project id; defaults to the resolved absolute root")
	query := fs.String("query", "", "search query (required)")
	k := fs.Int("k", 8, "number of results")
	pathPrefix := fs.String("path-prefix", "", "restrict results to paths starting with this prefix")
	dbPath := fs.String("db", "", "sqlite path; defaults to $GOCODE_DATA/rag.db")
	embProvider := fs.String("embedding-provider", "", "models.dev provider id, or gocoder (default gocoder when signed in to gocoder.org, else openai)")
	embModel := fs.String("embedding-model", "", "embedding model id (default text-embedding-3-small)")
	embBaseURL := fs.String("embedding-base-url", "", "override the embeddings endpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *query == "" {
		return fmt.Errorf("-query is required")
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	projectID := *project
	if projectID == "" {
		projectID = absRoot
	}

	ctx := context.Background()
	r, err := buildRuntime(ctx, runtimeOptions{
		Directory:         absRoot,
		Worktree:          absRoot,
		ProjectID:         projectID,
		DBPath:            *dbPath,
		EmbeddingProvider: *embProvider,
		EmbeddingModel:    *embModel,
		EmbeddingBaseURL:  *embBaseURL,
	})
	if err != nil {
		return err
	}
	defer r.close()

	hits, err := r.searcher.Search(ctx, rag.SearchOptions{Query: *query, K: r.topK(*k), PathPrefix: *pathPrefix})
	if err != nil {
		return err
	}
	fmt.Println(rag.FormatHits(hits))
	return nil
}

// runCLIScan is a debugging aid: it chunks a tree exactly like index would,
// but never calls the embeddings endpoint, so it works with no provider or
// API key configured and can't itself trigger the "maximum input length"
// error it's meant to help diagnose. It exists because that error's only
// clue is a batch-relative input index — no file, no path — so pinpointing
// the offending chunk otherwise means bisecting the whole tree by hand.
func runCLIScan(args []string) error {
	fs := flag.NewFlagSet("rag-plugin scan", flag.ContinueOnError)
	root := fs.String("root", ".", "directory to scan")
	include := fs.String("include", "", "comma-separated include globs")
	exclude := fs.String("exclude", "", "comma-separated exclude globs")
	chunkLines := fs.Int("chunk-lines", 0, "chunk size in source lines (0 = default 60)")
	chunkOverlap := fs.Int("chunk-overlap", 0, "overlap between adjacent chunks (0 = default 10)")
	noGitignore := fs.Bool("no-gitignore", false, "don't honor .gitignore/.ignore files")
	threshold := fs.Int("threshold", 0, "flag chunks over this many bytes (0 = rag-plugin's own embedding clamp, DefaultMaxInputChars)")
	top := fs.Int("top", 20, "how many of the largest chunks to print")
	if err := fs.Parse(args); err != nil {
		return err
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}

	limitBytes := *threshold
	if limitBytes <= 0 {
		limitBytes = embed.DefaultMaxInputChars
	}

	exc := splitCSV(*exclude)
	if exc == nil {
		exc = defaultExclude
	}
	chunks, err := chunk.Walk(context.Background(), absRoot, chunk.Options{
		Include:          splitCSV(*include),
		Exclude:          exc,
		Lines:            *chunkLines,
		Overlap:          *chunkOverlap,
		DisableGitignore: *noGitignore,
	})
	if err != nil {
		return err
	}

	sort.Slice(chunks, func(i, j int) bool { return len(chunks[i].Content) > len(chunks[j].Content) })

	overThreshold := 0
	for _, c := range chunks {
		if len(c.Content) > limitBytes {
			overThreshold++
		}
	}
	fmt.Printf("scanned %d chunks under %s\n", len(chunks), absRoot)
	fmt.Printf("%d chunk(s) exceed %d bytes and would be truncated before embedding (see rag-plugin's DefaultMaxInputChars)\n", overThreshold, limitBytes)

	limit := *top
	if limit > len(chunks) {
		limit = len(chunks)
	}
	for i := 0; i < limit; i++ {
		c := chunks[i]
		marker := ""
		if len(c.Content) > limitBytes {
			marker = "  [OVER CLAMP]"
		}
		fmt.Printf("%8d bytes  %s:%d-%d%s\n", len(c.Content), c.Path, c.StartLine, c.EndLine, marker)
	}
	return nil
}

// ---- CLI maintenance mode ----
//
// These three commands never embed anything, so they deliberately open only
// the store (openStore, not buildRuntime): cleaning up after a project whose
// provider config or API key has since gone away must not be blocked on
// resolving that provider.

// runCLIList prints what the database actually holds, largest project first.
// It is the command every other one here is meant to be run after: chromem-go
// names its collection directories by a hash of the project id, so without
// this there is no way to find out which project owns the 300 MB directory.
func runCLIList(args []string) error {
	fs := flag.NewFlagSet("rag-plugin list", flag.ContinueOnError)
	dbPath := fs.String("db", "", "sqlite path; defaults to $GOCODE_DATA/rag.db")
	asJSON := fs.Bool("json", false, "emit the raw stats as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	r, err := openStore(ctx, runtimeOptions{DBPath: *dbPath})
	if err != nil {
		return err
	}
	defer r.close()

	projects, err := r.store.Projects(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(projects)
	}
	fmt.Print(formatProjects(r.opts.DBPath, projects))
	return nil
}

// runCLIClean removes indexed chunks: one subtree, one project, or the whole
// database. Nothing here can be undone — the deleted chunks have to be
// re-embedded, at the provider's price — so a destructive run needs either an
// interactive confirmation or an explicit -yes.
func runCLIClean(args []string) error {
	fs := flag.NewFlagSet("rag-plugin clean", flag.ContinueOnError)
	root := fs.String("root", ".", "project root, used to derive the project id when -project is omitted")
	project := fs.String("project", "", "project id to clean; defaults to the resolved absolute root")
	path := fs.String("path", "", "clean only this path prefix, relative to the project root")
	all := fs.Bool("all", false, "clean EVERY project in the database, not just this one")
	dryRun := fs.Bool("dry-run", false, "report what would be removed and remove nothing")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	dbPath := fs.String("db", "", "sqlite path; defaults to $GOCODE_DATA/rag.db")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *all && (*project != "" || *path != "") {
		return fmt.Errorf("-all cleans every project; it cannot be combined with -project or -path")
	}

	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	projectID := *project
	if projectID == "" {
		projectID = absRoot
	}

	ctx := context.Background()
	r, err := openStore(ctx, runtimeOptions{DBPath: *dbPath})
	if err != nil {
		return err
	}
	defer r.close()

	// Describe the exact target before asking, and size it from the same
	// stats `list` prints — a confirmation prompt that cannot say how much is
	// about to disappear is not really a confirmation.
	projects, err := r.store.Projects(ctx)
	if err != nil {
		return err
	}
	var action, subject string
	var affected store.ProjectStat
	switch {
	case *all:
		action = "delete every project"
		subject = fmt.Sprintf("%d project(s), %s", len(projects), formatBytes(totalBytes(projects)))
	case *path != "":
		affected = findProject(projects, projectID)
		// HashesUnderPath applies exactly the scope rule DeleteUnderPath
		// deletes by, so counting it here is a real preview rather than an
		// estimate that could disagree with what follows.
		scoped, err := r.store.HashesUnderPath(ctx, projectID, *path)
		if err != nil {
			return err
		}
		action = fmt.Sprintf("delete %d chunk(s) under %q", len(scoped), *path)
		subject = fmt.Sprintf("project %s", projectID)
	default:
		affected = findProject(projects, projectID)
		action = "delete project"
		subject = fmt.Sprintf("%s (%d chunks, %s)", projectID, affected.Chunks, formatBytes(affected.Bytes))
	}

	if *dryRun {
		fmt.Printf("dry run: would %s — %s\n", action, subject)
		return nil
	}
	if !*yes {
		ok, err := confirm(fmt.Sprintf("About to %s: %s. This cannot be undone and the chunks must be re-embedded to restore. Continue?", action, subject))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("aborted")
			return nil
		}
	}

	switch {
	case *all:
		if err := r.store.Reset(ctx); err != nil {
			return err
		}
		fmt.Printf("removed every project (%s reclaimed)\n", formatBytes(totalBytes(projects)))
	case *path != "":
		removed, err := r.store.DeleteUnderPath(ctx, projectID, *path)
		if err != nil {
			return err
		}
		fmt.Printf("removed %d chunk(s) under %q from %s\n", removed, *path, projectID)
	default:
		removed, err := r.store.DeleteProject(ctx, projectID)
		if err != nil {
			return err
		}
		fmt.Printf("removed %d chunk(s) for %s (%s reclaimed)\n", removed, projectID, formatBytes(affected.Bytes))
	}
	return nil
}

// runCLIVacuum reconciles the database against itself. Unlike clean it
// removes only data that is already unreachable, so it needs no target — but
// it still confirms, because -prune-missing widens it to projects that are
// merely absent rather than broken.
func runCLIVacuum(args []string) error {
	fs := flag.NewFlagSet("rag-plugin vacuum", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "report what would be removed and remove nothing")
	pruneMissing := fs.Bool("prune-missing", false, "also drop projects whose root directory no longer exists")
	yes := fs.Bool("yes", false, "skip the confirmation prompt")
	dbPath := fs.String("db", "", "sqlite path; defaults to $GOCODE_DATA/rag.db")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()
	r, err := openStore(ctx, runtimeOptions{DBPath: *dbPath})
	if err != nil {
		return err
	}
	defer r.close()

	// Always run the scan as a dry run first: it is the only way to tell the
	// user what they are confirming.
	preview, err := r.store.Vacuum(ctx, store.VacuumOptions{DryRun: true, PruneMissing: *pruneMissing})
	if err != nil {
		return err
	}
	if preview.Empty() {
		fmt.Println("nothing to vacuum: every project's collection and manifest agree")
		return nil
	}
	fmt.Print(formatVacuum(preview))
	if *dryRun {
		return nil
	}
	if !*yes {
		ok, err := confirm(fmt.Sprintf("Remove the above (%s)? This cannot be undone.", formatBytes(preview.BytesFreed)))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Println("aborted")
			return nil
		}
	}

	report, err := r.store.Vacuum(ctx, store.VacuumOptions{PruneMissing: *pruneMissing})
	if err != nil {
		return err
	}
	fmt.Printf("removed %d chunk(s) across %d project(s), %s reclaimed\n",
		report.ChunksRemoved,
		len(report.OrphanCollections)+len(report.DanglingProjects)+len(report.MissingProjects),
		formatBytes(report.BytesFreed))
	return nil
}

// ---- Maintenance formatting ----

func findProject(projects []store.ProjectStat, id string) store.ProjectStat {
	for _, p := range projects {
		if p.ProjectID == id {
			return p
		}
	}
	return store.ProjectStat{ProjectID: id}
}

func totalBytes(projects []store.ProjectStat) int64 {
	var total int64
	for _, p := range projects {
		total += p.Bytes
	}
	return total
}

// projectStatus names the one thing about a project a reader needs to decide
// whether to clean it. The states are mutually exclusive by construction:
// Orphan and Dangling are the two directions the collection and the manifest
// can disagree in, and a project can only be judged missing if it is
// otherwise intact.
func projectStatus(p store.ProjectStat) string {
	switch {
	case p.Orphan:
		return "orphan (no manifest rows)"
	case p.Dangling:
		return "dangling (no collection)"
	case p.RootMissing:
		return "root missing"
	default:
		return "ok"
	}
}

func formatProjects(dbPath string, projects []store.ProjectStat) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", dbPath)
	if len(projects) == 0 {
		b.WriteString("no projects indexed\n")
		return b.String()
	}

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PROJECT\tCHUNKS\tFILES\tSIZE\tMODIFIED\tSTATUS")
	var chunks int
	for _, p := range projects {
		modified := "-"
		if !p.ModTime.IsZero() {
			modified = p.ModTime.Format("2006-01-02")
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\t%s\n",
			p.ProjectID, p.Chunks, p.Paths, formatBytes(p.Bytes), modified, projectStatus(p))
		chunks += p.Chunks
	}
	fmt.Fprintf(w, "%d project(s)\t%d\t\t%s\t\t\n", len(projects), chunks, formatBytes(totalBytes(projects)))
	w.Flush()
	return b.String()
}

func formatVacuum(report store.VacuumReport) string {
	var b strings.Builder
	section := func(title string, ids []string) {
		if len(ids) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", title)
		for _, id := range ids {
			fmt.Fprintf(&b, "  %s\n", id)
		}
	}
	section("orphan collections (vectors on disk, no manifest rows — unreachable)", report.OrphanCollections)
	section("dangling projects (manifest rows, no collection — unsearchable)", report.DanglingProjects)
	section("missing roots (project directory no longer exists)", report.MissingProjects)
	fmt.Fprintf(&b, "%d chunk(s), %s\n", report.ChunksRemoved, formatBytes(report.BytesFreed))
	return b.String()
}

// formatBytes renders a size the way `du -h` would. Exact byte counts are
// noise in a report whose entire purpose is "which of these is the big one."
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}

// confirm asks for a y/N answer on stdin, and treats "nobody was there to
// answer" as an error rather than as "no".
//
// That distinction is the whole point. A caller that cannot answer — CI, a
// cron job, `< /dev/null` — must not have its cleanup silently turn into a
// no-op it then reports as success; -yes is how a script says it meant it.
// Emptiness is detected from the read rather than by inspecting stdin's
// mode, because the obvious mode check is wrong: os.ModeCharDevice is set
// for /dev/null exactly as it is for a terminal, so `clean < /dev/null`
// would look interactive, read EOF, and quietly abort with a success exit
// code. Reading first and judging what came back needs no platform-specific
// tty ioctl and gets that case right.
func confirm(prompt string) (bool, error) {
	fmt.Printf("%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if line == "" && errors.Is(err, io.EOF) {
		fmt.Println()
		return false, fmt.Errorf("stdin closed without an answer; pass -yes to confirm non-interactively")
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- Plugin (JSON-RPC) mode ----

type request struct {
	ID     *int64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

var out = json.NewEncoder(os.Stdout)

func runPlugin() {
	in := json.NewDecoder(os.Stdin)
	for {
		var message request
		if err := in.Decode(&message); err != nil {
			if !errors.Is(err, io.EOF) {
				fmt.Fprintln(os.Stderr, "decode:", err)
			}
			closeRuntime()
			return
		}
		if message.Method == "shutdown" {
			closeRuntime()
			return
		}
		if err := dispatch(message); err != nil {
			fmt.Fprintln(os.Stderr, message.Method+":", err)
		}
	}
}

func dispatch(message request) error {
	switch message.Method {
	case "initialize":
		return handleInitialize(message)
	case "hook":
		// No hooks are declared in the manifest, so the host never sends one;
		// answered defensively rather than left silently unhandled.
		return reply(message.ID, map[string]any{}, nil)
	case "tool":
		return handleTool(message)
	default:
		return reply(message.ID, nil, fmt.Errorf("unknown method %q", message.Method))
	}
}

func handleInitialize(message request) error {
	var params struct {
		Protocol int `json:"protocol"`
		Input    struct {
			Directory string            `json:"directory"`
			Worktree  string            `json:"worktree"`
			ProjectID string            `json:"projectID"`
			ServerURL string            `json:"serverURL"`
			Headers   map[string]string `json:"headers"`
			Version   string            `json:"version"`
		} `json:"input"`
		Options map[string]any `json:"options"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return err
	}

	opts := runtimeOptions{
		Directory:         params.Input.Directory,
		Worktree:          params.Input.Worktree,
		ProjectID:         params.Input.ProjectID,
		DBPath:            stringOpt(params.Options, "dbPath", ""),
		EmbeddingProvider: stringOpt(params.Options, "embeddingProvider", ""),
		EmbeddingModel:    stringOpt(params.Options, "embeddingModel", ""),
		EmbeddingBaseURL:  stringOpt(params.Options, "embeddingBaseURL", ""),
		Include:           stringSliceOpt(params.Options, "include"),
		Exclude:           stringSliceOpt(params.Options, "exclude"),
		ChunkLines:        intOpt(params.Options, "chunkLines", 0),
		ChunkOverlap:      intOpt(params.Options, "chunkOverlap", 0),
		TopK:              intOpt(params.Options, "topK", 0),
		DisableGitignore:  boolOpt(params.Options, "disableGitignore", false),
	}

	// Only remembered here — see ensureRuntime for why the store is not
	// opened until a tool call needs it. The manifest below is static, so
	// nothing in this reply depends on the runtime existing yet.
	rtMu.Lock()
	pending = &opts
	rtMu.Unlock()

	return reply(message.ID, map[string]any{
		"id":    "rag-plugin",
		"hooks": []string{},
		"tools": []map[string]any{
			{
				"name":        "rag_index",
				"description": "(Re)index project files for semantic search. Incremental: only changed files are re-embedded. Run after large edits, or if rag_search finds nothing.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string", "description": "Subdirectory to index, relative to the project root. Default: the whole project."},
						"force":   map[string]any{"type": "boolean", "description": "Re-embed every chunk. Needed after switching embedding models."},
						"timeout": map[string]any{"type": "integer", "description": "Maximum time in seconds for this indexing call. Default 300."},
					},
				},
			},
			{
				"name": "rag_search",
				// The decision rule is the point of this description. Semantic
				// search and grep fail in opposite directions — grep cannot
				// find code whose wording you guessed wrong, semantic search
				// cannot guarantee every literal occurrence — so a blanket
				// "prefer this" would be wrong as often as it was right. What
				// the model needs is which failure it is facing.
				"description": "Search this project's code by meaning. Returns ranked path:line snippets. " +
					"Try this FIRST when locating unfamiliar code — where a concept lives, how something is implemented, what handles a behaviour — instead of grep/ripgrep, find, or shell search, which only match text you can already spell. Use grep instead for an exact symbol or string you already know, or when you need every occurrence.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":      map[string]any{"type": "string", "description": "What to find, in natural language."},
						"k":          map[string]any{"type": "integer", "description": "Max results. Default 8."},
						"pathPrefix": map[string]any{"type": "string", "description": "Only return paths under this prefix."},
					},
					"required": []string{"query"},
				},
			},
			{
				"name":        "rag_status",
				"description": "List indexed projects with chunk counts, size on disk, and health. Read-only. Run before rag_clean or rag_vacuum.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
			{
				"name": "rag_clean",
				// The irreversibility warning stays even under a tightening
				// pass: it is the only brake on this tool. Nothing in the
				// protocol can prompt the user (see handleMaintenanceTool),
				// so the description is where the caution has to live.
				"description": "Permanently delete indexed chunks. IRREVERSIBLE — restoring them means paying to re-embed. " +
					"Prefer re-running rag_index, which prunes stale chunks on its own. Preview with dryRun.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"scope":     map[string]any{"type": "string", "enum": []string{"path", "project", "all"}, "description": "\"path\": one subtree. \"project\": one whole project. \"all\": EVERY project on this machine."},
						"path":      map[string]any{"type": "string", "description": "Path prefix relative to the project root. Required for scope \"path\"."},
						"projectId": map[string]any{"type": "string", "description": "Only for scope \"project\". Default: the current project."},
						"dryRun":    map[string]any{"type": "boolean", "description": "Report what would be deleted, delete nothing."},
						"confirm":   map[string]any{"type": "boolean", "description": "Must be true to delete. Ask the user first."},
					},
					"required": []string{"scope", "confirm"},
				},
			},
			{
				"name":        "rag_vacuum",
				"description": "Reclaim space by removing index data no re-index can reach (storage and bookkeeping out of sync). Never touches a healthy project. Preview with dryRun.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"pruneMissing": map[string]any{"type": "boolean", "description": "Also drop projects whose directory is gone. Off by default; ask the user."},
						"dryRun":       map[string]any{"type": "boolean", "description": "Report what would be removed, remove nothing."},
						"confirm":      map[string]any{"type": "boolean", "description": "Must be true to remove. Ask the user first."},
					},
					"required": []string{"confirm"},
				},
			},
		},
	}, nil)
}

func handleTool(message request) error {
	var params struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return err
	}

	// The maintenance tools stop at the store tier. They only read and
	// delete already-stored chunks, so routing them through ensureRuntime
	// would fail them whenever the embeddings provider can't be resolved —
	// which is precisely the state an index worth cleaning up tends to be
	// in (provider removed from config, key rotated, project abandoned).
	switch params.Name {
	case "rag_status", "rag_clean", "rag_vacuum":
		r, err := ensureStore()
		if err != nil {
			return reply(message.ID, nil, err)
		}
		return handleMaintenanceTool(message, r, params.Name, params.Args)
	}

	rt, err := ensureRuntime()
	if err != nil {
		return reply(message.ID, nil, err)
	}

	switch params.Name {
	case "rag_index":
		path := stringOpt(params.Args, "path", "")
		force := boolOpt(params.Args, "force", false)
		timeout := intOpt(params.Args, "timeout", defaultIndexTimeoutSeconds)
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
		defer cancel()
		summary, err := rt.indexer.Index(ctx, rt.indexOptions(path, force))
		if err != nil {
			return reply(message.ID, nil, err)
		}
		return reply(message.ID, map[string]any{
			"title":  "rag_index",
			"output": summary.String(),
		}, nil)

	case "rag_search":
		query := stringOpt(params.Args, "query", "")
		hits, err := rt.searcher.Search(context.Background(), rag.SearchOptions{
			Query:      query,
			K:          rt.topK(intOpt(params.Args, "k", 0)),
			PathPrefix: stringOpt(params.Args, "pathPrefix", ""),
		})
		if err != nil {
			return reply(message.ID, nil, err)
		}
		return reply(message.ID, map[string]any{
			"title":  "rag_search",
			"output": rag.FormatHits(hits),
		}, nil)

	default:
		return reply(message.ID, nil, fmt.Errorf("unknown tool %q", params.Name))
	}
}

// handleMaintenanceTool serves rag_status, rag_clean and rag_vacuum off a
// store-only runtime.
//
// Every destructive branch here checks confirm before doing anything. That
// is a guard against an accidental call, not a security boundary, and it is
// worth being honest about which: the process-plugin protocol has no
// permission field (internal/plugin's manifestTool carries only
// name/description/parameters, and Process.hooks wires Execute straight
// through), so nothing between the model and this code can prompt a human.
// confirm is an argument the caller supplies about itself. The real
// safeguards are that the destructive scopes are described as irreversible
// in the manifest, that dryRun exists and is free, and that the -all
// equivalent is reachable from the CLI where a person types it.
func handleMaintenanceTool(message request, r *runtime, name string, args map[string]any) error {
	ctx := context.Background()
	confirmed := boolOpt(args, "confirm", false)
	dryRun := boolOpt(args, "dryRun", false)

	switch name {
	case "rag_status":
		projects, err := r.store.Projects(ctx)
		if err != nil {
			return reply(message.ID, nil, err)
		}
		return reply(message.ID, map[string]any{
			"title":  "rag_status",
			"output": formatProjects(r.opts.DBPath, projects),
		}, nil)

	case "rag_clean":
		scope := stringOpt(args, "scope", "")
		if !dryRun && !confirmed {
			return reply(message.ID, nil, fmt.Errorf("rag_clean: nothing was deleted — set confirm=true to delete, or dryRun=true to preview. Ask the user before confirming: this cannot be undone and restoring means re-embedding"))
		}
		switch scope {
		case "path":
			path := stringOpt(args, "path", "")
			if path == "" {
				return reply(message.ID, nil, fmt.Errorf("rag_clean: scope \"path\" requires a path"))
			}
			if dryRun {
				scoped, err := r.store.HashesUnderPath(ctx, r.opts.ProjectID, path)
				if err != nil {
					return reply(message.ID, nil, err)
				}
				return maintenanceReply(message, "rag_clean", fmt.Sprintf("dry run: would delete %d chunk(s) under %q from %s", len(scoped), path, r.opts.ProjectID))
			}
			removed, err := r.store.DeleteUnderPath(ctx, r.opts.ProjectID, path)
			if err != nil {
				return reply(message.ID, nil, err)
			}
			return maintenanceReply(message, "rag_clean", fmt.Sprintf("deleted %d chunk(s) under %q from %s. Re-run rag_index to restore them.", removed, path, r.opts.ProjectID))

		case "project":
			projectID := stringOpt(args, "projectId", r.opts.ProjectID)
			projects, err := r.store.Projects(ctx)
			if err != nil {
				return reply(message.ID, nil, err)
			}
			target := findProject(projects, projectID)
			if dryRun {
				return maintenanceReply(message, "rag_clean", fmt.Sprintf("dry run: would delete project %s (%d chunks, %s)", projectID, target.Chunks, formatBytes(target.Bytes)))
			}
			removed, err := r.store.DeleteProject(ctx, projectID)
			if err != nil {
				return reply(message.ID, nil, err)
			}
			return maintenanceReply(message, "rag_clean", fmt.Sprintf("deleted project %s: %d chunk(s), %s reclaimed. Re-run rag_index to restore them.", projectID, removed, formatBytes(target.Bytes)))

		case "all":
			projects, err := r.store.Projects(ctx)
			if err != nil {
				return reply(message.ID, nil, err)
			}
			if dryRun {
				return maintenanceReply(message, "rag_clean", fmt.Sprintf("dry run: would delete all %d project(s), %s", len(projects), formatBytes(totalBytes(projects))))
			}
			if err := r.store.Reset(ctx); err != nil {
				return reply(message.ID, nil, err)
			}
			return maintenanceReply(message, "rag_clean", fmt.Sprintf("deleted all %d project(s), %s reclaimed. Every project sharing this database must be re-indexed.", len(projects), formatBytes(totalBytes(projects))))

		default:
			return reply(message.ID, nil, fmt.Errorf("rag_clean: scope must be \"path\", \"project\" or \"all\", got %q", scope))
		}

	case "rag_vacuum":
		opts := store.VacuumOptions{
			DryRun:       dryRun || !confirmed,
			PruneMissing: boolOpt(args, "pruneMissing", false),
		}
		report, err := r.store.Vacuum(ctx, opts)
		if err != nil {
			return reply(message.ID, nil, err)
		}
		if report.Empty() {
			return maintenanceReply(message, "rag_vacuum", "nothing to vacuum: every project's collection and manifest agree")
		}
		if opts.DryRun {
			prefix := "dry run — nothing removed:\n"
			if !confirmed && !dryRun {
				prefix = "nothing was removed (confirm was not set):\n"
			}
			return maintenanceReply(message, "rag_vacuum", prefix+formatVacuum(report))
		}
		return maintenanceReply(message, "rag_vacuum", fmt.Sprintf("%sremoved %d chunk(s), %s reclaimed",
			formatVacuum(report), report.ChunksRemoved, formatBytes(report.BytesFreed)))

	default:
		return reply(message.ID, nil, fmt.Errorf("unknown tool %q", name))
	}
}

func maintenanceReply(message request, title, output string) error {
	return reply(message.ID, map[string]any{"title": title, "output": output}, nil)
}

func reply(id *int64, result any, failure error) error {
	if id == nil {
		return nil
	}
	message := map[string]any{"jsonrpc": "2.0", "id": id}
	if failure != nil {
		message["error"] = map[string]any{"code": -32603, "message": failure.Error()}
	} else {
		message["result"] = result
	}
	return out.Encode(message)
}

// ---- Options-bag helpers (Options/Args arrive as map[string]any over JSON-RPC) ----

func stringOpt(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func boolOpt(m map[string]any, key string, def bool) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return def
}

func intOpt(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64: // encoding/json decodes JSON numbers into map[string]any as float64
		return int(v)
	case int:
		return v
	}
	return def
}

func stringSliceOpt(m map[string]any, key string) []string {
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
