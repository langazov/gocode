package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/flag"
	"github.com/langazov/gocode-go/internal/global"
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

// zenOverlayCache is the in-process half of the overlay cache. zenOverlayMu
// is held across the network fetch as well as the cache reads, which
// single-flights it: Resolve runs per message and once per candidate inside
// Fallback, so several goroutines reaching a cold cache at once used to mean
// several identical round trips.
var (
	zenOverlayMu    sync.Mutex
	zenOverlayCache struct {
		key     string
		catalog modelsdev.Catalog
		err     error
		at      time.Time
	}
)

// zenOverlayFile is the on-disk half, written next to models.json in the
// cache directory.
//
// It exists because process boot always starts with a cold in-memory cache,
// and Overlay sits on the boot-critical path — bootStack resolves a provider
// before the server listens, so a ~0.9s round trip to opencode.ai was ~0.9s
// before the TUI drew anything. The public catalog solved the same problem
// the same way (see internal/modelsdev): serve whatever is on disk
// immediately, and refresh it in the background.
//
// Key is the hash of the credential the entry was fetched with, so a
// re-login or a different server is a miss rather than a wrong answer.
//
// What is cached is each endpoint's own answer rather than the merged
// modelsdev.Catalog it decodes into. That keeps zenConfig's conversion the
// one place the remote shape is interpreted, and it sidesteps a real trap:
// Provider.Whitelist is `json:"-"` — it has no place in the public catalog's
// wire format — so a catalog round-tripped through JSON silently comes back
// with no whitelist and the picker stops being pruned.
type zenOverlayFile struct {
	// Version is the shape of this file. An entry written by a different
	// version is a miss, not a best-effort decode: an empty overlay is a
	// legitimate cached state (the account has no overrides), so a shape
	// change that silently decodes to nothing would look exactly like one
	// and suppress the account's real model list.
	Version int    `json:"version"`
	Key     string `json:"key"`
	OrgID   string `json:"orgID,omitempty"`
	// Config is the raw {server}/api/config body, empty when the account has
	// no overrides.
	Config json.RawMessage `json:"config,omitempty"`
	// Models is the id list from the inference gateway's own /models, which
	// is all an API-key credential can reach. See the fallback in overlay.
	Models []string `json:"models,omitempty"`
}

// zenOverlayVersion is bumped whenever zenOverlayFile's shape changes.
const zenOverlayVersion = 1

// catalog decodes a cached entry back into the overlay it represents.
func (f zenOverlayFile) catalog() (modelsdev.Catalog, error) {
	if len(f.Config) > 0 {
		return zenDecodeConfig(f.Config)
	}
	if len(f.Models) > 0 {
		return modelsdev.Catalog{"opencode": {ID: "opencode", Whitelist: f.Models}}, nil
	}
	return nil, nil
}

// zenOverlayPath is where that file lives. A var so tests can redirect it.
var zenOverlayPath = func() string {
	return filepath.Join(global.Resolve().Cache, "zen-overlay.json")
}

// zenAccountKey identifies the credential an overlay belongs to. The token
// is hashed rather than stored: the file is a cache, not a credential store,
// and auth.json is the only place a token belongs.
func zenAccountKey(server, token string) string {
	sum := sha256.Sum256([]byte(server + "|" + token))
	return hex.EncodeToString(sum[:])
}

// Overlay returns the account's provider config, porting fetchProviders().
// Without a stored credential there is nothing to fetch and no overlay.
//
// It answers from memory, then from disk, and only fetches when neither has
// anything for this credential — the first run after a login, and nothing
// else. [RefreshOverlay] is what keeps the disk copy current, off the boot
// path.
func (t opencodeTransform) Overlay(ctx context.Context) (modelsdev.Catalog, error) {
	return t.overlay(ctx, false)
}

// RefreshOverlay re-fetches the overlay and rewrites the disk cache,
// implementing [OverlayRefresher]. The runtime calls it from a background
// goroutine at boot, alongside modelsdev's own catalog refresh.
func (t opencodeTransform) RefreshOverlay(ctx context.Context) error {
	_, err := t.overlay(ctx, true)
	return err
}

func (opencodeTransform) overlay(ctx context.Context, force bool) (modelsdev.Catalog, error) {
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
	key := zenAccountKey(server, token)

	zenOverlayMu.Lock()
	defer zenOverlayMu.Unlock()

	if !force && zenOverlayCache.key == key && time.Since(zenOverlayCache.at) < zenOverlayTTL {
		return zenOverlayCache.catalog, zenOverlayCache.err
	}
	if !force {
		if cached, ok := readZenOverlay(key); ok {
			if catalog, err := cached.catalog(); err == nil {
				// The org id travels with the overlay because resolving it
				// costs its own round trip (/api/orgs) for an OAuth
				// credential stored before login started recording it — and
				// Apply needs it on every inference request, so leaving it
				// to be re-resolved would put that round trip back on the
				// boot path this cache exists to clear.
				seedZenOrgID(token, cached.OrgID)
				zenOverlayCache.key, zenOverlayCache.catalog, zenOverlayCache.err, zenOverlayCache.at = key, catalog, nil, time.Now()
				return catalog, nil
			}
		}
	}
	// Explicitly offline: serve nothing rather than reaching out. The public
	// catalog honours the same flag, and an account overlay that silently
	// ignored it was the one thing on the boot path that still hit the
	// network with fetching disabled.
	if flag.DisableModelsFetch() {
		return nil, nil
	}

	orgID := zenOrgID(ctx, server, token, info)
	fetched := zenOverlayFile{Version: zenOverlayVersion, Key: key, OrgID: orgID}
	raw, fetchErr := zenConfig(ctx, server, token, orgID)
	catalog, decodeErr := zenDecodeConfig(raw)
	if fetchErr == nil && decodeErr != nil {
		fetchErr = decodeErr
	}
	if fetchErr == nil {
		fetched.Config = raw
	}
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
			fetched.Config, fetched.Models = nil, ids
			fetchErr = nil
		}
	}

	zenOverlayCache.key, zenOverlayCache.catalog, zenOverlayCache.err, zenOverlayCache.at = key, catalog, fetchErr, time.Now()
	// A failed fetch is not persisted: the disk copy is what the next boot
	// trusts instead of the network, and caching an outage there would
	// outlive the outage.
	if fetchErr == nil {
		writeZenOverlay(fetched)
	}
	return catalog, fetchErr
}

// readZenOverlay loads the disk cache when it belongs to this credential.
func readZenOverlay(key string) (zenOverlayFile, bool) {
	data, err := os.ReadFile(zenOverlayPath())
	if err != nil {
		return zenOverlayFile{}, false
	}
	var cached zenOverlayFile
	if err := json.Unmarshal(data, &cached); err != nil {
		return zenOverlayFile{}, false
	}
	if cached.Version != zenOverlayVersion || cached.Key != key {
		return zenOverlayFile{}, false
	}
	return cached, true
}

// writeZenOverlay replaces the disk cache atomically. Failures are logged
// and swallowed: the overlay is an enhancement, and an unwritable cache
// directory must not fail a provider resolution.
func writeZenOverlay(entry zenOverlayFile) {
	path := zenOverlayPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		global.LogBackground("opencode: cache overlay: %v", err)
		return
	}
	data, err := json.Marshal(entry)
	if err != nil {
		global.LogBackground("opencode: cache overlay: %v", err)
		return
	}
	temp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		global.LogBackground("opencode: cache overlay: %v", err)
		return
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
		global.LogBackground("opencode: cache overlay: %v", err)
	}
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

// seedZenOrgID primes the org-id cache from the overlay's disk copy, so a
// boot that answered the overlay from disk does not turn around and resolve
// the org id over the network anyway. An empty id is not seeded — that is
// "not known", not "known to be none".
func seedZenOrgID(token, orgID string) {
	if token == "" || orgID == "" {
		return
	}
	zenOrgIDCache.Lock()
	defer zenOrgIDCache.Unlock()
	zenOrgIDCache.token, zenOrgIDCache.orgID, zenOrgIDCache.at = token, orgID, time.Now()
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

// zenConfig fetches {server}/api/config and returns its raw body, so the
// caller can cache exactly what the server said. A nil body with a nil error
// means the account simply has no overrides.
func zenConfig(ctx context.Context, server, token, orgID string) ([]byte, error) {
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
	return io.ReadAll(res.Body)
}

// zenDecodeConfig converts an /api/config body into catalog entries. An
// empty body is no overlay, not an error.
func zenDecodeConfig(data []byte) (modelsdev.Catalog, error) {
	if len(data) == 0 {
		return nil, nil
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
	_ Transform        = opencodeTransform{}
	_ AuthProvider     = opencodeTransform{}
	_ Refresher        = opencodeTransform{}
	_ CatalogOverlay   = opencodeTransform{}
	_ OverlayRefresher = opencodeTransform{}
)
