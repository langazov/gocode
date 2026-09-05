package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	start := time.Now()
	_, err := client.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("a non-retryable error should fail immediately, took %v", elapsed)
	}
}

func TestEmbedRetriesRateLimitThenSucceeds(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"message": "Rate limit reached, please try again in 0.01s."},
			})
			return
		}
		var req request
		json.NewDecoder(r.Body).Decode(&req)
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
	vectors, err := client.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 {
		t.Fatalf("got %d vectors, want 1", len(vectors))
	}
	if attempts != 3 {
		t.Errorf("got %d attempts, want 3 (2 rate limited, 1 success)", attempts)
	}
}

func TestEmbedRetriesServerErrorThenSucceeds(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("upstream overloaded"))
			return
		}
		var req request
		json.NewDecoder(r.Body).Decode(&req)
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
	client.MaxRetries = 2
	vectors, err := client.Embed(context.Background(), []string{"hi"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 1 {
		t.Fatalf("got %d vectors, want 1", len(vectors))
	}
}

func TestEmbedGivesUpAfterMaxRetries(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "still limited"},
		})
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", "m")
	client.MaxRetries = 2
	_, err := client.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if attempts != 3 {
		t.Errorf("got %d attempts, want 3 (1 initial + 2 retries)", attempts)
	}
}

func TestEmbedRetryDisabledByNegativeMaxRetries(t *testing.T) {
	var attempts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", "m")
	client.MaxRetries = -1
	_, err := client.Embed(context.Background(), []string{"hi"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if attempts != 1 {
		t.Errorf("got %d attempts, want 1 (retries disabled)", attempts)
	}
}

func TestEmbedRetryAbortsOnContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := New(server.URL, "sk-test", "m")

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := client.Embed(ctx, []string{"hi"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("canceling the context should abort the wait immediately, took %v", elapsed)
	}
}

func TestParseRetryAfterFromMessage(t *testing.T) {
	d, ok := parseRetryAfterFromMessage("Rate limit reached. Please try again in 1.728s.")
	if !ok {
		t.Fatal("expected a match")
	}
	if d != 1728*time.Millisecond {
		t.Errorf("got %v, want 1.728s", d)
	}
}

func TestEmbedClampsOversizedInput(t *testing.T) {
	var gotInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		json.NewDecoder(r.Body).Decode(&req)
		gotInputs = req.Input
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
	client.MaxInputChars = 10
	huge := strings.Repeat("x", 100)
	if _, err := client.Embed(context.Background(), []string{"short", huge}); err != nil {
		t.Fatal(err)
	}
	if len(gotInputs) != 2 || gotInputs[0] != "short" {
		t.Fatalf("short input should pass through unchanged: %v", gotInputs)
	}
	if len(gotInputs[1]) != 10 {
		t.Errorf("got clamped length %d, want 10", len(gotInputs[1]))
	}
}

func TestEmbedClampDisabledByNegativeMaxInputChars(t *testing.T) {
	var gotInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		json.NewDecoder(r.Body).Decode(&req)
		gotInputs = req.Input
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
	client.MaxInputChars = -1
	huge := strings.Repeat("x", 100)
	if _, err := client.Embed(context.Background(), []string{huge}); err != nil {
		t.Fatal(err)
	}
	if len(gotInputs[0]) != 100 {
		t.Errorf("clamping should be disabled, got length %d, want 100", len(gotInputs[0]))
	}
}

func TestClampInputDropsTrailingPartialRune(t *testing.T) {
	s := "ab€" // "€" is the 3-byte UTF-8 sequence E2 82 AC
	got := clampInput(s, 4)
	if got != "ab" {
		t.Fatalf("got %q (bytes %v), want %q: a cut mid-rune must not leave invalid UTF-8", got, []byte(got), "ab")
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
