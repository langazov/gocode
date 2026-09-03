package provider

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/llm/anthropic"
	"github.com/langazov/gocode-go/internal/llm/gemini"
	"github.com/langazov/gocode-go/internal/llm/openai"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// keylessProvider lists local OpenAI-compatible endpoints that need no auth.
func keylessProvider(providerID string) bool {
	return providerID == "ollama" || providerID == "lmstudio"
}

// sdkBaseURL maps a models.dev `npm` package to the endpoint that package
// hard-codes.
//
// models.dev omits its own `api` field for exactly those providers whose
// TypeScript SDK supplies the endpoint — 26 of the catalog's 212 entries,
// including groq, mistral, cerebras, xai and perplexity. This port has no such
// SDKs, so without this table those providers resolve to *no* base URL, and an
// openai-protocol client with no base URL silently defaults to
// api.openai.com: a Mistral key posted to OpenAI. Keying on the npm package
// rather than the provider id means a new provider that reuses a known SDK
// works without another entry here.
//
// Only genuinely OpenAI-compatible endpoints belong in this table. A provider
// whose SDK speaks its own protocol must fall through and be reported, not
// guessed at — see FromConfig's unknown-endpoint error.
var sdkBaseURL = map[string]string{
	"@ai-sdk/openai":     openai.DefaultBaseURL,
	"@ai-sdk/groq":       "https://api.groq.com/openai/v1",
	"@ai-sdk/cerebras":   "https://api.cerebras.ai/v1",
	"@ai-sdk/mistral":    "https://api.mistral.ai/v1",
	"@ai-sdk/perplexity": "https://api.perplexity.ai",
	"@ai-sdk/deepinfra":  "https://api.deepinfra.com/v1/openai",
	"@ai-sdk/togetherai": "https://api.together.xyz/v1",
	"@ai-sdk/xai":        "https://api.x.ai/v1",
	"@ai-sdk/vercel":     "https://api.v0.dev/v1",
}

// providerBaseURL is the legacy provider-id table, kept for the local
// endpoints that are in no catalog (ollama, lmstudio) and for ids whose
// catalog entry predates their `api` field.
func providerBaseURL(providerID string) (string, bool) {
	defaults := map[string]string{
		"openai":     openai.DefaultBaseURL,
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

// resolveBaseURL layers every source of a provider's endpoint, most specific
// first. ok is false when none of them knows one — which must be an error
// rather than a default, because the default would be someone else's API.
func resolveBaseURL(providerID string, providerConfig *config.Provider, entry modelsdev.Provider) (string, bool) {
	// An explicit config override wins outright.
	if providerConfig != nil && providerConfig.Options.BaseURL != "" {
		return providerConfig.Options.BaseURL, true
	}
	// Then the env override, which used to be reachable only for the handful
	// of ids in the legacy table and only when the provider was *not*
	// declared in config.
	if value := os.Getenv(strings.ToUpper(providerID) + "_BASE_URL"); value != "" {
		return value, true
	}
	if entry.API != "" {
		return entry.API, true
	}
	if base, ok := sdkBaseURL[entry.NPM]; ok {
		return base, true
	}
	return providerBaseURL(providerID)
}

// Resolve produces a provider's fully-materialized configuration: the
// models.dev catalog entry and the user's config block, layered as
// resolveBaseURL/ResolveAPIKey describe, and then handed to every matching
// Transform.
//
// It is separate from Client so that callers which only want to inspect a
// provider — its models, its login methods — do not need a usable credential
// to do so. Credential validation happens in Client, after transforms have
// run, because a transform may be the thing that supplies the credential
// (github-copilot exchanges a GitHub token) or replace the need for one
// entirely (bedrock signs with AWS credentials instead).
func Resolve(ctx context.Context, providerID string, cfg *config.Config) (*Resolved, error) {
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

	protocol := ProtocolOpenAI
	if providerConfig != nil && providerConfig.API != "" {
		protocol = Protocol(providerConfig.API)
	}

	// The catalog names the provider's SDK, which is what decides the wire
	// protocol, and usually its endpoint too. Overlays (an opencode/Zen
	// account's own provider config, among others) must be merged in here,
	// not only on the model-listing path, or a connected account keeps
	// resolving to the public, unauthenticated endpoint.
	var entry modelsdev.Provider
	if catalogData, catErr := modelsdev.New().Get(ctx); catErr == nil {
		catalogData = ApplyOverlays(ctx, catalogData)
		if found, ok := catalogData[providerID]; ok {
			entry = found
			if providerConfig == nil || providerConfig.API == "" {
				protocol = Protocol(entry.NPM)
			}
		}
	}

	baseURL, _ := resolveBaseURL(providerID, providerConfig, entry)

	resolved := &Resolved{
		ID:       providerID,
		Name:     entry.Name,
		Protocol: protocol,
		BaseURL:  baseURL,
		APIKey:   key,
		Entry:    entry,
		Config:   providerConfig,
		Models:   entry.Models,
		keyErr:   keyErr,
	}
	if resolved.Name == "" {
		resolved.Name = providerID
	}
	if err := applyTransforms(ctx, resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

// Client builds the stream client for a resolved provider. Most providers
// speak one protocol for every model they list; a provider whose catalog
// declares per-model overrides (opencode/Zen chief among them — see
// model_route.go) gets its default client wrapped so requests for those
// specific models are routed to the protocol/endpoint they actually need.
func (r *Resolved) Client() (llm.StreamClient, error) {
	client, err := r.defaultClient()
	if err != nil {
		return nil, err
	}
	if modelsNeedRouting(r.Models) {
		return &modelRoutedClient{resolved: r, fallback: client}, nil
	}
	return client, nil
}

// defaultClient builds the provider-wide client from Resolved.Protocol —
// what every model gets unless a per-model override in modelRoutedClient
// says otherwise.
func (r *Resolved) defaultClient() (llm.StreamClient, error) {
	// A transform that signs requests or supplied its own key has met the
	// credential requirement; so has a local endpoint that wants none.
	needsKey := r.APIKey == "" && r.Options.Sign == nil && !keylessProvider(r.ID)
	if needsKey && r.keyErr != nil {
		return nil, r.keyErr
	}

	switch r.Protocol {
	case ProtocolAnthropic:
		client := anthropic.New(r.APIKey)
		client.Options = r.Options
		if r.BaseURL != "" {
			client.BaseURL = r.BaseURL
		} else if envBase := os.Getenv("ANTHROPIC_BASE_URL"); envBase != "" {
			client.BaseURL = envBase
		}
		return client, nil
	case ProtocolGemini:
		client := gemini.New(r.APIKey)
		client.Options = r.Options
		if r.BaseURL != "" {
			client.BaseURL = r.BaseURL
		} else if envBase := os.Getenv("GEMINI_BASE_URL"); envBase != "" {
			client.BaseURL = envBase
		}
		return client, nil
	default:
		// Never fall through to openai.DefaultBaseURL for a provider that is
		// not OpenAI. Doing so used to post the user's key for one provider to
		// another company's API — silently, and with a 401 as the only clue.
		if r.BaseURL == "" {
			return nil, fmt.Errorf(
				"provider %q: no API endpoint known (models.dev lists no `api` for it and its SDK %q is not one this port implements) — set provider.%s.options.baseURL in gocode.json",
				r.ID, r.Entry.NPM, r.ID)
		}
		client := openai.New(r.APIKey)
		client.Options = r.Options
		client.BaseURL = r.BaseURL
		return client, nil
	}
}

// FromConfig builds the stream client for a provider, honoring config
// (options.apiKey/baseURL, api protocol), known defaults, and env overrides —
// the runtime half of the provider config consumption.
func FromConfig(ctx context.Context, providerID string, cfg *config.Config) (llm.StreamClient, error) {
	resolved, err := Resolve(ctx, providerID, cfg)
	if err != nil {
		return nil, err
	}
	return resolved.Client()
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
