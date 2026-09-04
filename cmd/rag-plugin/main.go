// Command rag-plugin is a gocode process plugin (see
// examples/plugin-echo for the protocol) that indexes a project's files for
// semantic search and exposes two tools: rag_index and rag_search.
//
// It speaks newline-delimited JSON-RPC 2.0 on stdin/stdout when launched by
// the host, exactly like examples/plugin-echo. It can also run a one-shot
// index directly from the shell (`rag-plugin index ...`), which exists
// because the host bounds a tool call it makes with no deadline of its own
// to 30s (internal/plugin/process.go's DefaultCallTimeout) — too short for a
// large repo's first index. Both paths share the same runtime construction
// and the same internal/rag orchestration.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/lsp"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/provider"
	"github.com/langazov/gocode-go/internal/rag"
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
}

// defaultExclude keeps the common dependency/output directories out of an
// index by default; .git is already skipped unconditionally by chunk.Walk.
var defaultExclude = []string{"**/node_modules/**", "**/vendor/**", "**/dist/**", "**/.venv/**"}

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

// close releases every resource buildRuntime opened. Every exit path (CLI
// mode's defer, and the plugin loop's shutdown/EOF handlers) must call this;
// skipping the lsp.Shutdown half would leak a spawned language server
// process past the rag-plugin process's own lifetime.
func (r *runtime) close() {
	r.lsp.Shutdown()
	r.store.Close()
}

var rt *runtime

func buildRuntime(ctx context.Context, opts runtimeOptions) (*runtime, error) {
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
			return nil, fmt.Errorf("resolve data dir: %w", err)
		}
		opts.DBPath = filepath.Join(dataDir, "rag.db")
	}

	db, err := store.Open(ctx, opts.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}

	providers := provider.New(modelsdev.New())
	embedder, err := embed.Resolve(ctx, embed.Config{
		Provider: opts.EmbeddingProvider,
		Model:    opts.EmbeddingModel,
		BaseURL:  opts.EmbeddingBaseURL,
	}, providers)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("resolve embeddings provider: %w", err)
	}

	// lsp.New spawns nothing by itself — servers start lazily, the first time
	// a file of a language they handle is actually walked — so building this
	// unconditionally costs nothing when indexing never runs (e.g. a
	// search-only session) or when no server for the project's languages is
	// installed. A nil config means every built-in server, none disabled;
	// this plugin has no opencode.json of its own to read one from.
	lspRoot := opts.Worktree
	if lspRoot == "" {
		lspRoot = opts.Directory
	}
	lspService := lsp.New(lspRoot, nil)

	return &runtime{
		store:    db,
		indexer:  &rag.Indexer{Store: db, Embedder: embedder, ProjectID: opts.ProjectID, LSP: lspService},
		searcher: &rag.Searcher{Store: db, Embedder: embedder, ProjectID: opts.ProjectID},
		lsp:      lspService,
		opts:     opts,
	}, nil
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
	return rag.IndexOptions{
		Root:         rt.indexRoot(path),
		Force:        force,
		Include:      include,
		Exclude:      exclude,
		ChunkLines:   rt.opts.ChunkLines,
		ChunkOverlap: rt.opts.ChunkOverlap,
	}
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
	embProvider := fs.String("embedding-provider", "", "models.dev provider id (default openai)")
	embModel := fs.String("embedding-model", "", "embedding model id (default text-embedding-3-small)")
	embBaseURL := fs.String("embedding-base-url", "", "override the embeddings endpoint")
	include := fs.String("include", "", "comma-separated include globs")
	exclude := fs.String("exclude", "", "comma-separated exclude globs")
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
	embProvider := fs.String("embedding-provider", "", "models.dev provider id (default openai)")
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
			if rt != nil {
				rt.close()
			}
			return
		}
		if message.Method == "shutdown" {
			if rt != nil {
				rt.close()
			}
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
	}

	built, err := buildRuntime(context.Background(), opts)
	if err != nil {
		return reply(message.ID, nil, fmt.Errorf("rag-plugin: %w", err))
	}
	rt = built

	return reply(message.ID, map[string]any{
		"id":    "rag-plugin",
		"hooks": []string{},
		"tools": []map[string]any{
			{
				"name":        "rag_index",
				"description": "(Re)index project files for semantic search. Only embeds files that changed since the last index. Run this before rag_search if the project hasn't been indexed yet, or after making significant changes.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":  map[string]any{"type": "string", "description": "Directory to index, relative to the project root. Defaults to the whole project."},
						"force": map[string]any{"type": "boolean", "description": "Re-embed every chunk even if unchanged. Use after switching embedding models."},
					},
				},
			},
			{
				"name":        "rag_search",
				"description": "Semantically search the project's indexed files for chunks relevant to a natural-language query. Returns ranked snippets with file:line citations. Run rag_index first if the project hasn't been indexed.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query":      map[string]any{"type": "string", "description": "Natural-language search query."},
						"k":          map[string]any{"type": "integer", "description": "Maximum number of results. Defaults to 8."},
						"pathPrefix": map[string]any{"type": "string", "description": "Restrict results to paths starting with this prefix."},
					},
					"required": []string{"query"},
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
	if rt == nil {
		return reply(message.ID, nil, fmt.Errorf("rag-plugin: not initialized"))
	}

	switch params.Name {
	case "rag_index":
		path := stringOpt(params.Args, "path", "")
		force := boolOpt(params.Args, "force", false)
		summary, err := rt.indexer.Index(context.Background(), rt.indexOptions(path, force))
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
