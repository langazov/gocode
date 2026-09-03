package modelsdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const fixture = `{
  "anthropic": {
    "id": "anthropic",
    "name": "Anthropic",
    "env": ["ANTHROPIC_API_KEY"],
    "api": "https://api.anthropic.com/v1/",
    "models": {
      "claude-sonnet-4-5": {
        "id": "claude-sonnet-4-5",
        "name": "Claude Sonnet 4.5",
        "release_date": "2025-09-29",
        "attachment": true,
        "reasoning": true,
        "temperature": true,
        "tool_call": true,
        "interleaved": true,
        "cost": {"input": 3, "output": 15, "cache_read": 0.3, "cache_write": 3.75},
        "limit": {"context": 200000, "output": 64000}
      },
      "claude-opus-4-1": {
        "id": "claude-opus-4-1",
        "name": "Claude Opus 4.1",
        "release_date": "2025-08-05",
        "attachment": true,
        "reasoning": false,
        "temperature": true,
        "tool_call": true,
        "interleaved": {"field": "reasoning"},
        "limit": {"context": 200000, "output": 32000}
      }
    }
  },
  "openai": {
    "id": "openai",
    "name": "OpenAI",
    "env": ["OPENAI_API_KEY"],
    "models": {
      "gpt-5": {
        "id": "gpt-5",
        "name": "GPT-5",
        "release_date": "2025-08-07",
        "attachment": true,
        "reasoning": true,
        "temperature": false,
        "tool_call": true,
        "interleaved": "reasoning",
        "reasoning_options": [{"type": "effort", "values": ["minimal", "low", "medium", "high"]}],
        "limit": {"context": 400000, "output": 128000}
      }
    }
  }
}`

func testService(t *testing.T) *Service {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.json" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("missing User-Agent header")
		}
		w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GOCODE_MODELS_URL", srv.URL)
	return New()
}

func TestGetFetchesAndParses(t *testing.T) {
	service := testService(t)
	catalog, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(catalog))
	}
	model := catalog["anthropic"].Models["claude-sonnet-4-5"]
	if model.Name != "Claude Sonnet 4.5" {
		t.Fatalf("unexpected model name: %s", model.Name)
	}
	if model.Cost == nil || model.Cost.Input != 3 || model.Cost.Output != 15 {
		t.Fatalf("unexpected cost: %+v", model.Cost)
	}
	if model.Limit.Context != 200000 || model.Limit.Output != 64000 {
		t.Fatalf("unexpected limit: %+v", model.Limit)
	}
	if model.Interleaved == nil || !model.Interleaved.Enabled {
		t.Fatalf("expected interleaved enabled")
	}
}

// TestGetIsSingleFlightUnderConcurrency exercises the channel-based cache
// cell (Service.cache) that replaced a sync.Mutex: concurrent first-load
// Get calls must take turns owning the entry rather than racing each other
// into populate(), so the network is hit once no matter how many goroutines
// call Get before anything is cached. Run with -race to also confirm the
// channel handoff has no data race on the returned Catalog.
func TestGetIsSingleFlightUnderConcurrency(t *testing.T) {
	var calls int32
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(20 * time.Millisecond) // widen the race window
		w.Write([]byte(fixture))
	}))
	defer srv.Close()
	t.Setenv("GOCODE_MODELS_URL", srv.URL)
	service := New()

	const goroutines = 20
	results := make(chan Catalog, goroutines)
	errs := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			catalog, err := service.Get(context.Background())
			if err != nil {
				errs <- err
				return
			}
			results <- catalog
		}()
	}
	for i := 0; i < goroutines; i++ {
		select {
		case err := <-errs:
			t.Fatal(err)
		case catalog := <-results:
			if len(catalog) != 2 {
				t.Errorf("catalog = %d providers, want 2", len(catalog))
			}
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("server hit %d times by %d concurrent Get calls, want 1 (single-flight)", got, goroutines)
	}
}

func TestInterleavedUnion(t *testing.T) {
	service := testService(t)
	catalog, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	boolForm := catalog["anthropic"].Models["claude-sonnet-4-5"].Interleaved
	if boolForm.Field != "" {
		t.Fatalf("bool form should have empty field, got %q", boolForm.Field)
	}
	objectForm := catalog["anthropic"].Models["claude-opus-4-1"].Interleaved
	if objectForm.Field != "reasoning" {
		t.Fatalf("expected field reasoning, got %q", objectForm.Field)
	}
	stringForm := catalog["openai"].Models["gpt-5"].Interleaved
	if stringForm.Field != "reasoning" || !stringForm.Enabled {
		t.Fatalf("expected string form reasoning, got %+v", stringForm)
	}
}

func TestCacheWrittenToDisk(t *testing.T) {
	service := testService(t)
	if _, err := service.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(service.Filepath); err != nil {
		t.Fatalf("expected cache file at %s: %v", service.Filepath, err)
	}
}

func TestLoadFromDiskWithoutNetwork(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_MODELS_URL", "http://127.0.0.1:1")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	service := New()
	if err := os.MkdirAll(filepath.Dir(service.Filepath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service.Filepath, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("expected 2 providers from disk cache, got %d", len(catalog))
	}
}

func TestCorruptCacheIsDroppedAndRefetched(t *testing.T) {
	service := testService(t)
	if err := os.MkdirAll(filepath.Dir(service.Filepath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service.Filepath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("expected refetch after corrupt cache, got %d providers", len(catalog))
	}
}

// TestDisableModelsFetch asserts the embedded snapshot serves the catalog when
// fetching is switched off. Disabling the fetch means "do not go to the
// network", not "have no models" — before the snapshot existed this path
// returned an empty catalog, leaving a fresh offline install with no model
// list at all.
func TestDisableModelsFetch(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_MODELS_URL", "http://127.0.0.1:1")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "true")
	catalog, err := New().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog["anthropic"]; !ok {
		t.Fatalf("expected the embedded snapshot when fetch disabled, got %d providers", len(catalog))
	}
}

// TestUnreachableSourceFallsBackToSnapshot covers the other offline path: the
// fetch is allowed but the source is unreachable.
func TestUnreachableSourceFallsBackToSnapshot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_MODELS_URL", "http://127.0.0.1:1")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	catalog, err := New().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog["anthropic"]
	if !ok {
		t.Fatalf("expected the embedded snapshot for an unreachable source, got %d providers", len(catalog))
	}
	if len(entry.Env) == 0 || len(entry.Models) == 0 {
		t.Fatalf("snapshot entry is not usable: env=%v models=%d", entry.Env, len(entry.Models))
	}
}

func TestSnapshotDecodes(t *testing.T) {
	catalog, err := Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) < 100 {
		t.Fatalf("snapshot looks truncated: %d providers", len(catalog))
	}
	for _, id := range []string{"anthropic", "openai", "google", "amazon-bedrock", "github-copilot"} {
		if _, ok := catalog[id]; !ok {
			t.Errorf("snapshot is missing provider %q", id)
		}
	}
}

func TestModelsPathOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-models.json")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCODE_MODELS_PATH", path)
	t.Setenv("GOCODE_MODELS_URL", "http://127.0.0.1:1")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	catalog, err := New().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("expected 2 providers from GOCODE_MODELS_PATH, got %d", len(catalog))
	}
}

func TestRefreshSkipsWhenFresh(t *testing.T) {
	service := testService(t)
	if _, err := service.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(service.Filepath)
	if err != nil {
		t.Fatal(err)
	}
	// not forced and file is fresh: no rewrite
	if err := service.Refresh(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(service.Filepath)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("expected no rewrite for fresh cache")
	}
}

// TestStartBackgroundRefreshFetchesImmediatelyOnStartup guards against a
// time.Ticker's missing first tick: packages/core/src/models-dev.ts uses
// `Effect.repeat(Schedule.spaced(...))`, which runs the refresh once right
// away and only then starts spacing further runs, so a stale on-disk
// catalog gets refreshed at the start of every gocode run rather than up to
// 60 minutes into one (or never, for a one-shot `gocode run`).
func TestStartBackgroundRefreshFetchesImmediatelyOnStartup(t *testing.T) {
	service := testService(t)
	// Seed a stale cache directly, bypassing Get, so nothing has "just"
	// fetched it — StartBackgroundRefresh is what must notice it is stale.
	if err := os.MkdirAll(filepath.Dir(service.Filepath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(service.Filepath, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Hour)
	if err := os.Chtimes(service.Filepath, stale, stale); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.StartBackgroundRefresh(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for {
		info, err := os.Stat(service.Filepath)
		if err == nil && info.ModTime().After(stale) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("StartBackgroundRefresh did not refresh a stale cache at startup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStartBackgroundRefreshSkipsWhenDisabled: offline mode must not spawn a
// goroutine that hits the network anyway, immediately or on a timer.
func TestStartBackgroundRefreshSkipsWhenDisabled(t *testing.T) {
	service := testService(t)
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "1")

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Write([]byte(fixture))
	}))
	defer srv.Close()
	service.Source = srv.URL

	if err := os.MkdirAll(filepath.Dir(service.Filepath), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Hour)
	if err := os.WriteFile(service.Filepath, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(service.Filepath, stale, stale); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.StartBackgroundRefresh(ctx)
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("fetched %d times with GOCODE_DISABLE_MODELS_FETCH set, want 0", got)
	}
}

func TestRefreshForce(t *testing.T) {
	service := testService(t)
	if _, err := service.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Refresh(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	catalog, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("expected catalog after forced refresh, got %d", len(catalog))
	}
}
