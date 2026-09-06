package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/plugin"
)

// helperEnv marks the re-executed test binary as the plugin under test —
// the same trick internal/plugin/process_test.go uses to exercise a real
// subprocess protocol without shipping a second binary.
const helperEnv = "RAG_PLUGIN_TEST_HELPER"

// TestHelperPlugin is not a test: when the marker is set, it *is* the plugin
// process. It runs the real runPlugin() JSON-RPC loop — this is an
// integration test of the actual production entry point, not a stand-in.
func TestHelperPlugin(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process; runs only when re-executed by a test")
	}
	defer os.Exit(0)
	runPlugin()
}

// fakeKeywords/fakeEmbed mirror internal/rag/rag_test.go's bag-of-words
// stand-in for a real embeddings API (kept local: that helper is unexported
// in a different package).
var fakeKeywords = []string{"apple", "banana"}

func fakeEmbed(text string) []float32 {
	lower := strings.ToLower(text)
	vec := make([]float32, len(fakeKeywords))
	for i, kw := range fakeKeywords {
		vec[i] = float32(strings.Count(lower, kw)) + 0.01
	}
	return vec
}

func fakeEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}
		var resp struct {
			Data []item `json:"data"`
		}
		for i, text := range req.Input {
			resp.Data = append(resp.Data, item{Embedding: fakeEmbed(text), Index: i})
		}
		json.NewEncoder(w).Encode(resp)
	}))
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func spawnRagPlugin(t *testing.T, projectRoot string, options plugin.Options) *plugin.Instance {
	t.Helper()
	instance, err := plugin.Spawn(context.Background(), "rag-plugin", plugin.SpawnConfig{
		Command: []string{os.Args[0], "-test.run=TestHelperPlugin"},
		Dir:     projectRoot,
		Env: []string{
			helperEnv + "=1",
			"OPENAI_API_KEY=test-key",
			"GOCODE_DISABLE_MODELS_FETCH=true",
		},
		Stderr: io.Discard,
	}, plugin.Input{Directory: projectRoot, Worktree: projectRoot}, options, func(string) {})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Instance.closer is unexported (only internal/plugin's own tests can
	// reach it directly); Host.Close is the exported teardown path for
	// everyone else, so route through a throwaway Host.
	host := plugin.NewHost(func(string, string, error) {})
	host.Add(instance)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	return instance
}

// spawnRagPluginWithoutCredentials is spawnRagPlugin with the API key and
// every provider hint withheld, so any code path that tries to resolve an
// embeddings provider fails. It is how the maintenance tools are shown to
// need no credentials, rather than merely happening not to use the ones the
// test supplied.
func spawnRagPluginWithoutCredentials(t *testing.T, projectRoot string, options plugin.Options) *plugin.Instance {
	t.Helper()
	instance, err := plugin.Spawn(context.Background(), "rag-plugin", plugin.SpawnConfig{
		Command: []string{os.Args[0], "-test.run=TestHelperPlugin"},
		Dir:     projectRoot,
		Env: []string{
			helperEnv + "=1",
			"GOCODE_DISABLE_MODELS_FETCH=true",
			// An empty home keeps the resolver away from a real auth.json on
			// the machine running the tests.
			"HOME=" + t.TempDir(),
			"GOCODE_DATA=" + t.TempDir(),
		},
		Stderr: io.Discard,
	}, plugin.Input{Directory: projectRoot, Worktree: projectRoot}, options, func(string) {})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	host := plugin.NewHost(func(string, string, error) {})
	host.Add(instance)
	t.Cleanup(func() { _ = host.Close(context.Background()) })
	return instance
}

func findTool(t *testing.T, instance *plugin.Instance, name string) plugin.Tool {
	t.Helper()
	for _, tl := range instance.Hooks.Tools {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("tool %q not declared in manifest; got %+v", name, instance.Hooks.Tools)
	return plugin.Tool{}
}

// TestRagPluginIndexAndSearchOverJSONRPC drives the real rag-plugin binary
// (re-exec'd as this test binary) through the actual JSON-RPC-over-stdio
// protocol the host uses in production (internal/plugin.Spawn), against a
// fixture project and a fake embeddings server — end to end: handshake,
// rag_index, rag_search.
func TestRagPluginIndexAndSearchOverJSONRPC(t *testing.T) {
	server := fakeEmbeddingServer(t)
	defer server.Close()

	root := t.TempDir()
	writeFile(t, root, "fruit.md", "apple apple apple is a fruit\n")
	writeFile(t, root, "vegetable.md", "a plain vegetable, no keywords here\n")

	instance := spawnRagPlugin(t, root, plugin.Options{
		"embeddingBaseURL": server.URL,
		"dbPath":           filepath.Join(t.TempDir(), "rag.db"),
	})

	if instance.ID != "rag-plugin" {
		t.Errorf("ID = %q, want rag-plugin", instance.ID)
	}
	if len(instance.Hooks.Tools) != 5 {
		t.Fatalf("got %d tools, want 5: %+v", len(instance.Hooks.Tools), instance.Hooks.Tools)
	}

	indexTool := findTool(t, instance, "rag_index")
	indexResult, err := indexTool.Execute(context.Background(), map[string]any{}, plugin.ToolContext{
		SessionID: "s1", Directory: root, Worktree: root,
	})
	if err != nil {
		t.Fatalf("rag_index: %v", err)
	}
	var summary struct {
		FilesScanned  int `json:"filesScanned"`
		ChunksAdded   int `json:"chunksAdded"`
		ChunksUpdated int `json:"chunksUpdated"`
		ChunksRemoved int `json:"chunksRemoved"`
	}
	if err := json.Unmarshal([]byte(indexResult.Output), &summary); err != nil {
		t.Fatalf("decode rag_index output %q: %v", indexResult.Output, err)
	}
	if summary.FilesScanned != 2 || summary.ChunksAdded != 2 {
		t.Fatalf("unexpected index summary: %+v (raw %q)", summary, indexResult.Output)
	}

	searchTool := findTool(t, instance, "rag_search")
	searchResult, err := searchTool.Execute(context.Background(), map[string]any{"query": "apple", "k": float64(2)}, plugin.ToolContext{
		SessionID: "s1", Directory: root, Worktree: root,
	})
	if err != nil {
		t.Fatalf("rag_search: %v", err)
	}
	if !strings.Contains(searchResult.Output, "fruit.md") {
		t.Errorf("expected fruit.md to rank for an apple query, got %q", searchResult.Output)
	}
	if strings.Index(searchResult.Output, "fruit.md") > strings.Index(searchResult.Output, "vegetable.md") &&
		strings.Contains(searchResult.Output, "vegetable.md") {
		t.Errorf("expected fruit.md to rank before vegetable.md, got %q", searchResult.Output)
	}
}

// TestRagPluginCLIIndex exercises the one-shot `rag-plugin index` CLI path
// (bypassing the JSON-RPC/host-timeout path entirely) by invoking the real
// binary as a plain subprocess.
func TestRagPluginCLIIndex(t *testing.T) {
	server := fakeEmbeddingServer(t)
	defer server.Close()

	root := t.TempDir()
	writeFile(t, root, "a.md", "banana banana\n")

	name := "rag-plugin-cli"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build rag-plugin: %v\n%s", err, out)
	}

	dbPath := filepath.Join(t.TempDir(), "rag.db")
	cmd := exec.Command(binary, "index",
		"-root", root,
		"-project", "p1",
		"-db", dbPath,
		"-embedding-base-url", server.URL,
	)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY=test-key", "GOCODE_DISABLE_MODELS_FETCH=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rag-plugin index: %v\n%s", err, out)
	}
	var summary struct {
		FilesScanned int `json:"filesScanned"`
		ChunksAdded  int `json:"chunksAdded"`
	}
	if err := json.Unmarshal(out, &summary); err != nil {
		t.Fatalf("decode CLI output %q: %v", out, err)
	}
	if summary.FilesScanned != 1 || summary.ChunksAdded != 1 {
		t.Fatalf("unexpected CLI summary: %+v (raw %q)", summary, out)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("expected db at %s: %v", dbPath, err)
	}
}

// TestRagPluginHandshakeDefersStoreOpen pins the reason the runtime is built
// lazily: the host blocks boot on this handshake, and store.Open reads the
// entire vector DB into memory — seconds, for a large index, paid by every
// `gocode tui`/`serve` start whether or not the session searches anything.
//
// The store directory is the observable proof: it does not exist until a
// tool call actually needs it.
func TestRagPluginHandshakeDefersStoreOpen(t *testing.T) {
	server := fakeEmbeddingServer(t)
	defer server.Close()

	root := t.TempDir()
	writeFile(t, root, "fruit.md", "apple apple apple is a fruit\n")
	dbPath := filepath.Join(t.TempDir(), "rag.db")

	instance := spawnRagPlugin(t, root, plugin.Options{
		"embeddingBaseURL": server.URL,
		"dbPath":           dbPath,
	})
	// The manifest is static, so the handshake still declares every tool.
	if len(instance.Hooks.Tools) != 5 {
		t.Fatalf("got %d tools, want 5: %+v", len(instance.Hooks.Tools), instance.Hooks.Tools)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("the handshake opened the store at %s (stat err = %v)", dbPath, err)
	}

	if _, err := findTool(t, instance, "rag_index").Execute(context.Background(), map[string]any{}, plugin.ToolContext{
		SessionID: "s1", Directory: root, Worktree: root,
	}); err != nil {
		t.Fatalf("rag_index: %v", err)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("the first tool call should have opened the store: %v", err)
	}
}

// ---- Maintenance ----

// buildRagPlugin compiles the real binary once per test that needs the CLI,
// mirroring TestRagPluginCLIIndex's approach: the maintenance commands are
// argv dispatch plus flag parsing, so exercising them any other way would
// skip the part most likely to be wrong.
func buildRagPlugin(t *testing.T) string {
	t.Helper()
	name := "rag-plugin-cli"
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build rag-plugin: %v\n%s", err, out)
	}
	return binary
}

// seedIndex runs a real `rag-plugin index` against a fake embeddings server
// and returns the database path, so the maintenance commands are tested
// against a database the indexer itself wrote rather than a hand-built one.
func seedIndex(t *testing.T, binary, dbPath, projectID string, files map[string]string) {
	t.Helper()
	server := fakeEmbeddingServer(t)
	defer server.Close()

	root := t.TempDir()
	for rel, content := range files {
		writeFile(t, root, rel, content)
	}
	cmd := exec.Command(binary, "index",
		"-root", root,
		"-project", projectID,
		"-db", dbPath,
		"-embedding-base-url", server.URL,
	)
	cmd.Env = append(os.Environ(), "OPENAI_API_KEY=test-key", "GOCODE_DISABLE_MODELS_FETCH=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed index: %v\n%s", err, out)
	}
}

func runRagPlugin(t *testing.T, binary string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Env = append(os.Environ(), "GOCODE_DISABLE_MODELS_FETCH=true")
	// Detach stdin so a command that reaches a confirmation prompt fails
	// fast instead of blocking the test forever.
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRagPluginCLIListAndClean(t *testing.T) {
	binary := buildRagPlugin(t)
	dbPath := filepath.Join(t.TempDir(), "rag.db")
	seedIndex(t, binary, dbPath, "p1", map[string]string{"a.md": "apple\n"})
	seedIndex(t, binary, dbPath, "p2", map[string]string{"b.md": "banana\n"})

	out, err := runRagPlugin(t, binary, "list", "-db", dbPath)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "p1") || !strings.Contains(out, "p2") {
		t.Fatalf("list should name both projects, got:\n%s", out)
	}

	// A dry run must report and change nothing.
	out, err = runRagPlugin(t, binary, "clean", "-db", dbPath, "-project", "p1", "-dry-run")
	if err != nil {
		t.Fatalf("clean -dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("dry run should say so, got:\n%s", out)
	}
	if out, _ := runRagPlugin(t, binary, "list", "-db", dbPath); !strings.Contains(out, "p1") {
		t.Fatalf("the dry run deleted p1:\n%s", out)
	}

	out, err = runRagPlugin(t, binary, "clean", "-db", dbPath, "-project", "p1", "-yes")
	if err != nil {
		t.Fatalf("clean: %v\n%s", err, out)
	}

	out, err = runRagPlugin(t, binary, "list", "-db", dbPath)
	if err != nil {
		t.Fatalf("list after clean: %v\n%s", err, out)
	}
	if strings.Contains(out, "p1") {
		t.Errorf("p1 should be gone, got:\n%s", out)
	}
	if !strings.Contains(out, "p2") {
		t.Errorf("cleaning p1 removed p2 as well, got:\n%s", out)
	}
}

// TestRagPluginCleanRequiresConfirmation pins the guard on the irreversible
// path: with no answer available and no -yes, the command must fail loudly
// rather than quietly doing nothing (or, worse, proceeding).
func TestRagPluginCleanRequiresConfirmation(t *testing.T) {
	binary := buildRagPlugin(t)
	dbPath := filepath.Join(t.TempDir(), "rag.db")
	seedIndex(t, binary, dbPath, "p1", map[string]string{"a.md": "apple\n"})

	out, err := runRagPlugin(t, binary, "clean", "-db", dbPath, "-project", "p1")
	if err == nil {
		t.Fatalf("expected a non-zero exit without -yes, got:\n%s", out)
	}
	if !strings.Contains(out, "-yes") {
		t.Errorf("the error should name -yes, got:\n%s", out)
	}
	if out, _ := runRagPlugin(t, binary, "list", "-db", dbPath); !strings.Contains(out, "p1") {
		t.Fatalf("the refused clean deleted p1 anyway:\n%s", out)
	}
}

func TestRagPluginCLICleanPathScoped(t *testing.T) {
	binary := buildRagPlugin(t)
	dbPath := filepath.Join(t.TempDir(), "rag.db")
	seedIndex(t, binary, dbPath, "p1", map[string]string{
		"keep/a.md": "apple\n",
		"drop/b.md": "banana\n",
	})

	out, err := runRagPlugin(t, binary, "clean", "-db", dbPath, "-project", "p1", "-path", "drop", "-yes")
	if err != nil {
		t.Fatalf("clean -path: %v\n%s", err, out)
	}
	if !strings.Contains(out, "removed 1 chunk(s)") {
		t.Errorf("expected one chunk removed, got:\n%s", out)
	}

	out, err = runRagPlugin(t, binary, "list", "-db", dbPath, "-json")
	if err != nil {
		t.Fatalf("list -json: %v\n%s", err, out)
	}
	var projects []struct {
		ProjectID string `json:"projectId"`
		Chunks    int    `json:"chunks"`
	}
	if err := json.Unmarshal([]byte(out), &projects); err != nil {
		t.Fatalf("decode %q: %v", out, err)
	}
	if len(projects) != 1 || projects[0].Chunks != 1 {
		t.Fatalf("expected p1 to keep exactly one chunk, got %+v", projects)
	}
}

// TestRagPluginVacuumReclaimsOrphans builds the drift that actually occurs
// in the wild — a collection whose manifest rows were lost to a concurrent
// whole-file manifest rewrite — and checks vacuum reclaims it, refuses to
// without confirmation, and leaves the healthy project alone.
func TestRagPluginVacuumReclaimsOrphans(t *testing.T) {
	binary := buildRagPlugin(t)
	dbPath := filepath.Join(t.TempDir(), "rag.db")
	seedIndex(t, binary, dbPath, "healthy", map[string]string{"a.md": "apple\n"})
	seedIndex(t, binary, dbPath, "orphan", map[string]string{"b.md": "banana\n"})

	// Drop the orphan's manifest rows, leaving its collection on disk.
	manifestPath := filepath.Join(dbPath, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]struct {
		ProjectID   string `json:"projectId"`
		Path        string `json:"path"`
		ContentHash string `json:"contentHash"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for key, entry := range manifest {
		if entry.ProjectID == "orphan" {
			delete(manifest, key)
		}
	}
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runRagPlugin(t, binary, "vacuum", "-db", dbPath, "-dry-run")
	if err != nil {
		t.Fatalf("vacuum -dry-run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "orphan") {
		t.Fatalf("dry run should name the orphan, got:\n%s", out)
	}

	if out, err := runRagPlugin(t, binary, "vacuum", "-db", dbPath); err == nil {
		t.Fatalf("expected a non-zero exit without -yes, got:\n%s", out)
	}

	out, err = runRagPlugin(t, binary, "vacuum", "-db", dbPath, "-yes")
	if err != nil {
		t.Fatalf("vacuum: %v\n%s", err, out)
	}

	out, err = runRagPlugin(t, binary, "list", "-db", dbPath)
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if strings.Contains(out, "orphan") {
		t.Errorf("the orphan survived vacuum:\n%s", out)
	}
	if !strings.Contains(out, "healthy") {
		t.Errorf("vacuum removed the healthy project:\n%s", out)
	}

	// A second pass has nothing left to do.
	out, err = runRagPlugin(t, binary, "vacuum", "-db", dbPath, "-dry-run")
	if err != nil {
		t.Fatalf("second vacuum: %v\n%s", err, out)
	}
	if !strings.Contains(out, "nothing to vacuum") {
		t.Errorf("expected a clean bill of health, got:\n%s", out)
	}
}

// TestMaintenanceToolsWorkWithoutCredentials pins the two-tier runtime
// split. rag_status must answer with no embeddings provider resolvable at
// all: an index worth cleaning up is often one whose provider config or API
// key has since gone away, and requiring credentials to delete stored bytes
// would lock the user out of exactly that case.
func TestMaintenanceToolsWorkWithoutCredentials(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "fruit.md", "apple apple\n")
	dbPath := filepath.Join(t.TempDir(), "rag.db")

	instance := spawnRagPluginWithoutCredentials(t, root, plugin.Options{
		"dbPath": dbPath,
		// Deliberately no embeddingBaseURL: resolving a provider would have
		// to reach the network, which is what this test asserts never
		// happens on the maintenance path.
	})

	ctx := context.Background()
	toolCtx := plugin.ToolContext{SessionID: "s1", Directory: root, Worktree: root}

	out, err := findTool(t, instance, "rag_status").Execute(ctx, map[string]any{}, toolCtx)
	if err != nil {
		t.Fatalf("rag_status without credentials: %v", err)
	}
	if !strings.Contains(out.Output, dbPath) {
		t.Errorf("rag_status should report the database path, got %q", out.Output)
	}

	// rag_index, by contrast, legitimately needs the provider and must still
	// fail — the split must not have quietly made indexing credential-free.
	if _, err := findTool(t, instance, "rag_index").Execute(ctx, map[string]any{}, toolCtx); err == nil {
		t.Error("rag_index should still require a resolvable embeddings provider")
	}
}

// TestRagCleanRequiresConfirm pins the tool-side guard: without confirm the
// call must be refused, and the index must survive it.
func TestRagCleanRequiresConfirm(t *testing.T) {
	server := fakeEmbeddingServer(t)
	defer server.Close()

	root := t.TempDir()
	writeFile(t, root, "fruit.md", "apple apple\n")
	dbPath := filepath.Join(t.TempDir(), "rag.db")

	instance := spawnRagPlugin(t, root, plugin.Options{
		"embeddingBaseURL": server.URL,
		"dbPath":           dbPath,
	})
	ctx := context.Background()
	toolCtx := plugin.ToolContext{SessionID: "s1", Directory: root, Worktree: root}

	if _, err := findTool(t, instance, "rag_index").Execute(ctx, map[string]any{}, toolCtx); err != nil {
		t.Fatalf("rag_index: %v", err)
	}

	clean := findTool(t, instance, "rag_clean")
	if _, err := clean.Execute(ctx, map[string]any{"scope": "project"}, toolCtx); err == nil {
		t.Error("rag_clean without confirm should be refused")
	}
	if _, err := clean.Execute(ctx, map[string]any{"scope": "path", "confirm": true}, toolCtx); err == nil {
		t.Error("rag_clean scope=path without a path should be refused")
	}
	if _, err := clean.Execute(ctx, map[string]any{"scope": "nonsense", "confirm": true}, toolCtx); err == nil {
		t.Error("rag_clean with an unknown scope should be refused")
	}

	// A dry run needs no confirm and must delete nothing.
	out, err := clean.Execute(ctx, map[string]any{"scope": "project", "dryRun": true}, toolCtx)
	if err != nil {
		t.Fatalf("rag_clean dry run: %v", err)
	}
	if !strings.Contains(out.Output, "dry run") {
		t.Errorf("expected a dry-run report, got %q", out.Output)
	}

	status, err := findTool(t, instance, "rag_status").Execute(ctx, map[string]any{}, toolCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.Output, root) {
		t.Fatalf("the refused calls deleted the project: %q", status.Output)
	}

	// And with confirm it actually deletes.
	if _, err := clean.Execute(ctx, map[string]any{"scope": "project", "confirm": true}, toolCtx); err != nil {
		t.Fatalf("rag_clean confirmed: %v", err)
	}
	status, err = findTool(t, instance, "rag_status").Execute(ctx, map[string]any{}, toolCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status.Output, "no projects indexed") {
		t.Errorf("expected an empty database, got %q", status.Output)
	}
}
