package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/rag/embed"
	"github.com/langazov/gocode-go/internal/rag/store"
)

// fakeEmbeddingServer is a bag-of-words stand-in for a real embeddings API:
// each dimension counts one keyword's occurrences in the input, so a query
// containing "apple" embeds close to a chunk containing "apple" and far from
// one that doesn't — enough structure to test ranking without a live model.
var fakeKeywords = []string{"apple", "banana", "carrot"}

func fakeEmbed(text string) []float32 {
	lower := strings.ToLower(text)
	vec := make([]float32, len(fakeKeywords))
	for i, kw := range fakeKeywords {
		vec[i] = float32(strings.Count(lower, kw)) + 0.01 // avoid an all-zero vector
	}
	return vec
}

func fakeEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
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

type harness struct {
	store    *store.Store
	embedder *embed.Client
	root     string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	server := fakeEmbeddingServer(t)
	t.Cleanup(server.Close)

	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	return &harness{
		store:    s,
		embedder: embed.New(server.URL, "test-key", "fake-model"),
		root:     t.TempDir(),
	}
}

func TestIndexThenSearchRanksRelevantChunkFirst(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	writeFile(t, h.root, "fruit.md", "apple apple apple is a fruit\n")
	writeFile(t, h.root, "vegetable.md", "carrot is a vegetable\n")

	idx := &Indexer{Store: h.store, Embedder: h.embedder, ProjectID: "p1"}
	summary, err := idx.Index(ctx, IndexOptions{Root: h.root, ChunkLines: 60, ChunkOverlap: 5})
	if err != nil {
		t.Fatal(err)
	}
	if summary.FilesScanned != 2 || summary.ChunksAdded != 2 || summary.ChunksUpdated != 0 || summary.ChunksRemoved != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	search := &Searcher{Store: h.store, Embedder: h.embedder, ProjectID: "p1"}
	hits, err := search.Search(ctx, SearchOptions{Query: "apple", K: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].Path != "fruit.md" {
		t.Errorf("top hit: got %s, want fruit.md", hits[0].Path)
	}
	if hits[0].Score <= hits[1].Score {
		t.Errorf("hits not ranked by descending score: %v", hits)
	}

	formatted := FormatHits(hits)
	if !strings.Contains(formatted, "fruit.md:1-1") {
		t.Errorf("formatted output missing citation header: %q", formatted)
	}
}

func TestReindexSkipsUnchangedChunksAndEmbedsOnlyChanged(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	writeFile(t, h.root, "a.md", "apple\n")
	writeFile(t, h.root, "b.md", "banana\n")

	idx := &Indexer{Store: h.store, Embedder: h.embedder, ProjectID: "p1"}
	if _, err := idx.Index(ctx, IndexOptions{Root: h.root, ChunkLines: 60, ChunkOverlap: 5}); err != nil {
		t.Fatal(err)
	}

	// Change only b.md.
	writeFile(t, h.root, "b.md", "banana banana\n")
	summary, err := idx.Index(ctx, IndexOptions{Root: h.root, ChunkLines: 60, ChunkOverlap: 5})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ChunksAdded != 0 {
		t.Errorf("got ChunksAdded=%d, want 0 (no new files)", summary.ChunksAdded)
	}
	if summary.ChunksUpdated != 1 {
		t.Errorf("got ChunksUpdated=%d, want 1 (only b.md changed)", summary.ChunksUpdated)
	}
	if summary.ChunksRemoved != 0 {
		t.Errorf("got ChunksRemoved=%d, want 0", summary.ChunksRemoved)
	}
}

func TestReindexAfterFileDeletionRemovesItsChunks(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	writeFile(t, h.root, "a.md", "apple\n")
	writeFile(t, h.root, "b.md", "banana\n")

	idx := &Indexer{Store: h.store, Embedder: h.embedder, ProjectID: "p1"}
	if _, err := idx.Index(ctx, IndexOptions{Root: h.root, ChunkLines: 60, ChunkOverlap: 5}); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(h.root, "b.md")); err != nil {
		t.Fatal(err)
	}
	summary, err := idx.Index(ctx, IndexOptions{Root: h.root, ChunkLines: 60, ChunkOverlap: 5})
	if err != nil {
		t.Fatal(err)
	}
	if summary.ChunksRemoved != 1 {
		t.Fatalf("got ChunksRemoved=%d, want 1", summary.ChunksRemoved)
	}

	search := &Searcher{Store: h.store, Embedder: h.embedder, ProjectID: "p1"}
	hits, err := search.Search(ctx, SearchOptions{Query: "banana", K: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, hit := range hits {
		if hit.Path == "b.md" {
			t.Errorf("b.md should no longer be searchable after deletion, found %+v", hit)
		}
	}
}

func TestSearchRespectsPathPrefix(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	writeFile(t, h.root, "src/a.go", "apple\n")
	writeFile(t, h.root, "docs/a.md", "apple\n")

	idx := &Indexer{Store: h.store, Embedder: h.embedder, ProjectID: "p1"}
	if _, err := idx.Index(ctx, IndexOptions{Root: h.root, ChunkLines: 60, ChunkOverlap: 5}); err != nil {
		t.Fatal(err)
	}

	search := &Searcher{Store: h.store, Embedder: h.embedder, ProjectID: "p1"}
	hits, err := search.Search(ctx, SearchOptions{Query: "apple", K: 10, PathPrefix: "src/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "src/a.go" {
		t.Fatalf("expected only src/a.go, got %+v", hits)
	}
}

func TestSearchRequiresQuery(t *testing.T) {
	h := newHarness(t)
	search := &Searcher{Store: h.store, Embedder: h.embedder, ProjectID: "p1"}
	if _, err := search.Search(context.Background(), SearchOptions{Query: "  "}); err == nil {
		t.Fatal("expected an error for a blank query")
	}
}
