package modelsdev

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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
	t.Setenv("OPENCODE_MODELS_PATH", "")
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "")
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
	t.Setenv("OPENCODE_MODELS_URL", srv.URL)
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
	t.Setenv("OPENCODE_MODELS_PATH", "")
	t.Setenv("OPENCODE_MODELS_URL", "http://127.0.0.1:1")
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "")
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

func TestDisableModelsFetch(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("OPENCODE_MODELS_PATH", "")
	t.Setenv("OPENCODE_MODELS_URL", "http://127.0.0.1:1")
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "true")
	catalog, err := New().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 0 {
		t.Fatalf("expected empty catalog when fetch disabled, got %d", len(catalog))
	}
}

func TestModelsPathOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-models.json")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_MODELS_PATH", path)
	t.Setenv("OPENCODE_MODELS_URL", "http://127.0.0.1:1")
	t.Setenv("OPENCODE_DISABLE_MODELS_FETCH", "")
	catalog, err := New().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 2 {
		t.Fatalf("expected 2 providers from OPENCODE_MODELS_PATH, got %d", len(catalog))
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
