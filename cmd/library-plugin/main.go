// Command library-plugin is a gocode process plugin (see
// examples/plugin-echo for the protocol) that exposes the user's
// gocoder.org Library — a personal, cross-machine collection of uploaded
// md/txt/pdf documents, semantically searchable — as four tools:
//
//   - library_search — semantic search over the account's library, run
//     server-side (gocoder.org embeds the query and does the vector search;
//     this plugin does none of that itself).
//   - library_list — browse the library's folder/file tree.
//   - library_get — fetch one node's metadata, optionally its full content.
//   - library_upload — push a local .md/.txt/.pdf file into the library.
//
// This is deliberately much simpler than rag-plugin: rag-plugin chunks,
// embeds, and vector-searches a local project itself, so it needs an
// embeddings provider, a local store, and an LSP service. The Library API
// does all of that server-side (website/backend/internal/library in
// gocode-infra) — this plugin is an authenticated HTTP client wrapped as
// gocode tools, nothing more.
//
// Auth is the same gk_ API key rag-plugin's "gocoder" embedding provider
// already uses (internal/gocoder.LoadAccount): sign in to gocoder.org once
// via gocode, and both plugins share the stored account.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/langazov/gocode-go/internal/gocoder"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "search":
			if err := runCLISearch(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "library-plugin search:", err)
				os.Exit(1)
			}
			return
		case "list":
			if err := runCLIList(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "library-plugin list:", err)
				os.Exit(1)
			}
			return
		case "get":
			if err := runCLIGet(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "library-plugin get:", err)
				os.Exit(1)
			}
			return
		case "upload":
			if err := runCLIUpload(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "library-plugin upload:", err)
				os.Exit(1)
			}
			return
		}
	}
	runPlugin()
}

// ---- runtime ----

const (
	defaultTopK          = 10
	defaultUploadTimeout = 120 // seconds; matches the manifest's callTimeoutSeconds headroom

	// uploadPollInterval is how often library_upload polls the node's
	// status while wait=true. gocoder.org's own pipeline (convert -> chunk
	// -> embed) typically finishes small documents in a couple of seconds,
	// so this favors prompt completion over request volume.
	uploadPollInterval = 2 * time.Second

	// maxUploadBytes mirrors website/backend/internal/library.MaxUploadBytes
	// (gocode-infra) so an oversized upload fails locally, fast, rather than
	// after a slow transfer the server would reject anyway. Kept in sync by
	// hand — a coupling point like rag-plugin's embedding-model/vector-size
	// one, flagged here for the same reason.
	maxUploadBytes = 25 << 20
)

// allowedUploadExt mirrors website/backend/internal/library.SourceKindForExt
// (gocode-infra), which is not exported across repos. Same coupling-point
// caveat as maxUploadBytes above: if gocode-infra adds a format, this list
// needs a matching update.
var allowedUploadExt = map[string]bool{".md": true, ".txt": true, ".pdf": true}

// runtimeOptions is what both the plugin handshake and the CLI mode resolve
// into before building a runtime.
type runtimeOptions struct {
	Directory string
	Worktree  string

	BaseURL       string
	TopK          int
	UploadTimeout int
}

func resolveDefaults(opts runtimeOptions) runtimeOptions {
	if opts.TopK <= 0 {
		opts.TopK = defaultTopK
	}
	if opts.UploadTimeout <= 0 {
		opts.UploadTimeout = defaultUploadTimeout
	}
	return opts
}

// runtime is the live set of services one plugin process (or one CLI
// invocation) uses.
type runtime struct {
	client *gocoder.Client
	bearer string
	opts   runtimeOptions
}

// newRuntime resolves the stored gocoder.org account and builds a runtime
// from it. Unlike rag-plugin's embed.Resolve, there is no "or fall back to a
// plain OpenAI setup" path: the Library only exists on gocoder.org, so no
// account means no runtime, full stop.
func newRuntime(opts runtimeOptions) (*runtime, error) {
	opts = resolveDefaults(opts)
	account, err := gocoder.LoadAccount()
	if err != nil {
		return nil, fmt.Errorf("library-plugin: load gocoder.org account: %w", err)
	}
	if account == nil || account.Key == "" {
		return nil, fmt.Errorf("library-plugin: no gocoder.org account is stored at %s; start gocode to register or log in — your library lives on gocoder.org and needs an account", gocoder.AccountPath())
	}
	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = account.URL
	}
	if baseURL == "" {
		baseURL = gocoder.BaseURL()
	}
	return &runtime{
		client: gocoder.NewClient(baseURL),
		bearer: account.Key,
		opts:   opts,
	}, nil
}

func (rt *runtime) topK(k int) int {
	if k > 0 {
		return k
	}
	return rt.opts.TopK
}

// resolveLocalPath resolves a tool-supplied local file path against the
// project directory/worktree the host handed this plugin at handshake time
// (see handleInitialize), the same base rag-plugin resolves rag_index's
// path argument against.
func (rt *runtime) resolveLocalPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	base := rt.opts.Worktree
	if base == "" {
		base = rt.opts.Directory
	}
	if base == "" {
		return p
	}
	return filepath.Join(base, p)
}

// rt is the process's single runtime, and pending holds the options the
// handshake resolved it from — the same lazy-build shape rag-plugin uses
// (ensureStore/ensureRuntime in cmd/rag-plugin/main.go), so a session that
// never calls a library_* tool never pays to resolve an account or build an
// HTTP client.
var (
	rtMu    sync.Mutex
	rt      *runtime
	pending *runtimeOptions
)

// ensureRuntime builds the runtime on first use. A failed build is not
// cached: an account that doesn't exist yet at the first call (the user
// hasn't signed in) may well exist by the second.
func ensureRuntime() (*runtime, error) {
	rtMu.Lock()
	defer rtMu.Unlock()
	if rt != nil {
		return rt, nil
	}
	if pending == nil {
		return nil, fmt.Errorf("library-plugin: not initialized")
	}
	r, err := newRuntime(*pending)
	if err != nil {
		return nil, err
	}
	rt = r
	return rt, nil
}

// ---- library path helpers ----
// Minimal reimplementations of gocode-infra's
// website/backend/internal/library.NormalizePath/SplitPath — that package is
// not importable across repos/modules, and the rules are small enough not to
// be worth vendoring.

func normalizeLibraryPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.Trim(p, "/")
	if p == "" || p == "." {
		return ""
	}
	cleaned := path.Clean(p)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func splitLibraryPath(p string) (parent, name string) {
	if p == "" {
		return "", ""
	}
	idx := strings.LastIndexByte(p, '/')
	if idx < 0 {
		return "", p
	}
	return p[:idx], p[idx+1:]
}

// ensureAncestorFolders creates every missing ancestor folder of libraryPath
// (not libraryPath itself), mkdir -p style. This matters because the
// Library's tree is simulated by exact ParentPath matches (no synthesized
// virtual folders — see gocode-infra's node_memory.go/node_mongo.go
// ListChildren): uploading straight to "docs/report.pdf" with no "docs"
// folder node makes the file invisible to library_list from the root.
// CreateLibraryFolder treats "already exists" as success, so this is safe
// to call unconditionally, including for a path that's already fully
// there.
func ensureAncestorFolders(ctx context.Context, rt *runtime, libraryPath string) error {
	parent, _ := splitLibraryPath(libraryPath)
	if parent == "" {
		return nil
	}
	segments := strings.Split(parent, "/")
	var built strings.Builder
	for i, seg := range segments {
		if i > 0 {
			built.WriteByte('/')
		}
		built.WriteString(seg)
		if err := rt.client.CreateLibraryFolder(ctx, rt.bearer, built.String()); err != nil {
			return fmt.Errorf("ensure folder %q: %w", built.String(), err)
		}
	}
	return nil
}

// resolveLibraryPath walks a path segment by segment via ListLibraryNodes to
// find the node at it, for callers (library_get) that know a path but not
// an id. There is no get-by-path HTTP endpoint — only get-by-id and
// list-children — so this costs one call per path segment.
func resolveLibraryPath(ctx context.Context, rt *runtime, libraryPath string) (*gocoder.LibraryNode, error) {
	libraryPath = normalizeLibraryPath(libraryPath)
	if libraryPath == "" {
		return nil, fmt.Errorf("empty path")
	}
	segments := strings.Split(libraryPath, "/")
	parent := ""
	var found *gocoder.LibraryNode
	for _, seg := range segments {
		children, err := rt.client.ListLibraryNodes(ctx, rt.bearer, parent)
		if err != nil {
			return nil, err
		}
		found = nil
		for i := range children {
			if children[i].Name == seg {
				found = &children[i]
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("no node named %q under %q", seg, displayParent(parent))
		}
		if parent == "" {
			parent = seg
		} else {
			parent = parent + "/" + seg
		}
	}
	return found, nil
}

func displayParent(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func validateUploadExt(filename string) error {
	ext := strings.ToLower(path.Ext(filename))
	if !allowedUploadExt[ext] {
		return fmt.Errorf("unsupported file type %q (allowed: .md, .txt, .pdf)", ext)
	}
	return nil
}

// sliceLines returns 1-based inclusive lines [start, end] of content — the
// same convention gocode's own chunk.Chunk.StartLine/EndLine use — clamped
// to content's actual line count. Used by library_search to recover a full
// chunk when a hit's Content wasn't populated (see handleLibrarySearch).
func sliceLines(content string, start, end int) string {
	lines := strings.Split(content, "\n")
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	if start > end || start > len(lines) {
		return ""
	}
	return strings.Join(lines[start-1:end], "\n")
}

// ---- formatting ----

func formatNode(n gocoder.LibraryNode) string {
	if n.Type == gocoder.LibraryTypeFolder {
		return fmt.Sprintf("%s  [folder]  id=%s", n.Path, n.ID)
	}
	status := n.Status
	if n.StatusDetail != "" {
		status += ": " + n.StatusDetail
	}
	return fmt.Sprintf("%s  [file, %s, %s]  id=%s  status=%s", n.Path, n.SourceKind, formatBytes(n.SizeBytes), n.ID, status)
}

func formatNodeList(nodes []gocoder.LibraryNode) string {
	if len(nodes) == 0 {
		return "no nodes"
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "TYPE\tPATH\tSIZE\tSTATUS\tID")
	for _, n := range nodes {
		size := ""
		if n.Type == gocoder.LibraryTypeFile {
			size = formatBytes(n.SizeBytes)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n.Type, n.Path, size, n.Status, n.ID)
	}
	w.Flush()
	return b.String()
}

func formatSearchHits(hits []gocoder.LibrarySearchHit) string {
	if len(hits) == 0 {
		return "no results"
	}
	var b strings.Builder
	for i, hit := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		p, id := "?", "?"
		if hit.Node != nil {
			p, id = hit.Node.Path, hit.Node.ID
		}
		fmt.Fprintf(&b, "%s:%d-%d  (score %.3f, id=%s)", p, hit.StartLine, hit.EndLine, hit.Score, id)
		if len(hit.HeadingPath) > 0 {
			fmt.Fprintf(&b, "  [%s]", strings.Join(hit.HeadingPath, " > "))
		}
		b.WriteString("\n")
		text := hit.Content
		if text == "" {
			text = hit.Snippet
		}
		b.WriteString(text)
	}
	return b.String()
}

func formatUploadResult(n *gocoder.LibraryNode, waited, timedOut bool) string {
	var b strings.Builder
	b.WriteString(formatNode(*n))
	switch {
	case n.Status == gocoder.LibraryStatusFailed:
		fmt.Fprintf(&b, "\nindexing failed: %s", n.StatusDetail)
	case n.Status == gocoder.LibraryStatusReady:
		b.WriteString("\nready — indexed and searchable.")
	case !waited:
		b.WriteString("\nuploaded — indexing runs in the background; poll library_get with this id to check status.")
	case timedOut:
		b.WriteString("\nstill processing after the wait timeout; poll library_get with this id to check status.")
	}
	return b.String()
}

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

// ---- tool handlers (shared by plugin and CLI modes) ----

func handleLibrarySearch(ctx context.Context, rt *runtime, query, pathPrefix string, k int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("library_search: query is required")
	}
	hits, err := rt.client.SearchLibrary(ctx, rt.bearer, query, normalizeLibraryPath(pathPrefix), rt.topK(k))
	if err != nil {
		return "", err
	}

	// SearchLibrary always asks the server for full=true (see its doc
	// comment), so Content is normally already the complete chunk. This is
	// the fallback for a gocoder.org deployment that predates that
	// parameter and silently ignored it: fetch the node's full content
	// (once per node, not once per hit) and slice out the matched lines,
	// rather than settling for the 280-char Snippet.
	contentCache := map[string]string{}
	for i := range hits {
		hit := &hits[i]
		if hit.Content != "" || hit.Node == nil {
			continue
		}
		full, cached := contentCache[hit.Node.ID]
		if !cached {
			fetched, ferr := rt.client.GetLibraryContent(ctx, rt.bearer, hit.Node.ID)
			if ferr == nil {
				full = fetched
			}
			contentCache[hit.Node.ID] = full // cache "" on failure too, so we don't retry
		}
		if full != "" {
			if sliced := sliceLines(full, hit.StartLine, hit.EndLine); sliced != "" {
				hit.Content = sliced
			}
		}
	}
	return formatSearchHits(hits), nil
}

func handleLibraryList(ctx context.Context, rt *runtime, pathPrefix string) (string, error) {
	nodes, err := rt.client.ListLibraryNodes(ctx, rt.bearer, normalizeLibraryPath(pathPrefix))
	if err != nil {
		return "", err
	}
	return formatNodeList(nodes), nil
}

func handleLibraryGet(ctx context.Context, rt *runtime, id, path string, withContent bool) (string, error) {
	// A real node id (a Mongo ObjectID hex string) never contains "/". A
	// caller — the model included, since it only ever sees a hit's Path in
	// library_search's citation line, not its id — sometimes passes a path
	// where an id was asked for; recover rather than let GET
	// /library/nodes/{escaped path} 404.
	if id != "" && strings.Contains(id, "/") {
		path, id = id, ""
	}

	var n *gocoder.LibraryNode
	var err error
	switch {
	case id != "":
		n, err = rt.client.GetLibraryNode(ctx, rt.bearer, id)
	case path != "":
		n, err = resolveLibraryPath(ctx, rt, path)
	default:
		return "", fmt.Errorf("library_get: id or path is required")
	}
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(formatNode(*n))
	if withContent {
		if n.Type != gocoder.LibraryTypeFile {
			return "", fmt.Errorf("library_get: %s is a folder, not a file; withContent only applies to files", n.Path)
		}
		content, err := rt.client.GetLibraryContent(ctx, rt.bearer, n.ID)
		if err != nil {
			return "", err
		}
		b.WriteString("\n\n---\n\n")
		b.WriteString(content)
	}
	return b.String(), nil
}

func handleLibraryUpload(ctx context.Context, rt *runtime, localPath, libraryPath string, wait bool, timeoutSeconds int) (string, error) {
	if localPath == "" {
		return "", fmt.Errorf("library_upload: localPath is required")
	}
	libraryPath = normalizeLibraryPath(libraryPath)
	if libraryPath == "" {
		return "", fmt.Errorf("library_upload: path is required")
	}
	if timeoutSeconds <= 0 {
		timeoutSeconds = rt.opts.UploadTimeout
	}

	resolved := rt.resolveLocalPath(localPath)
	if err := validateUploadExt(resolved); err != nil {
		return "", fmt.Errorf("library_upload: %w", err)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", fmt.Errorf("library_upload: read %s: %w", resolved, err)
	}
	if len(data) > maxUploadBytes {
		return "", fmt.Errorf("library_upload: %s is %d bytes, exceeds the %d byte limit", resolved, len(data), maxUploadBytes)
	}

	if err := ensureAncestorFolders(ctx, rt, libraryPath); err != nil {
		return "", fmt.Errorf("library_upload: %w", err)
	}

	_, name := splitLibraryPath(libraryPath)
	node, err := rt.client.UploadLibraryFile(ctx, rt.bearer, libraryPath, name, data)
	if err != nil {
		return "", err
	}
	if !wait {
		return formatUploadResult(node, false, false), nil
	}

	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	for {
		if node.Status == gocoder.LibraryStatusReady || node.Status == gocoder.LibraryStatusFailed {
			return formatUploadResult(node, true, false), nil
		}
		if time.Now().After(deadline) {
			return formatUploadResult(node, true, true), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(uploadPollInterval):
		}
		refreshed, err := rt.client.GetLibraryNode(ctx, rt.bearer, node.ID)
		if err != nil {
			return "", err
		}
		node = refreshed
	}
}

// ---- CLI mode ----

func runCLISearch(args []string) error {
	fs := flag.NewFlagSet("library-plugin search", flag.ContinueOnError)
	query := fs.String("query", "", "search query (required)")
	k := fs.Int("k", 0, "max results (default 10)")
	pathPrefix := fs.String("path", "", "restrict results to paths under this prefix")
	baseURL := fs.String("base-url", "", "override gocoder.org's base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rt, err := newRuntime(runtimeOptions{BaseURL: *baseURL})
	if err != nil {
		return err
	}
	out, err := handleLibrarySearch(context.Background(), rt, *query, *pathPrefix, *k)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func runCLIList(args []string) error {
	fs := flag.NewFlagSet("library-plugin list", flag.ContinueOnError)
	pathPrefix := fs.String("path", "", "folder to list (default: root)")
	baseURL := fs.String("base-url", "", "override gocoder.org's base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rt, err := newRuntime(runtimeOptions{BaseURL: *baseURL})
	if err != nil {
		return err
	}
	out, err := handleLibraryList(context.Background(), rt, *pathPrefix)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func runCLIGet(args []string) error {
	fs := flag.NewFlagSet("library-plugin get", flag.ContinueOnError)
	id := fs.String("id", "", "node id")
	nodePath := fs.String("path", "", "node path, if id is unknown")
	withContent := fs.Bool("content", false, "also fetch the full document content")
	baseURL := fs.String("base-url", "", "override gocoder.org's base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rt, err := newRuntime(runtimeOptions{BaseURL: *baseURL})
	if err != nil {
		return err
	}
	out, err := handleLibraryGet(context.Background(), rt, *id, *nodePath, *withContent)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func runCLIUpload(args []string) error {
	fs := flag.NewFlagSet("library-plugin upload", flag.ContinueOnError)
	file := fs.String("file", "", "local file to upload (.md, .txt, .pdf) (required)")
	libraryPath := fs.String("path", "", "destination path in the library (required)")
	wait := fs.Bool("wait", true, "block until indexing finishes (or timeout)")
	timeout := fs.Int("timeout", 0, "wait timeout in seconds (default 120)")
	baseURL := fs.String("base-url", "", "override gocoder.org's base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	rt, err := newRuntime(runtimeOptions{Directory: wd, BaseURL: *baseURL})
	if err != nil {
		return err
	}
	out, err := handleLibraryUpload(context.Background(), rt, *file, *libraryPath, *wait, *timeout)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
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
			return
		}
		if message.Method == "shutdown" {
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
		// No hooks are declared in the manifest, so the host never sends
		// one; answered defensively rather than left silently unhandled.
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
			Directory string `json:"directory"`
			Worktree  string `json:"worktree"`
		} `json:"input"`
		Options map[string]any `json:"options"`
	}
	if err := json.Unmarshal(message.Params, &params); err != nil {
		return err
	}

	opts := runtimeOptions{
		Directory:     params.Input.Directory,
		Worktree:      params.Input.Worktree,
		BaseURL:       stringOpt(params.Options, "baseURL", ""),
		TopK:          intOpt(params.Options, "topK", 0),
		UploadTimeout: intOpt(params.Options, "uploadTimeout", 0),
	}

	rtMu.Lock()
	pending = &opts
	rtMu.Unlock()

	return reply(message.ID, map[string]any{
		"id":    "library-plugin",
		"hooks": []string{},
		"tools": []map[string]any{
			{
				"name": "library_search",
				"description": "Search the user's personal gocoder.org Library by meaning — notes, uploaded docs and PDFs synced to their account across machines. Returns ranked path:line snippets with the complete matched chunk text, ready to cite. " +
					"This is NOT this project's code: use rag_search or grep for that. Use library_search when the user refers to something they saved, wrote, or uploaded to their library — personal notes, reference docs, research — rather than something in this repository.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{"type": "string", "description": "What to find, in natural language."},
						"k":     map[string]any{"type": "integer", "description": "Max results. Default 10."},
						"path":  map[string]any{"type": "string", "description": "Only return results under this library path prefix."},
					},
					"required": []string{"query"},
				},
			},
			{
				"name":        "library_list",
				"description": "List the folders and files under a path in the user's gocoder.org Library. Read-only. Use to browse the library's structure, or to find a node's id before calling library_get.",
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string", "description": "Folder to list. Default: the library root."}},
				},
			},
			{
				"name":        "library_get",
				"description": "Fetch one library node's metadata (path, type, status) by id or path, optionally its full document content. library_search's results already include the complete matched chunk — use this when you need the whole document, not just the matched chunk, or to check an upload's indexing status.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":          map[string]any{"type": "string", "description": "Node id (the id= shown in library_search/library_list output, e.g. \"68d2...\" — never a path). Preferred over path — cheaper to resolve."},
						"path":        map[string]any{"type": "string", "description": "Node path, e.g. \"docs/notes.md\" — the citation before the colon in library_search output. Use this, not id, if you only have the path. Resolved by walking the tree, one call per path segment."},
						"withContent": map[string]any{"type": "boolean", "description": "Also fetch the full converted Markdown. Only valid for a file node."},
					},
				},
			},
			{
				"name": "library_upload",
				"description": "Upload a local .md/.txt/.pdf file into the user's gocoder.org Library, where it's converted, chunked, and embedded for library_search to find. " +
					"Missing ancestor folders in the destination path are created automatically. Non-destructive: never overwrites or deletes anything already in the library.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"localPath": map[string]any{"type": "string", "description": "Path to the local file to upload (.md, .txt, or .pdf), relative to the project directory or absolute."},
						"path":      map[string]any{"type": "string", "description": "Destination path in the library, e.g. \"research/report.pdf\"."},
						"wait":      map[string]any{"type": "boolean", "description": "Block until indexing finishes (or timeout elapses) before replying. Default true."},
						"timeout":   map[string]any{"type": "integer", "description": "With wait=true, how long to poll before returning the still-processing node's status instead of its final result. Default 120."},
					},
					"required": []string{"localPath", "path"},
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

	rt, err := ensureRuntime()
	if err != nil {
		return reply(message.ID, nil, err)
	}

	ctx := context.Background()
	switch params.Name {
	case "library_search":
		output, err := handleLibrarySearch(ctx, rt, stringOpt(params.Args, "query", ""), stringOpt(params.Args, "path", ""), intOpt(params.Args, "k", 0))
		if err != nil {
			return reply(message.ID, nil, err)
		}
		return reply(message.ID, map[string]any{"title": "library_search", "output": output}, nil)

	case "library_list":
		output, err := handleLibraryList(ctx, rt, stringOpt(params.Args, "path", ""))
		if err != nil {
			return reply(message.ID, nil, err)
		}
		return reply(message.ID, map[string]any{"title": "library_list", "output": output}, nil)

	case "library_get":
		output, err := handleLibraryGet(ctx, rt, stringOpt(params.Args, "id", ""), stringOpt(params.Args, "path", ""), boolOpt(params.Args, "withContent", false))
		if err != nil {
			return reply(message.ID, nil, err)
		}
		return reply(message.ID, map[string]any{"title": "library_get", "output": output}, nil)

	case "library_upload":
		output, err := handleLibraryUpload(ctx, rt, stringOpt(params.Args, "localPath", ""), stringOpt(params.Args, "path", ""), boolOpt(params.Args, "wait", true), intOpt(params.Args, "timeout", 0))
		if err != nil {
			return reply(message.ID, nil, err)
		}
		return reply(message.ID, map[string]any{"title": "library_upload", "output": output}, nil)

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
