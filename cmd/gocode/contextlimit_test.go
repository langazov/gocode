package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/modelsdev"
)

// The context limits pinned here come from vendor documentation, not the
// (stale or absent) models.dev entries:
//
//   - GLM-5.3-Flash: Z.AI developer docs, "Context Length: 1M"
//     (https://docs.z.ai/guides/llm/glm-5.3-flash)
//   - Claude Opus 5: Anthropic docs, "Claude Opus 5 ... have a 1M-token
//     context window as both the default and the maximum"
//     (https://platform.claude.com/docs/en/build-with-claude/context-windows)
//
// Both are 1M-window models. Budgeting their compaction against the 200k
// static default compacted them at ~180k tokens — a sixth of what they hold.
const glmFlashContext = 1_000_000
const opus5Context = 1_000_000

// longContextCatalog is a fixture shaped like the models.dev catalog with the
// documented limits for the two models, plus a genuinely 200k model for
// contrast.
const longContextCatalog = `{
  "zhipuai": {
    "id": "zhipuai",
    "npm": "@ai-sdk/openai-compatible",
    "api": "https://open.bigmodel.cn/api/paas/v4",
    "env": ["ZHIPU_API_KEY"],
    "name": "Zhipu",
    "models": {
      "glm-5.3-flash": {"id": "glm-5.3-flash", "name": "GLM-5.3 Flash", "release_date": "2026-01-01", "attachment": false, "reasoning": true, "temperature": true, "tool_call": true, "limit": {"context": 1000000, "output": 131072}}
    }
  },
  "anthropic": {
    "id": "anthropic",
    "npm": "@ai-sdk/anthropic",
    "env": ["ANTHROPIC_API_KEY"],
    "name": "Anthropic",
    "models": {
      "claude-opus-5": {"id": "claude-opus-5", "name": "Claude Opus 5", "release_date": "2026-01-01", "attachment": true, "reasoning": true, "temperature": true, "tool_call": true, "limit": {"context": 1000000, "output": 128000}},
      "claude-sonnet-4-5": {"id": "claude-sonnet-4-5", "name": "Claude Sonnet 4.5", "release_date": "2025-01-01", "attachment": true, "reasoning": true, "temperature": true, "tool_call": true, "limit": {"context": 200000, "output": 64000}}
    }
  }
}`

func longContextCatalogService(t *testing.T) *modelsdev.Service {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(longContextCatalog))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GOCODE_MODELS_URL", srv.URL)
	return modelsdev.New()
}

// TestContextLimitResolverReadsDocumentedLimits pins the catalog values the
// compaction budget resolves for GLM-5.3-Flash and Claude Opus 5. If one of
// these fails, either the fixture drifted from the vendor docs or the
// resolver stopped reading limit.context — both would silently move where
// compaction fires.
func TestContextLimitResolverReadsDocumentedLimits(t *testing.T) {
	resolver := contextLimitResolver(longContextCatalogService(t))

	if limit, ok := resolver("zhipuai", "glm-5.3-flash"); !ok || limit != glmFlashContext {
		t.Fatalf("glm-5.3-flash context limit = %d (ok=%v), want %d per Z.AI docs", limit, ok, glmFlashContext)
	}
	if limit, ok := resolver("anthropic", "claude-opus-5"); !ok || limit != opus5Context {
		t.Fatalf("claude-opus-5 context limit = %d (ok=%v), want %d per Anthropic docs", limit, ok, opus5Context)
	}
	// Contrast: a model the docs give 200k still resolves 200k, not the
	// static default by accident.
	if limit, ok := resolver("anthropic", "claude-sonnet-4-5"); !ok || limit != 200_000 {
		t.Fatalf("claude-sonnet-4-5 context limit = %d (ok=%v), want 200000", limit, ok)
	}
	// An unknown model reports false so the static fallback applies.
	if limit, ok := resolver("anthropic", "model-not-in-any-docs"); ok {
		t.Fatalf("unknown model resolved limit %d, want false", limit)
	}
}

// TestLiveCatalogHasDocumentedLimits checks the catalog actually downloaded
// from models.opencode.ai, the same fetch a real boot makes. The fixture
// tests above pin what we believe; this one pins what models.dev serves. It
// skips when the network or the endpoint is unavailable, matching the
// live-endpoint test convention in internal/provider/endpoint_test.go.
func TestLiveCatalogHasDocumentedLimits(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	t.Setenv("GOCODE_MODELS_URL", "")
	service := modelsdev.New()
	catalog, err := service.Get(context.Background())
	if err != nil {
		t.Skipf("catalog unavailable: %v", err)
	}

	// The primary entries gocode itself ships as coding-plan providers.
	if glm, ok := catalog["zhipuai"].Models["glm-5.3-flash"]; ok {
		if glm.Limit.Context < 1_000_000 {
			t.Errorf("zhipuai/glm-5.3-flash context = %v, want ≥ 1M per Z.AI docs", glm.Limit.Context)
		}
		if glm.Limit.Output != 131_072 {
			t.Errorf("zhipuai/glm-5.3-flash output = %v, want 131072", glm.Limit.Output)
		}
	} else {
		t.Log("zhipuai/glm-5.3-flash not in live catalog")
	}
	if opus, ok := catalog["anthropic"].Models["claude-opus-5"]; ok {
		if opus.Limit.Context != 1_000_000 {
			t.Errorf("anthropic/claude-opus-5 context = %v, want 1M per Anthropic docs", opus.Limit.Context)
		}
		if opus.Limit.Output != 128_000 {
			t.Errorf("anthropic/claude-opus-5 output = %v, want 128000", opus.Limit.Output)
		}
	} else {
		t.Log("anthropic/claude-opus-5 not in live catalog")
	}

	// The coding-plan overlays the CLI actually recommends.
	for _, providerID := range []string{"zai-coding-plan", "zhipuai-coding-plan", "opencode"} {
		if glm, ok := catalog[providerID].Models["glm-5.3-flash"]; ok && glm.Limit.Context < 1_000_000 {
			t.Errorf("%s/glm-5.3-flash context = %v, want ≥ 1M", providerID, glm.Limit.Context)
		}
	}
}

// TestBootedRunnerResolvesLongContextModels checks the wiring end to end: a
// stack booted against this catalog must hand its runner a resolver that
// returns the documented 1M windows for both models, so the proactive
// compaction check budgets against the real window rather than the 200k
// static default.
func TestBootedRunnerResolvesLongContextModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(longContextCatalog), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("GOCODE_MODELS_PATH", path)
	t.Setenv("GOCODE_CONFIG_CONTENT", `{"model":"zhipuai/glm-5.3-flash"}`)
	t.Setenv("GOCODE_AUTH_CONTENT", `{"zhipuai":{"type":"api","key":"k"},"anthropic":{"type":"api","key":"k"}}`)

	// Booted on the config default (GLM-5.3-Flash).
	stack := bootStackT(t, context.Background(), "")
	resolver := stack.Runner.ContextLimitResolver
	if resolver == nil {
		t.Fatal("booted runner has no ContextLimitResolver; compaction budgets against the static default")
	}
	if limit, ok := resolver("zhipuai", "glm-5.3-flash"); !ok || limit != glmFlashContext {
		t.Fatalf("booted runner: glm-5.3-flash = %d (ok=%v), want %d", limit, ok, glmFlashContext)
	}

	// Booted again on Opus 5 via the --model flag path.
	stack = bootStackT(t, context.Background(), "anthropic/claude-opus-5")
	resolver = stack.Runner.ContextLimitResolver
	if resolver == nil {
		t.Fatal("booted runner has no ContextLimitResolver; compaction budgets against the static default")
	}
	if limit, ok := resolver("anthropic", "claude-opus-5"); !ok || limit != opus5Context {
		t.Fatalf("booted runner: claude-opus-5 = %d (ok=%v), want %d", limit, ok, opus5Context)
	}
}
