package embed

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/provider"
)

// GocoderProvider selects gocoder.org's embeddings API, authenticated with
// the API key gocode stored when the user registered or logged in.
const GocoderProvider = "gocoder"

// gocoderUpstreams are the gocoder.org providers rag-plugin embeds through,
// in preference order, and the prefix each needs on an OpenAI model id. Both
// serve OpenAI's embedding models, so vectors stay in one space whichever the
// site has enabled — and match an index built directly against OpenAI.
// Leaving the choice to the server would follow its alphabetically-first
// active provider, which changes as providers are added, and a different
// model means vectors of a different size than an existing index holds.
var gocoderUpstreams = []struct{ name, modelPrefix string }{
	{"openrouter", "openai/"},
	{"openai", ""},
}

// gocoderMaxBatch is gocoder.org's per-request input limit.
const gocoderMaxBatch = 64

// Config selects the embeddings endpoint and model. All fields are optional;
// zero values use gocoder.org when the user is signed in to it, and otherwise
// fall back to a plain OpenAI setup resolved through the usual provider
// credential chain (models.dev catalog env[] -> {ID}_API_KEY -> auth.json),
// the same chain every chat provider in this Go port uses.
type Config struct {
	// Provider is a models.dev provider id, e.g. "openai", or
	// GocoderProvider. Defaults to GocoderProvider when a gocoder.org account
	// is stored and BaseURL is unset, else "openai".
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
	if client, ok, err := resolveGocoder(ctx, cfg); ok || err != nil {
		return client, err
	}
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

// resolveGocoder builds a gocoder.org client when cfg asks for one, or when
// cfg names no provider or endpoint and a gocoder.org account is stored. ok
// reports whether gocoder.org was chosen; false means resolve as before.
func resolveGocoder(ctx context.Context, cfg Config) (client *Client, ok bool, err error) {
	explicit := cfg.Provider == GocoderProvider
	if !explicit && (cfg.Provider != "" || cfg.BaseURL != "") {
		return nil, false, nil
	}
	account, err := gocoder.LoadAccount()
	if err != nil {
		if explicit {
			return nil, true, fmt.Errorf("rag: load gocoder.org account: %w", err)
		}
		return nil, false, nil
	}
	if account == nil || account.Key == "" {
		if explicit {
			return nil, true, fmt.Errorf("rag: embeddingProvider is %q but no gocoder.org account is stored at %s; start gocode to register or log in",
				GocoderProvider, gocoder.AccountPath())
		}
		return nil, false, nil
	}

	site := account.URL
	if site == "" {
		site = gocoder.BaseURL()
	}
	site = strings.TrimSuffix(site, "/")
	upstream, model, err := pickGocoderUpstream(ctx, gocoder.NewClient(site), cfg.Model)
	if err != nil {
		return nil, true, err
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = site + "/api"
	}
	client = New(baseURL, account.Key, model)
	client.Provider = upstream
	client.BatchSize = gocoderMaxBatch
	return client, true, nil
}

// pickGocoderUpstream chooses the first of gocoderUpstreams the site has
// enabled and spells model the way that upstream expects. With none enabled
// it fails rather than falling back to some other provider's model, whose
// vectors would not fit an index built with this one.
func pickGocoderUpstream(ctx context.Context, site *gocoder.Client, model string) (string, string, error) {
	if model == "" {
		model = "text-embedding-3-small"
	}
	active, err := site.EmbeddingProviders(ctx)
	if err != nil {
		return "", "", fmt.Errorf("rag: list gocoder.org embedding providers: %w", err)
	}
	enabled := make(map[string]bool, len(active))
	names := make([]string, 0, len(active))
	for _, p := range active {
		enabled[p.Name] = true
		names = append(names, p.Name)
	}
	for _, u := range gocoderUpstreams {
		if !enabled[u.name] {
			continue
		}
		model = strings.TrimPrefix(model, "openai/")
		if !strings.Contains(model, "/") {
			model = u.modelPrefix + model
		}
		return u.name, model, nil
	}
	return "", "", fmt.Errorf("rag: %s has no OpenAI-model embedding provider enabled (active: %s); set embeddingProvider or embeddingBaseURL to embed elsewhere",
		site.BaseURL, strings.Join(names, ", "))
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
