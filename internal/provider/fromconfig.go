package provider

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/anomalyco/opencode-go/internal/auth"
	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/llm"
	"github.com/anomalyco/opencode-go/internal/llm/anthropic"
	"github.com/anomalyco/opencode-go/internal/llm/gemini"
	"github.com/anomalyco/opencode-go/internal/llm/openai"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

// keylessProvider lists local OpenAI-compatible endpoints that need no auth.
func keylessProvider(providerID string) bool {
	return providerID == "ollama" || providerID == "lmstudio"
}

// openaiCompatibleBase resolves a default endpoint for known
// OpenAI-compatible providers, with {PROVIDER}_BASE_URL overrides.
func openaiCompatibleBase(providerID string) (string, bool) {
	if value := os.Getenv(strings.ToUpper(providerID) + "_BASE_URL"); value != "" {
		return value, true
	}
	defaults := map[string]string{
		"openai":     "https://api.openai.com/v1",
		"openrouter": "https://openrouter.ai/api/v1",
		"groq":       "https://api.groq.com/openai/v1",
		"together":   "https://api.together.xyz/v1",
		"togetherai": "https://api.together.xyz/v1",
		"zhipuai":    "https://open.bigmodel.cn/api/paas/v4",
		"ollama":     "http://localhost:11434/v1",
		"lmstudio":   "http://localhost:1234/v1",
	}
	base, ok := defaults[providerID]
	return base, ok
}

// FromConfig builds the stream client for a provider, honoring config
// (options.apiKey/baseURL, api protocol), known defaults, and env overrides —
// the runtime half of the provider config consumption.
func FromConfig(ctx context.Context, providerID string, cfg *config.Config) (llm.StreamClient, error) {
	service := New(modelsdev.New())
	key, keyErr := service.ResolveAPIKey(ctx, providerID)

	var providerConfig *config.Provider
	if cfg != nil {
		if entry, ok := cfg.Provider[providerID]; ok {
			providerConfig = &entry
			if entry.Options.APIKey != "" {
				key, keyErr = entry.Options.APIKey, nil
			}
		}
	}

	baseURL := ""
	protocol := "openai"
	if providerConfig != nil {
		baseURL = providerConfig.Options.BaseURL
		if providerConfig.API != "" {
			protocol = Protocol(providerConfig.API)
		}
	}

	// Catalog providers carry their own endpoint and SDK protocol; use them
	// unless the config overrode the endpoint.
	if baseURL == "" {
		catalogData, catErr := modelsdev.New().Get(ctx)
		if catErr == nil {
			if entry, ok := catalogData[providerID]; ok {
				baseURL = entry.API
				protocol = Protocol(entry.NPM)
			}
		}
	}

	switch providerID {
	case "anthropic":
		protocol = "anthropic"
	case "google", "gemini", "google-vertex":
		protocol = "gemini"
	}

	if providerConfig == nil {
		if base, ok := openaiCompatibleBase(providerID); ok && baseURL == "" {
			baseURL = base
		}
		if keyErr != nil && !keylessProvider(providerID) {
			return nil, keyErr
		}
	}

	switch protocol {
	case "anthropic":
		if keyErr != nil {
			return nil, keyErr
		}
		client := anthropic.New(key)
		if baseURL != "" {
			client.BaseURL = baseURL
		} else if envBase := os.Getenv("ANTHROPIC_BASE_URL"); envBase != "" {
			client.BaseURL = envBase
		}
		return client, nil
	case "gemini":
		if keyErr != nil {
			return nil, keyErr
		}
		client := gemini.New(key)
		if baseURL != "" {
			client.BaseURL = baseURL
		} else if envBase := os.Getenv("GEMINI_BASE_URL"); envBase != "" {
			client.BaseURL = envBase
		}
		return client, nil
	default:
		if keyErr != nil && !keylessProvider(providerID) {
			return nil, keyErr
		}
		client := openai.New(key)
		if baseURL != "" {
			client.BaseURL = baseURL
		}
		return client, nil
	}
}

// Fallback finds an available provider when the configured one has no
// credentials, mirroring the original's autoload behavior: providers are only
// loaded when credentials exist (env var, auth.json entry, or config
// apiKey), and the interface falls back to one that is available. The
// configured model is preserved whenever the fallback provider serves it.
//
// Preference order: auth entries matching the requested provider (e.g.
// "minimax" -> "minimax-coding-plan"), then remaining auth entries present in
// the catalog, then catalog providers whose env credentials are set.
func Fallback(ctx context.Context, requested, requestedModel string, cfg *config.Config) (providerID, modelID string, ok bool) {
	catalogData, err := modelsdev.New().Get(ctx)
	if err != nil {
		catalogData = nil
	}

	entries, _ := auth.All()
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ids[i], ids[j]
		aMatch := matchScore(a, requested)
		bMatch := matchScore(b, requested)
		if aMatch != bMatch {
			return aMatch > bMatch
		}
		return a < b
	})

	var candidates []string
	for _, id := range ids {
		if _, exists := catalogData[id]; exists {
			candidates = append(candidates, id)
		}
	}
	for id, entry := range catalogData {
		if providerHasEnvCredentials(entry.Env) {
			if !containsString(candidates, id) {
				candidates = append(candidates, id)
			}
		}
	}

	for _, id := range candidates {
		if _, err := FromConfig(ctx, id, cfg); err != nil {
			continue
		}
		models := make([]string, 0, len(catalogData[id].Models))
		for modelID := range catalogData[id].Models {
			models = append(models, modelID)
		}
		sort.Strings(models)
		if len(models) == 0 {
			continue
		}
		// Keep the configured model when the fallback provider serves it.
		if requestedModel != "" {
			for _, modelID := range models {
				if modelID == requestedModel {
					return id, modelID, true
				}
			}
		}
		return id, models[0], true
	}
	return "", "", false
}

// matchScore ranks credential entries against the requested provider:
// direct match best, then family match ("minimax" -> "minimax-coding-plan").
func matchScore(id, requested string) int {
	switch {
	case id == requested:
		return 3
	case strings.Contains(id, requested) || strings.Contains(requested, id):
		return 2
	default:
		return 0
	}
}

func providerHasEnvCredentials(envList []string) bool {
	for _, name := range envList {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

func containsString(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
