package embed

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/provider"
)

// Config selects the embeddings endpoint and model. All fields are optional;
// zero values fall back to a plain OpenAI setup resolved through the usual
// provider credential chain (models.dev catalog env[] -> {ID}_API_KEY ->
// auth.json), the same chain every chat provider in this Go port uses.
type Config struct {
	// Provider is a models.dev provider id, e.g. "openai". Defaults to
	// "openai".
	Provider string
	// Model is the embedding model id. Defaults to "text-embedding-3-small".
	Model string
	// BaseURL overrides the resolved endpoint outright, for a provider or
	// model not in the models.dev catalog.
	BaseURL string
}

func (c Config) withDefaults() Config {
	if c.Provider == "" {
		c.Provider = "openai"
	}
	if c.Model == "" {
		c.Model = "text-embedding-3-small"
	}
	return c
}

// Resolve builds a ready-to-use Client from a Config. It reuses this
// project's existing credential resolution (provider.Service.ResolveAPIKey:
// catalog env[] -> {ID}_API_KEY -> auth.json) unchanged, since that is a
// stable public entry point on internal/provider. Endpoint resolution is its
// own small, self-contained copy of the same override order
// (env -> catalog api -> a plain default) rather than a call into
// internal/provider's unexported resolveBaseURL: that helper's full table
// also covers chat-only gateways (ollama, lmstudio, openrouter, ...) with no
// bearing on an embeddings endpoint, so duplicating just the two or three
// lines this actually needs is more honest than reusing — or exporting —
// logic scoped to a different problem.
func Resolve(ctx context.Context, cfg Config, providers *provider.Service) (*Client, error) {
	cfg = cfg.withDefaults()

	baseURL := cfg.BaseURL
	if baseURL == "" {
		if providers == nil {
			return nil, fmt.Errorf("rag: no embeddingBaseURL configured and no provider catalog available")
		}
		catalog, err := providers.Catalog(ctx)
		if err != nil {
			return nil, fmt.Errorf("rag: load provider catalog: %w", err)
		}
		resolved, ok := resolveBaseURL(cfg.Provider, catalog[cfg.Provider])
		if !ok {
			return nil, fmt.Errorf("rag: could not resolve a base URL for provider %q; set embeddingBaseURL explicitly", cfg.Provider)
		}
		baseURL = resolved
	}

	apiKey, err := providers.ResolveAPIKey(ctx, cfg.Provider)
	if err != nil {
		return nil, fmt.Errorf("rag: resolve credentials for provider %q: %w", cfg.Provider, err)
	}

	return New(baseURL, apiKey, cfg.Model), nil
}

// resolveBaseURL layers an explicit env override, then the models.dev
// catalog's own `api` field, then a plain OpenAI default — the same override
// order internal/provider.resolveBaseURL uses, scoped to what an embeddings
// call needs.
func resolveBaseURL(providerID string, entry modelsdev.Provider) (string, bool) {
	if value := os.Getenv(strings.ToUpper(providerID) + "_BASE_URL"); value != "" {
		return value, true
	}
	if entry.API != "" {
		return entry.API, true
	}
	if providerID == "openai" {
		return "https://api.openai.com/v1", true
	}
	return "", false
}
