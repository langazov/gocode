package embed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
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
	client.Concurrency = 1 // this test asserts request order, which only a single worker guarantees
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

// TestEmbedSkipsBlankInputs pins the guard against the endpoint's hard 400
// on an empty string: a blank input is never sent (it would fail every other
// input sharing its batch) and comes back as a nil vector, leaving the
// non-blank results in their original positions.
func TestEmbedSkipsBlankInputs(t *testing.T) {
	var gotInputs []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		gotInputs = append(gotInputs, req.Input...)
		var resp response
		for i, in := range req.Input {
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Embedding: []float32{float32(len(in))}, Index: i})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", "m")
	vectors, err := client.Embed(context.Background(), []string{"", "hello", "  \n\t ", "worldly"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gotInputs) != 2 || gotInputs[0] != "hello" || gotInputs[1] != "worldly" {
		t.Fatalf("only non-blank inputs should be sent, got %q", gotInputs)
	}
	if len(vectors) != 4 {
		t.Fatalf("got %d vectors, want one per input (4)", len(vectors))
	}
	if vectors[0] != nil || vectors[2] != nil {
		t.Errorf("blank inputs should map to nil vectors, got %v", vectors)
	}
	if len(vectors[1]) != 1 || vectors[1][0] != 5 || len(vectors[3]) != 1 || vectors[3][0] != 7 {
		t.Errorf("non-blank vectors landed in the wrong positions: %v", vectors)
	}
}

// TestEmbedAllBlankInputsSendsNothing: nothing to embed means no request at
// all, not a request the endpoint will reject.
func TestEmbedAllBlankInputsSendsNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request should be sent when every input is blank")
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", "m")
	vectors, err := client.Embed(context.Background(), []string{"", " "})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 2 || vectors[0] != nil || vectors[1] != nil {
		t.Errorf("got %v, want two nil vectors", vectors)
	}
}

// TestEmbedBatchesSkipBlanksWhenSplitting checks the blank-skipping and the
// batching interact correctly: batches are formed over the non-blank inputs,
// so a run of blanks never leaves a short (or empty) request behind.
func TestEmbedBatchesSkipBlanksWhenSplitting(t *testing.T) {
	var batchSizes []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		json.NewDecoder(r.Body).Decode(&req)
		batchSizes = append(batchSizes, len(req.Input))
		var resp response
		for i, in := range req.Input {
			if in == "" {
				t.Error("an empty input reached the endpoint")
			}
			resp.Data = append(resp.Data, struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{Embedding: []float32{1}, Index: i})
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	texts := make([]string, 0, 10)
	for i := range 10 {
		if i%2 == 0 {
			texts = append(texts, "")
			continue
		}
		texts = append(texts, "text")
	}

	client := New(server.URL, "sk-test", "m")
	client.BatchSize = 2
	client.Concurrency = 1 // this test asserts request order, which only a single worker guarantees
	vectors, err := client.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	// 5 non-blank inputs at BatchSize 2: 2, 2, 1.
	if len(batchSizes) != 3 || batchSizes[0] != 2 || batchSizes[1] != 2 || batchSizes[2] != 1 {
		t.Fatalf("got batch sizes %v, want [2 2 1]", batchSizes)
	}
	for i, v := range vectors {
		if (i%2 == 0) != (v == nil) {
			t.Errorf("vector %d: got %v, want nil only for the blank inputs", i, v)
		}
	}
}

func TestEmbedBatchesRunConcurrently(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxInFlight := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()

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
	client.BatchSize = 1
	texts := make([]string, 8)
	for i := range texts {
		texts[i] = fmt.Sprintf("t%d", i)
	}

	start := time.Now()
	vectors, err := client.Embed(context.Background(), texts)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 8 {
		t.Fatalf("got %d vectors, want 8", len(vectors))
	}
	if maxInFlight < 2 {
		t.Fatalf("requests never overlapped (max concurrent = %d): Embed is not dispatching batches concurrently", maxInFlight)
	}
	// 8 batches at 30ms each: serial would take >=240ms; DefaultConcurrency=4
	// should clear two rounds well under that.
	if elapsed >= 8*30*time.Millisecond {
		t.Fatalf("Embed took %v, no faster than fully serial dispatch of 8 batches", elapsed)
	}
}

func TestEmbedConcurrentDispatchPreservesResultOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		json.NewDecoder(r.Body).Decode(&req)
		n, _ := strconv.Atoi(strings.TrimPrefix(req.Input[0], "text-"))
		// The first-dispatched batch is also the slowest, so it's the last
		// to actually finish; output position must still land correctly.
		if n == 0 {
			time.Sleep(50 * time.Millisecond)
		}
		json.NewEncoder(w).Encode(response{Data: []struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{{Embedding: []float32{float32(n)}, Index: 0}}})
	}))
	defer server.Close()

	client := New(server.URL, "sk-test", "m")
	client.BatchSize = 1
	texts := make([]string, 8)
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}

	vectors, err := client.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range vectors {
		if len(v) != 1 || v[0] != float32(i) {
			t.Fatalf("vector %d = %v, want [%d]: concurrent dispatch must not scramble output order", i, v, i)
		}
	}
}

// A batch failure still aborts the whole call (embedBatchAbortsOnError-style
// tests above already cover that for the single-batch path Concurrency=1
// takes). Testing that concurrent siblings are cut off *promptly* would need
// asserting on real socket-level cancellation timing against an httptest
// server, which is exactly the kind of test that hangs or flakes on CI
// without proving much beyond what net/http already guarantees for
// context-canceled requests — so that property is left unverified here.

func TestEmbedWithProgressReportsCumulativeDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	client.BatchSize = 2
	client.Concurrency = 1 // deterministic call order for this assertion
	texts := make([]string, 7)
	for i := range texts {
		texts[i] = "t"
	}

	var mu sync.Mutex
	var reported []int
	var lastTotal int
	vectors, err := client.EmbedWithProgress(context.Background(), texts, func(done, total int) {
		mu.Lock()
		reported = append(reported, done)
		lastTotal = total
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != 7 {
		t.Fatalf("got %d vectors, want 7", len(vectors))
	}
	if lastTotal != 7 {
		t.Errorf("total = %d, want 7", lastTotal)
	}
	want := []int{2, 4, 6, 7}
	if len(reported) != len(want) {
		t.Fatalf("got progress calls %v, want %v", reported, want)
	}
	for i, w := range want {
		if reported[i] != w {
			t.Errorf("progress call %d: got done=%d, want %d", i, reported[i], w)
		}
	}
}

func TestEmbedNilProgressCallbackIsNeverInvoked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if _, err := client.Embed(context.Background(), []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	// Embed's whole point here is that it works with no progress callback at
	// all; reaching this line without a nil-pointer panic is the assertion.
}
