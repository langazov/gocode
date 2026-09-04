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
	if len(instance.Hooks.Tools) != 2 {
		t.Fatalf("got %d tools, want 2: %+v", len(instance.Hooks.Tools), instance.Hooks.Tools)
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
