package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/llm/anthropic"
	"github.com/langazov/gocode-go/internal/llm/openai"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

const fallbackFixture = `{
  "minimax": {
    "id": "minimax",
    "name": "MiniMax",
    "env": ["MINIMAX_API_KEY"],
    "api": "https://api.minimax.io/anthropic/v1",
    "models": {
      "MiniMax-M3": {"id": "MiniMax-M3", "name": "MiniMax M3", "release_date": "2026-01-01", "attachment": false, "reasoning": true, "temperature": true, "tool_call": true, "limit": {"context": 200000, "output": 100000}}
    }
  },
  "minimax-coding-plan": {
    "id": "minimax-coding-plan",
    "name": "MiniMax Coding Plan",
    "env": ["MINIMAX_API_KEY"],
    "npm": "@ai-sdk/anthropic",
    "api": "https://api.minimax.io/anthropic/v1",
    "models": {
      "MiniMax-M3": {"id": "MiniMax-M3", "name": "MiniMax M3", "release_date": "2026-01-01", "attachment": false, "reasoning": true, "temperature": true, "tool_call": true, "limit": {"context": 200000, "output": 100000}},
      "MiniMax-M2.7": {"id": "MiniMax-M2.7", "name": "MiniMax M2.7", "release_date": "2026-01-01", "attachment": false, "reasoning": true, "temperature": true, "tool_call": true, "limit": {"context": 200000, "output": 100000}},
      "MiniMax-M2.1": {"id": "MiniMax-M2.1", "name": "MiniMax M2.1", "release_date": "2026-01-01", "attachment": false, "reasoning": true, "temperature": true, "tool_call": true, "limit": {"context": 200000, "output": 100000}}
    }
  },
  "groq": {
    "id": "groq",
    "name": "Groq",
    "env": ["GROQ_API_KEY"],
    "models": {
      "llama-3": {"id": "llama-3", "name": "Llama 3", "release_date": "2024-01-01", "attachment": false, "reasoning": false, "temperature": true, "tool_call": true, "limit": {"context": 100000, "output": 8000}}
    }
  },
  "zai-coding-plan": {
    "id": "zai-coding-plan",
    "name": "ZAI Coding Plan",
    "env": ["ZHIPU_API_KEY"],
    "npm": "@ai-sdk/openai-compatible",
    "api": "https://api.z.ai/api/coding/paas/v4",
    "models": {
      "glm-5.3": {"id": "glm-5.3", "name": "GLM 5.3", "release_date": "2026-01-01", "attachment": false, "reasoning": true, "temperature": false, "tool_call": true, "limit": {"context": 200000, "output": 100000}}
    }
  }
}`

func fallbackEnv(t *testing.T, authContent string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	t.Setenv("GOCODE_MODELS_URL", "http://127.0.0.1:1")
	t.Setenv("GOCODE_AUTH_CONTENT", authContent)
	// Seed the catalog cache at the service's computed path (custom source
	// URLs hash the filename).
	catalog := modelsdev.New()
	os.MkdirAll(filepath.Dir(catalog.Filepath), 0o755)
	os.WriteFile(catalog.Filepath, []byte(fallbackFixture), 0o644)
	os.Unsetenv("MINIMAX_API_KEY")
	os.Unsetenv("GROQ_API_KEY")
	os.Unsetenv("ZHIPU_API_KEY")
}

// The user scenario: config asks for "minimax/MiniMax-M3" (no key) while
// auth.json has "minimax-coding-plan" — fallback must prefer the same-family
// provider and keep the configured model.
func TestFallbackPrefersProviderFamily(t *testing.T) {
	fallbackEnv(t, `{"minimax-coding-plan":{"type":"api","key":"plan-key"},"groq":{"type":"api","key":"gq"}}`)

	providerID, modelID, ok := Fallback(context.Background(), "minimax", "MiniMax-M3", nil)
	if !ok {
		t.Fatal("expected fallback provider")
	}
	if providerID != "minimax-coding-plan" {
		t.Fatalf("expected family match minimax-coding-plan, got %s", providerID)
	}
	if modelID != "MiniMax-M3" {
		t.Fatalf("configured model must be preserved, got %s", modelID)
	}
}

func TestFallbackUsesAnyAuthenticatedProvider(t *testing.T) {
	fallbackEnv(t, `{"groq":{"type":"api","key":"gq"}}`)
	providerID, _, ok := Fallback(context.Background(), "minimax", "", nil)
	if !ok || providerID != "groq" {
		t.Fatalf("expected groq fallback via auth, got %s ok=%v", providerID, ok)
	}
}

func TestFallbackUsesEnvCredentials(t *testing.T) {
	fallbackEnv(t, `{}`)
	t.Setenv("GROQ_API_KEY", "env-key")
	providerID, _, ok := Fallback(context.Background(), "minimax", "", nil)
	if !ok || providerID != "groq" {
		t.Fatalf("expected groq fallback via env, got %s ok=%v", providerID, ok)
	}
}

func TestFallbackNoneAvailable(t *testing.T) {
	fallbackEnv(t, `{}`)
	_, _, ok := Fallback(context.Background(), "minimax", "", nil)
	if ok {
		t.Fatal("expected no fallback without credentials")
	}
}

// FromConfig must use the catalog's api URL and npm protocol for catalog
// providers without a config entry (the zai-coding-plan 401 bug).
func TestFromConfigUsesCatalogEndpoint(t *testing.T) {
	fallbackEnv(t, `{"zai-coding-plan":{"type":"api","key":"sk-cp-key"}}`)

	client, err := FromConfig(context.Background(), "zai-coding-plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	openaiClient, ok := client.(*openai.Client)
	if !ok {
		t.Fatalf("expected openai-compatible client, got %T", client)
	}
	if openaiClient.BaseURL != "https://api.z.ai/api/coding/paas/v4" {
		t.Fatalf("expected catalog api URL, got %q", openaiClient.BaseURL)
	}
}

// Anthropic-protocol catalog providers (npm @ai-sdk/anthropic) use the
// anthropic client with the catalog URL.
func TestFromConfigCatalogAnthropicProtocol(t *testing.T) {
	fallbackEnv(t, `{"minimax-coding-plan":{"type":"api","key":"plan-key"}}`)

	client, err := FromConfig(context.Background(), "minimax-coding-plan", nil)
	if err != nil {
		t.Fatal(err)
	}
	anthropicClient, ok := client.(*anthropic.Client)
	if !ok {
		t.Fatalf("expected anthropic client, got %T", client)
	}
	if anthropicClient.BaseURL != "https://api.minimax.io/anthropic/v1" {
		t.Fatalf("expected catalog api URL, got %q", anthropicClient.BaseURL)
	}
}
