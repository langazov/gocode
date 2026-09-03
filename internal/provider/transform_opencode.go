package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/installation"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

func init() {
	Register(opencodeTransform{byID{"opencode"}})
}

// Ported from packages/core/src/plugin/provider/opencode.ts.
const (
	zenDefaultServer = "https://opencode.ai/console"
	zenClientID      = "opencode-cli"
)

// opencodeTransform implements the opencode/Zen account: a device-flow login,
// token refresh, and the per-account catalog fetched from {server}/api/config.
//
// That last part is the fourth layer of provider defaults — an org's own
// provider and model overrides, layered on top of the public models.dev
// catalog. It is the reason CatalogOverlay exists.
type opencodeTransform struct{ byID }

func (opencodeTransform) Apply(ctx context.Context, r *Resolved) error {
	info, err := ResolveCredential(ctx, r.ID, r.Entry)
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}
	switch info.Type {
	case "oauth":
		r.APIKey = info.Access
	case "api":
		r.APIKey = info.Key
	}
	if server := zenServer(info); server != zenDefaultServer && r.BaseURL == "" {
		r.BaseURL = server
	}
	// The inference gateway behind the catalog overlay's `api` URL requires
	// the account's org on every request — without it a real bearer token is
	// rejected outright ("Workspace selection is required"), which is why a
	// freshly authorized login otherwise looks identical to an invalid key.
	if orgID := zenOrgID(ctx, zenServer(info), r.APIKey, info); orgID != "" {
		r.Header("x-opencode-org-id", orgID)
	}
	// opencode.ai fronts both zen/v1 and inference/openai/v1 with a rule that
	// rejects any client whose User-Agent does not start with "opencode/" —
	// confirmed by capturing the real TS client's traffic: identical
	// requests (same key, same body) succeed with this header and fail with
	// a fabricated "FreeUsageLimitError: Rate limit exceeded" without it,
	// regardless of whether the credential is actually valid. Every other
	// provider's default (or Go's bare "Go-http-client/1.1") trips this.
	r.Header("User-Agent", "opencode/"+installation.Version)
	return nil
}

func (opencodeTransform) AuthMethods() []Method {
	return []Method{{
		Type:  MethodOAuth,
		Label: "OpenCode Console account",
		Login: zenLogin,
	}}
}

// RefreshCredential renews a Zen token against the same device token endpoint.
func (opencodeTransform) RefreshCredential(ctx context.Context, info auth.Info) (auth.Info, error) {
	tokens, err := auth.RefreshGrant(ctx, nil, zenServer(&info)+"/auth/device/token", zenClientID, info.Refresh, "")
	if err != nil {
		return auth.Info{}, err
	}
	next := info
	next.Access = tokens.AccessToken
	next.Refresh = tokens.RefreshToken
	next.Expires = tokens.ExpiresAt()
	return next, nil
}

// zenOverlayTTL matches modelsdev's own catalog TTL: Overlay is on Resolve's
// path (built for every message a session sends, and for every candidate
// Fallback scans), so a live /api/config fetch on each call would turn every
// turn into an extra network round trip. The account's provider config does
// not change within a session, so a short in-memory cache is enough.
const zenOverlayTTL = 5 * time.Minute

var zenOverlayCache struct {
	sync.Mutex
	key     string
	catalog modelsdev.Catalog
	err     error
	at      time.Time
}

// Overlay fetches the account's provider config, porting fetchProviders().
// Without a stored credential there is nothing to fetch and no overlay.
func (opencodeTransform) Overlay(ctx context.Context) (modelsdev.Catalog, error) {
	info, err := auth.Get("opencode")
	if err != nil || info == nil {
		return nil, err
	}
	token := info.Access
	if info.Type == "api" {
		token = info.Key
	}
	if token == "" {
		return nil, nil
	}
	server := zenServer(info)
	orgID := zenOrgID(ctx, server, token, info)
	key := server + "|" + orgID + "|" + token

	zenOverlayCache.Lock()
	if zenOverlayCache.key == key && time.Since(zenOverlayCache.at) < zenOverlayTTL {
		catalog, cachedErr := zenOverlayCache.catalog, zenOverlayCache.err
		zenOverlayCache.Unlock()
		return catalog, cachedErr
	}
	zenOverlayCache.Unlock()

	catalog, fetchErr := zenConfig(ctx, server, token, orgID)
	// A plain API key can never reach /api/config (opencode.ai answers it,
	// like /api/user and /api/orgs, with a flat 401 — confirmed directly
	// against the account API; only an OAuth session token is accepted
	// there). Per-model routing still works for these accounts, because the
	// public models.dev catalog already carries each model's `provider`
	// override independent of any account — see model_route.go. What is
	// lost is knowing which of the catalog's models this specific key may
	// actually use, so fall back to the inference gateway's own model
	// listing (a standard OpenAI-compatible GET /models, which a plain key
	// *can* call) purely to prune the picker to that set.
	if (fetchErr != nil || len(catalog) == 0) && info.Type == "api" {
		if ids, listErr := zenModelList(ctx, zenPublicBaseURL, token); listErr == nil && len(ids) > 0 {
			catalog = modelsdev.Catalog{"opencode": {ID: "opencode", Whitelist: ids}}
			fetchErr = nil
		}
	}

	zenOverlayCache.Lock()
	zenOverlayCache.key, zenOverlayCache.catalog, zenOverlayCache.err, zenOverlayCache.at = key, catalog, fetchErr, time.Now()
	zenOverlayCache.Unlock()

	return catalog, fetchErr
}

// zenPublicBaseURL is the default, accountless endpoint models.dev's public
// catalog declares for the "opencode" provider (entry.API) — stable and
// documented, unlike opencode.ai's console API. It is the base an API-key
// credential's requests already resolve to (see resolveBaseURL), so it is
// also where that key's own /models listing lives. A var, not a const, so
// tests can point it at an httptest server.
var zenPublicBaseURL = "https://opencode.ai/zen/v1"

// zenModelList calls the inference gateway's own GET /models — the one
// endpoint on opencode.ai a plain Zen API key can actually call — and
// returns the ids it lists. Unlike /api/config this carries no per-model
// routing detail, only which ids exist for this key; see Overlay's fallback.
func zenModelList(ctx context.Context, base, token string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "opencode/"+installation.Version)

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("opencode: %s/models returned %d", base, res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		ids = append(ids, model.ID)
	}
	return ids, nil
}

var zenOrgIDCache struct {
	sync.Mutex
	token string
	orgID string
	at    time.Time
}

// zenOrgID returns the account's org id: from stored metadata when present
// (populated at OAuth login time by zenAccount), or resolved live via
// /api/orgs for an OAuth session token that predates that fix. It is not
// resolvable at all for a plain API-key credential (MethodKey, the generic
// "paste your key" login every provider gets) — confirmed directly against
// the account API: opencode.ai's /api/user, /api/orgs and /api/config all
// answer a Zen API key with a flat 401 Unauthorized, unlike an OAuth session
// token. That is a real, permanent gap: without an org id, Overlay's
// /api/config fetch fails outright, so a key-authenticated account falls
// back to the full public catalog (unpruned to what it is actually entitled
// to) rather than the account's real model list. There is nothing this port
// can do about it short of the account re-authenticating via the OAuth
// method ("OpenCode Console account"), which is the only login path Zen's
// console API accepts.
func zenOrgID(ctx context.Context, server, token string, info *auth.Info) string {
	if info != nil {
		if orgID := info.Metadata["orgID"]; orgID != "" {
			return orgID
		}
	}
	if token == "" || info == nil || info.Type != "oauth" {
		return ""
	}

	zenOrgIDCache.Lock()
	if zenOrgIDCache.token == token && time.Since(zenOrgIDCache.at) < zenOverlayTTL {
		orgID := zenOrgIDCache.orgID
		zenOrgIDCache.Unlock()
		return orgID
	}
	zenOrgIDCache.Unlock()

	account, err := zenAccount(ctx, server, token)
	orgID := ""
	if err == nil {
		orgID = account["orgID"]
	}

	zenOrgIDCache.Lock()
	zenOrgIDCache.token, zenOrgIDCache.orgID, zenOrgIDCache.at = token, orgID, time.Now()
	zenOrgIDCache.Unlock()

	return orgID
}

func zenServer(info *auth.Info) string {
	if info != nil {
		if server := info.Metadata["server"]; server != "" {
			return server
		}
	}
	if server := os.Getenv("GOCODE_CONSOLE_SERVER"); server != "" {
		return server
	}
	return zenDefaultServer
}

// resolveVerificationURI resolves a (possibly relative) verification URI
// against the console's own origin, the way account.ts does with
// `new URL(parsed.verification_uri_complete, server + "/")`.
func resolveVerificationURI(server, target string) (string, error) {
	base, err := url.Parse(server + "/")
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", fmt.Errorf("opencode: unexpected verification URI scheme %q", resolved.Scheme)
	}
	return resolved.String(), nil
}

func zenLogin(ctx context.Context, _ map[string]string) (Credential, error) {
	server := zenServer(nil)
	flow := auth.DeviceFlow{
		ClientID:      zenClientID,
		DeviceCodeURL: server + "/auth/device/code",
		TokenURL:      server + "/auth/device/token",
		JSONRequest:   true,
	}
	code, err := flow.Start(ctx)
	if err != nil {
		return Credential{}, err
	}
	target := code.VerificationURIComplete
	if target == "" {
		target = code.VerificationURI
	}
	// The console sends the verification URI relative to its own origin
	// (e.g. "/console/device?..."); resolve it against server, matching
	// account.ts's `new URL(parsed.verification_uri_complete, server + "/")`.
	if resolved, err := resolveVerificationURI(server, target); err == nil {
		target = resolved
	}
	promptLogin(ctx, LoginPrompt{
		URL:     target,
		Code:    code.UserCode,
		Message: fmt.Sprintf("Open %s and enter code: %s\n\nWaiting for authorization...", target, code.UserCode),
	})

	token, err := flow.Poll(ctx, code)
	if err != nil {
		return Credential{}, err
	}
	metadata := map[string]string{"server": server}
	if account, err := zenAccount(ctx, server, token.AccessToken); err == nil {
		for key, value := range account {
			metadata[key] = value
		}
	}
	return Credential{
		Type:     "oauth",
		Access:   token.AccessToken,
		Refresh:  token.RefreshToken,
		Expires:  auth.TokenResponse{ExpiresIn: token.ExpiresIn}.ExpiresAt(),
		Metadata: metadata,
	}, nil
}

// zenAccount fetches the account's id/email and org, porting credential() in
// opencode.ts. The org id in particular is required later, on every
// inference request (see Apply's x-opencode-org-id header) and on the
// catalog overlay fetch (Overlay's x-org-id) — without it those endpoints
// treat an otherwise-valid bearer token as unauthenticated.
func zenAccount(ctx context.Context, server, accessToken string) (map[string]string, error) {
	var user struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := zenGetJSON(ctx, server+"/api/user", accessToken, &user); err != nil {
		return nil, err
	}
	var orgs []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := zenGetJSON(ctx, server+"/api/orgs", accessToken, &orgs); err != nil {
		return nil, err
	}
	sort.Slice(orgs, func(i, j int) bool {
		if orgs[i].Name != orgs[j].Name {
			return orgs[i].Name < orgs[j].Name
		}
		return orgs[i].ID < orgs[j].ID
	})
	metadata := map[string]string{"accountID": user.ID, "email": user.Email}
	if len(orgs) > 0 {
		metadata["orgID"] = orgs[0].ID
		metadata["orgName"] = orgs[0].Name
	}
	return metadata, nil
}

func zenGetJSON(ctx context.Context, endpoint, accessToken string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("opencode: %s returned %d", endpoint, res.StatusCode)
	}
	return json.Unmarshal(data, out)
}

// zenProviderConfig is the subset of the remote config this port can express.
// The TS side also carries variants, modalities, per-model headers and request
// bodies; those map onto catalog fields the Go port does not model yet, so
// decoding them here would produce values nothing reads.
type zenProviderConfig struct {
	Name string `json:"name"`
	NPM  string `json:"npm"`
	API  string `json:"api"`
	// Whitelist is the exact set of model ids the account is entitled to;
	// see mergeProvider's pruning step in transform.go.
	Whitelist []string `json:"whitelist"`
	Models    map[string]struct {
		Name        string `json:"name"`
		ID          string `json:"id"`
		Family      string `json:"family"`
		ReleaseDate string `json:"release_date"`
		Status      string `json:"status"`
		ToolCall    *bool  `json:"tool_call"`
		Limit       *struct {
			Context float64 `json:"context"`
			Output  float64 `json:"output"`
		} `json:"limit"`
		Cost *struct {
			Input      float64  `json:"input"`
			Output     float64  `json:"output"`
			CacheRead  *float64 `json:"cache_read"`
			CacheWrite *float64 `json:"cache_write"`
		} `json:"cost"`
		// Provider routes this specific model through a different SDK/wire
		// protocol than the rest of the account's catalog — Zen proxies its
		// Claude, Gemini and GPT-5-family/Grok/Muse-Spark models to the
		// matching upstream API instead of its own OpenAI-compatible Chat
		// Completions endpoint. See fromconfig.go's per-model client dispatch.
		Provider *struct {
			NPM string `json:"npm"`
			API string `json:"api"`
		} `json:"provider"`
	} `json:"models"`
}

// zenConfig fetches and converts {server}/api/config.
func zenConfig(ctx context.Context, server, token, orgID string) (modelsdev.Catalog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server+"/api/config", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if orgID != "" {
		req.Header.Set("x-org-id", orgID)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	// A 404 means the account simply has no overrides, not a failure.
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("opencode: %s/api/config returned %d", server, res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var payload struct {
		Config struct {
			Provider map[string]zenProviderConfig `json:"provider"`
		} `json:"config"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}

	catalog := modelsdev.Catalog{}
	for providerID, item := range payload.Config.Provider {
		entry := modelsdev.Provider{
			ID:        providerID,
			Name:      item.Name,
			NPM:       item.NPM,
			API:       item.API,
			Whitelist: item.Whitelist,
		}
		if len(item.Models) > 0 {
			entry.Models = map[string]modelsdev.Model{}
		}
		for modelID, config := range item.Models {
			model := modelsdev.Model{
				ID:          modelID,
				Name:        config.Name,
				Family:      config.Family,
				ReleaseDate: config.ReleaseDate,
				Status:      config.Status,
			}
			if config.ID != "" {
				model.ID = config.ID
			}
			if config.ToolCall != nil {
				model.ToolCall = *config.ToolCall
			}
			if config.Limit != nil {
				model.Limit = modelsdev.Limit{Context: config.Limit.Context, Output: config.Limit.Output}
			}
			if config.Cost != nil {
				cost := &modelsdev.Cost{}
				cost.Input = config.Cost.Input
				cost.Output = config.Cost.Output
				cost.CacheRead = config.Cost.CacheRead
				cost.CacheWrite = config.Cost.CacheWrite
				model.Cost = cost
			}
			if config.Provider != nil {
				model.Provider = &modelsdev.ProviderOverride{NPM: config.Provider.NPM, API: config.Provider.API}
			}
			entry.Models[modelID] = model
		}
		catalog[providerID] = entry
	}
	return catalog, nil
}

var (
	_ Transform      = opencodeTransform{}
	_ AuthProvider   = opencodeTransform{}
	_ Refresher      = opencodeTransform{}
	_ CatalogOverlay = opencodeTransform{}
)
