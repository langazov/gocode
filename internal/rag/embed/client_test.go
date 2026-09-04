package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbedSingleBatch(t *testing.T) {
	var gotAuth, gotModel string
	var gotInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		gotModel = req.Model
		gotInputs = req.Input
		var resp response
		for i := range req.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Embedding: []float32{float32(i), float32(i) + 0.5}, Index: i})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", "text-embedding-3-small")
	vectors, err := client.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("got Authorization %q, want Bearer sk-test", gotAuth)
	}
	if gotModel != "text-embedding-3-small" {
		t.Errorf("got model %q", gotModel)
	}
	if len(gotInputs) != 2 || gotInputs[0] != "hello" || gotInputs[1] != "world" {
		t.Errorf("got inputs %v", gotInputs)
	}
	if len(vectors) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vectors))
	}
	if vectors[0][0] != 0 || vectors[1][0] != 1 {
		t.Errorf("vectors out of order: %v", vectors)
	}
}

func TestEmbedBatchesLargeInput(t *testing.T) {
	var batchSizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		json.NewDecoder(r.Body).Decode(&req)
		batchSizes = append(batchSizes, len(req.Input))
		var resp response
		for i := range req.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Embedding: []float32{1}, Index: i})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", "m")
	client.BatchSize = 3
	texts := make([]string, 7)
	for i := range texts {
		texts[i] = "t"
	}
	vectors, err := client.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 7 {
		t.Fatalf("got %d vectors, want 7", len(vectors))
	}
	want := []int{3, 3, 1}
	if len(batchSizes) != len(want) {
		t.Fatalf("got batches %v, want sizes %v", batchSizes, want)
	}
	for i, w := range want {
		if batchSizes[i] != w {
			t.Errorf("batch %d: got size %d, want %d", i, batchSizes[i], w)
		}
	}
}

func TestEmbedErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "invalid api key"},
		})
	}))
	defer server.Close()

	client := New(server.URL, "bad-key", "m")
	_, err := client.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestEmbedEmptyInput(t *testing.T) {
	client := New("http://unused.invalid", "k", "m")
	vectors, err := client.Embed(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if vectors != nil {
		t.Errorf("got %v, want nil", vectors)
	}
}
