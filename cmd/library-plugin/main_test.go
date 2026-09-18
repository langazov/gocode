package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/plugin"
)

// helperEnv marks the re-executed test binary as the plugin under test — the
// same trick cmd/rag-plugin/main_test.go and internal/plugin/process_test.go
// use to exercise the real subprocess protocol without shipping a second
// binary.
const helperEnv = "LIBRARY_PLUGIN_TEST_HELPER"

// TestHelperPlugin is not a test: when the marker is set, it *is* the plugin
// process, running the real runPlugin() JSON-RPC loop.
func TestHelperPlugin(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process; runs only when re-executed by a test")
	}
	defer os.Exit(0)
	runPlugin()
}

// ---- fake gocoder.org account ----

// withAccount points gocoder.LoadAccount (both in this process, for the unit
// tests below, and in the re-exec'd subprocess, for the JSON-RPC test) at a
// throwaway account backed by a temp XDG_DATA_HOME — never a real signed-in
// account on the machine running the tests.
func withAccount(t *testing.T, dataHome, baseURL, key string) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", dataHome)
	if err := gocoder.SaveAccount(&gocoder.Account{URL: baseURL, UserID: "u1", Email: "u@example.com", Key: key}); err != nil {
		t.Fatal(err)
	}
}

// ---- fake gocoder.org Library server ----

type fakeNode struct {
	gocoder.LibraryNode
	content string
}

// fakeLibraryServer is a minimal stand-in for gocode-infra's
// website/backend/internal/library routes: enough of GET /library/search,
// /library/nodes, /library/nodes/{id}[/content], and POST
// /library/folders, /library/nodes to exercise this plugin's client and
// tool handlers — not a reimplementation of the real service's semantics.
type fakeLibraryServer struct {
	mu     sync.Mutex
	nodes  map[string]*fakeNode // by id
	nextID int

	// uploadedID/pollsUntilReady simulate the async convert->chunk->embed
	// pipeline: the node POST /library/nodes creates reports "converting"
	// until it has been GET at least pollsUntilReady times, then "ready" —
	// exercising library_upload's poll loop rather than resolving instantly.
	uploadedID      string
	polls           int
	pollsUntilReady int
}

func newFakeLibraryServer(pollsUntilReady int) *fakeLibraryServer {
	return &fakeLibraryServer{nodes: map[string]*fakeNode{}, pollsUntilReady: pollsUntilReady}
}

func (s *fakeLibraryServer) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Authorization") != "Bearer gk_test" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad key"}}`))
		return false
	}
	return true
}

func (s *fakeLibraryServer) addNode(n gocoder.LibraryNode, content string) *fakeNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	if n.ID == "" {
		n.ID = "n" + strconv.Itoa(s.nextID)
	}
	fn := &fakeNode{LibraryNode: n, content: content}
	s.nodes[fn.ID] = fn
	return fn
}

func (s *fakeLibraryServer) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /library/search", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		q := r.URL.Query()
		full := q.Get("full") == "true"
		s.mu.Lock()
		defer s.mu.Unlock()
		type hit struct {
			Node        *gocoder.LibraryNode `json:"node"`
			Score       float32              `json:"score"`
			StartLine   int                  `json:"startLine"`
			EndLine     int                  `json:"endLine"`
			HeadingPath []string             `json:"headingPath,omitempty"`
			Snippet     string               `json:"snippet,omitempty"`
			Content     string               `json:"content,omitempty"`
		}
		var hits []hit
		for _, n := range s.nodes {
			if n.Type != gocoder.LibraryTypeFile || !strings.Contains(n.content, q.Get("q")) {
				continue
			}
			node := n.LibraryNode
			h := hit{Node: &node, Score: 0.9, StartLine: 1, EndLine: len(strings.Split(n.content, "\n"))}
			if full {
				h.Content = n.content
			} else {
				h.Snippet = n.content
			}
			hits = append(hits, h)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": hits})
	})

	mux.HandleFunc("GET /library/nodes", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		parent := r.URL.Query().Get("parent")
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []gocoder.LibraryNode
		for _, n := range s.nodes {
			if n.ParentPath == parent {
				out = append(out, n.LibraryNode)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": out})
	})

	mux.HandleFunc("GET /library/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		id := r.PathValue("id")
		s.mu.Lock()
		n, ok := s.nodes[id]
		if ok && id == s.uploadedID {
			s.polls++
			if s.polls >= s.pollsUntilReady {
				n.Status = gocoder.LibraryStatusReady
			}
		}
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such node"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(n.LibraryNode)
	})

	mux.HandleFunc("GET /library/nodes/{id}/content", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		s.mu.Lock()
		n, ok := s.nodes[r.PathValue("id")]
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte(n.content))
	})

	mux.HandleFunc("POST /library/folders", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		for _, n := range s.nodes {
			if n.Path == body.Path {
				s.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"error":{"code":"conflict","message":"a node already exists at that path"}}`))
				return
			}
		}
		s.mu.Unlock()
		parent, name := splitLibraryPath(body.Path)
		fn := s.addNode(gocoder.LibraryNode{Path: body.Path, ParentPath: parent, Name: name, Type: gocoder.LibraryTypeFolder}, "")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(fn.LibraryNode)
	})

	mux.HandleFunc("POST /library/nodes", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		path := r.FormValue("path")
		file, _, err := r.FormFile("file")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer file.Close()
		data, _ := io.ReadAll(file)

		parent, name := splitLibraryPath(path)
		fn := s.addNode(gocoder.LibraryNode{
			Path: path, ParentPath: parent, Name: name, Type: gocoder.LibraryTypeFile,
			SourceKind: "md", SizeBytes: int64(len(data)), Status: gocoder.LibraryStatusConverting,
		}, string(data))
		s.mu.Lock()
		s.uploadedID = fn.ID
		s.polls = 0
		s.mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(fn.LibraryNode)
	})

	return mux
}

// ---- unit tests: handleLibrary* against the fake server ----

func testRuntime(t *testing.T, srv *httptest.Server) *runtime {
	t.Helper()
	withAccount(t, t.TempDir(), srv.URL, "gk_test")
	rt, err := newRuntime(runtimeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestSearchReturnsFullChunkFromServer(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	fake.addNode(gocoder.LibraryNode{ID: "n1", Path: "notes.md", Name: "notes.md", Type: gocoder.LibraryTypeFile, Status: gocoder.LibraryStatusReady}, "findable content here")

	rt := testRuntime(t, srv)
	out, err := handleLibrarySearch(context.Background(), rt, "findable", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "notes.md") || !strings.Contains(out, "findable content here") {
		t.Fatalf("output = %q", out)
	}
}

func TestSearchFallsBackToSlicingFullContentWhenServerOmitsContent(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	content := "line one\nfindable line two\nline three"
	fake.addNode(gocoder.LibraryNode{ID: "n1", Path: "notes.md", Name: "notes.md", Type: gocoder.LibraryTypeFile, Status: gocoder.LibraryStatusReady}, content)

	rt := testRuntime(t, srv)

	// Simulate an old server that ignores full=true and always returns a
	// (here, untruncated but still Content-less) Snippet: intercept the
	// hit after SearchLibrary and clear Content the way handleLibrarySearch
	// would see it from a pre-`full` deployment, forcing the fallback path.
	hits, err := rt.client.SearchLibrary(context.Background(), rt.bearer, "findable", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("expected a hit")
	}
	hits[0].Content = ""
	hits[0].StartLine, hits[0].EndLine = 2, 2

	// Exercise the same slicing handleLibrarySearch does, directly against
	// GetLibraryContent + sliceLines, since the fake server always answers
	// full=true and we want to test the fallback branch specifically.
	full, err := rt.client.GetLibraryContent(context.Background(), rt.bearer, hits[0].Node.ID)
	if err != nil {
		t.Fatal(err)
	}
	sliced := sliceLines(full, hits[0].StartLine, hits[0].EndLine)
	if sliced != "findable line two" {
		t.Fatalf("sliced = %q", sliced)
	}
}

func TestSliceLinesClampsOutOfRange(t *testing.T) {
	content := "a\nb\nc"
	cases := []struct {
		start, end int
		want       string
	}{
		{1, 3, "a\nb\nc"},
		{0, 1, "a"},
		{2, 100, "b\nc"},
		{5, 6, ""},
	}
	for _, tc := range cases {
		if got := sliceLines(content, tc.start, tc.end); got != tc.want {
			t.Errorf("sliceLines(%d,%d) = %q, want %q", tc.start, tc.end, got, tc.want)
		}
	}
}

func TestListFormatsNodes(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	fake.addNode(gocoder.LibraryNode{ID: "f1", Path: "docs", Name: "docs", Type: gocoder.LibraryTypeFolder}, "")
	fake.addNode(gocoder.LibraryNode{ID: "n1", Path: "readme.md", Name: "readme.md", Type: gocoder.LibraryTypeFile, Status: gocoder.LibraryStatusReady, SizeBytes: 42}, "hi")

	rt := testRuntime(t, srv)
	out, err := handleLibraryList(context.Background(), rt, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "docs") || !strings.Contains(out, "readme.md") {
		t.Fatalf("output = %q", out)
	}
}

func TestGetByIDAndByPath(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	fake.addNode(gocoder.LibraryNode{ID: "f1", Path: "docs", Name: "docs", Type: gocoder.LibraryTypeFolder}, "")
	fake.addNode(gocoder.LibraryNode{ID: "n1", Path: "docs/readme.md", ParentPath: "docs", Name: "readme.md", Type: gocoder.LibraryTypeFile, Status: gocoder.LibraryStatusReady}, "hello world")

	rt := testRuntime(t, srv)

	byID, err := handleLibraryGet(context.Background(), rt, "n1", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(byID, "docs/readme.md") || !strings.Contains(byID, "hello world") {
		t.Fatalf("byID = %q", byID)
	}

	byPath, err := handleLibraryGet(context.Background(), rt, "", "docs/readme.md", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(byPath, "docs/readme.md") {
		t.Fatalf("byPath = %q", byPath)
	}

	if _, err := handleLibraryGet(context.Background(), rt, "f1", "", true); err == nil {
		t.Fatal("expected an error requesting withContent on a folder")
	}
}

func TestUploadCreatesAncestorFoldersAndWaitsForReady(t *testing.T) {
	fake := newFakeLibraryServer(2) // "ready" only after the 2nd status poll
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	dir := t.TempDir()
	localFile := filepath.Join(dir, "report.md")
	if err := os.WriteFile(localFile, []byte("# Report\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := testRuntime(t, srv)
	rt.opts.UploadTimeout = 30 // generous relative to uploadPollInterval

	out, err := handleLibraryUpload(context.Background(), rt, localFile, "research/2024/report.md", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ready") {
		t.Fatalf("expected upload to reach ready, got %q", out)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	var sawResearch, sawResearch2024 bool
	for _, n := range fake.nodes {
		if n.Path == "research" && n.Type == gocoder.LibraryTypeFolder {
			sawResearch = true
		}
		if n.Path == "research/2024" && n.Type == gocoder.LibraryTypeFolder {
			sawResearch2024 = true
		}
	}
	if !sawResearch || !sawResearch2024 {
		t.Fatalf("ancestor folders not created: %+v", fake.nodes)
	}
}

func TestUploadRejectsUnsupportedExtension(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	dir := t.TempDir()
	localFile := filepath.Join(dir, "image.png")
	if err := os.WriteFile(localFile, []byte("not really a png"), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := testRuntime(t, srv)
	if _, err := handleLibraryUpload(context.Background(), rt, localFile, "assets/image.png", true, 5); err == nil {
		t.Fatal("expected an unsupported-extension error")
	}
}

func TestUploadRejectsOversizedFile(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	dir := t.TempDir()
	localFile := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(localFile, make([]byte, maxUploadBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}

	rt := testRuntime(t, srv)
	if _, err := handleLibraryUpload(context.Background(), rt, localFile, "big.txt", true, 5); err == nil {
		t.Fatal("expected an oversized-file error")
	}
}

func TestNewRuntimeErrorsWithoutAccount(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if _, err := newRuntime(runtimeOptions{}); err == nil {
		t.Fatal("expected an error with no gocoder.org account stored")
	}
}

func TestEnsureAncestorFoldersToleratesExisting(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	rt := testRuntime(t, srv)

	if err := ensureAncestorFolders(context.Background(), rt, "docs/notes.md"); err != nil {
		t.Fatal(err)
	}
	// Calling again for a sibling under the same, now-existing "docs" folder
	// must not fail even though "docs" already exists (409 tolerated).
	if err := ensureAncestorFolders(context.Background(), rt, "docs/more.md"); err != nil {
		t.Fatal(err)
	}
}

// ---- JSON-RPC integration test: the real production entrypoint end to end ----

func spawnLibraryPlugin(t *testing.T, srv *httptest.Server, dataHome string) *plugin.Instance {
	t.Helper()
	dir := t.TempDir()
	instance, err := plugin.Spawn(context.Background(), "library-plugin", plugin.SpawnConfig{
		Command: []string{os.Args[0], "-test.run=TestHelperPlugin"},
		Dir:     dir,
		Env: []string{
			helperEnv + "=1",
			"XDG_DATA_HOME=" + dataHome,
		},
		Stderr: io.Discard,
	}, plugin.Input{Directory: dir, Worktree: dir}, plugin.Options{"baseURL": srv.URL}, func(string) {})
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

func TestLibraryPluginSearchOverJSONRPC(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	fake.addNode(gocoder.LibraryNode{ID: "n1", Path: "notes.md", Name: "notes.md", Type: gocoder.LibraryTypeFile, Status: gocoder.LibraryStatusReady}, "findable content here")

	dataHome := t.TempDir()
	withAccount(t, dataHome, srv.URL, "gk_test")

	instance := spawnLibraryPlugin(t, srv, dataHome)
	if instance.ID != "library-plugin" {
		t.Errorf("ID = %q, want library-plugin", instance.ID)
	}
	if len(instance.Hooks.Tools) != 4 {
		t.Fatalf("got %d tools, want 4: %+v", len(instance.Hooks.Tools), instance.Hooks.Tools)
	}

	searchTool := findTool(t, instance, "library_search")
	result, err := searchTool.Execute(context.Background(), map[string]any{"query": "findable"}, plugin.ToolContext{
		SessionID: "s1", Directory: dataHome, Worktree: dataHome,
	})
	if err != nil {
		t.Fatalf("library_search: %v", err)
	}
	if !strings.Contains(result.Output, "notes.md") || !strings.Contains(result.Output, "findable content here") {
		t.Fatalf("output = %q", result.Output)
	}
}
