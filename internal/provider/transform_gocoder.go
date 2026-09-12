package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

func init() {
	Register(gocoderTransform{byID{"gocoder"}})
}

// gocoderTransform implements the gocoder.org provider: the website's own
// inference proxy (OpenAI protocol) keyed by the signed-in account's `gk_`
// API key.
//
// The credential intentionally does not live in auth.json: it lives in
// gocoder.json (written by sign-in/onboarding), because auth.json is keyed
// by provider id and enumerated against the models.dev catalog — where a
// "gocoder" entry does not exist. The transform reads it directly.
type gocoderTransform struct{ byID }

// Apply points the provider at the site's inference proxy with the stored
// account key. With no stored account the provider simply has no key — the
// picker's existing "no credentials" handling covers it, per the Transform
// contract's note on unconfigured providers.
func (gocoderTransform) Apply(_ context.Context, r *Resolved) error {
	account, err := gocoder.LoadAccount()
	if err != nil {
		r.keyErr = fmt.Errorf("gocoder.org: read sign-in: %w", err)
		return nil
	}
	if account == nil {
		return nil // not signed in; the key resolver reports it
	}
	r.APIKey = account.Key
	if r.BaseURL == "" {
		r.BaseURL = gocoderInferenceBase(account)
	}
	return nil
}

// gocoderInferenceBase picks the proxy endpoint: explicit
// GOCODE_INFERENCE_URL override first, else derived from the account's site
// (local dev stacks serve the proxy under the site's own /v1; production
// fronts the dedicated proxy.gocoder.org host).
func gocoderInferenceBase(account *gocoder.Account) string {
	if v := os.Getenv("GOCODE_INFERENCE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if account != nil && account.URL != "" &&
		(strings.Contains(account.URL, "localhost") || strings.Contains(account.URL, "127.0.0.1")) {
		return strings.TrimRight(account.URL, "/") + "/v1"
	}
	return gocoder.InferenceBaseURL()
}

// FetchModels implements ModelSource: the provider's model list IS the
// site's free-models endpoint, so a signed-in user sees exactly what their
// gocoder.org account can run at $0.0 through the proxy.
func (gocoderTransform) FetchModels(ctx context.Context, r *Resolved) (map[string]modelsdev.Model, error) {
	account, err := gocoder.LoadAccount()
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, fmt.Errorf("gocoder.org: not logged in")
	}
	return gocoderModelCache.get(ctx, account)
}

// gocoderModelTTL matches the site's own catalog cache (15 min): the list
// changes on the order of weeks, so this is about not re-fetching per
// picker open, matching the copilot cache's rationale.
const gocoderModelTTL = 15 * time.Minute

// gocoderModelCache memoizes the free-model list per site+key, the same
// shape as the copilot cache: keyed on the credential so switching accounts
// can never serve the previous account's models, and a failed fetch is
// remembered briefly so an unusable site doesn't cost a round trip per open.
type gocoderModelCacheType struct {
	mu      sync.Mutex
	entries map[string]gocoderCacheEntry
}

type gocoderCacheEntry struct {
	models  map[string]modelsdev.Model
	err     error
	fetched time.Time
}

var gocoderModelCache = &gocoderModelCacheType{entries: map[string]gocoderCacheEntry{}}

func (c *gocoderModelCacheType) get(ctx context.Context, account *gocoder.Account) (map[string]modelsdev.Model, error) {
	key := account.URL + "\x00" + account.Key

	c.mu.Lock()
	entry, ok := c.entries[key]
	fresh := ok && time.Since(entry.fetched) < gocoderModelTTL
	c.mu.Unlock()
	if fresh {
		return entry.models, entry.err
	}

	client := gocoder.NewClient(account.URL)
	free, err := client.FreeModels(ctx, account.Key)
	var models map[string]modelsdev.Model
	if err == nil {
		models = freeToCatalog(free)
	}

	c.mu.Lock()
	c.entries[key] = gocoderCacheEntry{models: models, err: err, fetched: time.Now()}
	c.mu.Unlock()
	return models, err
}

// freeToCatalog maps the site's FreeModel rows into catalog Model entries.
// Tool calling is assumed available (the proxy is a protocol passthrough);
// context length and modality come from the site's data.
func freeToCatalog(free []gocoder.FreeModel) map[string]modelsdev.Model {
	out := make(map[string]modelsdev.Model, len(free))
	for _, m := range free {
		model := modelsdev.Model{
			ID:       m.ID,
			Name:     m.Name,
			ToolCall: true,
			Limit:    modelsdev.Limit{Context: float64(m.ContextLength)},
			Status:   "active",
		}
		if strings.Contains(m.Modality, "image") {
			model.Attachment = true
		}
		out[m.ID] = model
	}
	return out
}

// compile-time: the transform satisfies both halves it claims.
var (
	_ ModelSource = gocoderTransform{}
	_ Transform   = gocoderTransform{}
)

// Overlay implements CatalogOverlay: the gocoder provider is not in the
// public models.dev catalog, so this contributes its entry — id, name, npm
// (openai protocol), env override — with an empty model map that
// FetchModels fills live from the free-models endpoint.
//
// Signed out it contributes nothing: an overlay must be an enhancement, not
// a permanent catalog mutation, and an unusable provider in the picker is
// noise (the same reason ApplyOverlays skips failed overlays wholesale).
func (gocoderTransform) Overlay(_ context.Context) (modelsdev.Catalog, error) {
	account, err := gocoder.LoadAccount()
	if err != nil || account == nil {
		return nil, err // nil catalog: skipped by ApplyOverlays
	}
	return modelsdev.Catalog{
		"gocoder": {
			ID:   "gocoder",
			Name: "gocoder.org",
			NPM:  "@ai-sdk/openai-compatible", // Protocol() → openai
			Env:  []string{"GOCODER_API_KEY"},
			API:  "", // endpoint is account-derived (proxy host); see Apply
		},
	}, nil
}
