package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/llm/openai"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

func baseOf(t *testing.T, client any) string {
	t.Helper()
	c, ok := client.(*openai.Client)
	if !ok {
		t.Fatalf("expected an openai-protocol client, got %T", client)
	}
	return c.BaseURL
}

// The bug this guards: models.dev omits `api` for the 26 providers whose SDK
// carries the endpoint, and an openai-protocol client with no base URL
// defaults to api.openai.com. Switching to any of those models posted that
// provider's key to OpenAI, and a 401 was the only symptom.
func TestSdkProvidersDoNotRouteToOpenAI(t *testing.T) {
	for _, tc := range []struct {
		providerID string
		env        string
		want       string
	}{
		{"mistral", "MISTRAL_API_KEY", "https://api.mistral.ai/v1"},
		{"groq", "GROQ_API_KEY", "https://api.groq.com/openai/v1"},
		{"cerebras", "CEREBRAS_API_KEY", "https://api.cerebras.ai/v1"},
		{"xai", "XAI_API_KEY", "https://api.x.ai/v1"},
		{"perplexity", "PERPLEXITY_API_KEY", "https://api.perplexity.ai"},
	} {
		t.Run(tc.providerID, func(t *testing.T) {
			t.Setenv(tc.env, "secret")
			client, err := FromConfig(context.Background(), tc.providerID, &config.Config{})
			if err != nil {
				t.Fatalf("FromConfig: %v", err)
			}
			got := baseOf(t, client)
			if got == openai.DefaultBaseURL {
				t.Fatalf("%s resolved to OpenAI's endpoint — its key would be posted to another company's API",
					tc.providerID)
			}
			if got != tc.want {
				t.Fatalf("%s base = %q, want %q", tc.providerID, got, tc.want)
			}
		})
	}
}

// Only OpenAI itself gets OpenAI's endpoint.
func TestOpenAIKeepsItsOwnEndpoint(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	client, err := FromConfig(context.Background(), "openai", &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := baseOf(t, client); got != openai.DefaultBaseURL {
		t.Fatalf("openai base = %q, want %q", got, openai.DefaultBaseURL)
	}
}

// A provider whose catalog entry carries its own api wins on that.
func TestCatalogEndpointIsUsed(t *testing.T) {
	t.Setenv("ZAI_CODING_PLAN_API_KEY", "secret")
	client, err := FromConfig(context.Background(), "zai-coding-plan", &config.Config{})
	if err != nil {
		t.Skipf("catalog unavailable: %v", err)
	}
	if got := baseOf(t, client); !strings.Contains(got, "z.ai") {
		t.Fatalf("zai-coding-plan base = %q, want its own endpoint", got)
	}
}

// An unknown endpoint must be reported, not guessed at.
func TestUnknownEndpointIsAnError(t *testing.T) {
	t.Setenv("COHERE_API_KEY", "secret")
	_, err := FromConfig(context.Background(), "cohere", &config.Config{})
	if err == nil {
		t.Fatal("a provider whose endpoint is unknown must error, not fall back to OpenAI")
	}
	for _, want := range []string{"cohere", "baseURL"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error should name %q and say how to fix it, got: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("the error must not leak the key")
	}
}

// --- resolveBaseURL's layering ---------------------------------------------

func TestResolveBaseURLPrecedence(t *testing.T) {
	entry := modelsdev.Provider{API: "https://catalog.example/v1", NPM: "@ai-sdk/groq"}

	// config wins over everything.
	cfg := &config.Provider{}
	cfg.Options.BaseURL = "https://override.example/v1"
	if got, _ := resolveBaseURL("groq", cfg, entry); got != "https://override.example/v1" {
		t.Fatalf("config override should win, got %q", got)
	}

	// then the env override — which used to be reachable only for a handful
	// of ids, and only when the provider was not declared in config.
	t.Setenv("GROQ_BASE_URL", "https://env.example/v1")
	if got, _ := resolveBaseURL("groq", &config.Provider{}, entry); got != "https://env.example/v1" {
		t.Fatalf("env override should beat the catalog, got %q", got)
	}
	t.Setenv("GROQ_BASE_URL", "")

	// then the catalog, then the SDK table.
	if got, _ := resolveBaseURL("groq", nil, entry); got != "https://catalog.example/v1" {
		t.Fatalf("catalog api should be used, got %q", got)
	}
	if got, _ := resolveBaseURL("groq", nil, modelsdev.Provider{NPM: "@ai-sdk/groq"}); got != "https://api.groq.com/openai/v1" {
		t.Fatalf("the SDK table should fill in for a missing catalog api, got %q", got)
	}

	// A local provider that is in no catalog at all.
	if got, ok := resolveBaseURL("ollama", nil, modelsdev.Provider{}); !ok || got != "http://localhost:11434/v1" {
		t.Fatalf("ollama = %q (%v), want its local endpoint", got, ok)
	}

	// And nothing known stays unknown.
	if _, ok := resolveBaseURL("something-new", nil, modelsdev.Provider{NPM: "@vendor/own-sdk"}); ok {
		t.Fatal("an unrecognised SDK must not resolve to an endpoint")
	}
}

// A config entry that supplies only an apiKey must not lose the endpoint the
// catalog knows — the old code skipped every default whenever the provider
// appeared in config at all.
func TestConfigApiKeyDoesNotDropTheEndpoint(t *testing.T) {
	cfg := &config.Config{Provider: map[string]config.Provider{"groq": {}}}
	entry := cfg.Provider["groq"]
	entry.Options.APIKey = "secret"
	cfg.Provider["groq"] = entry

	client, err := FromConfig(context.Background(), "groq", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := baseOf(t, client); got == openai.DefaultBaseURL || got == "" {
		t.Fatalf("groq base = %q, want its own endpoint", got)
	}
}
