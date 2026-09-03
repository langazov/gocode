package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// TestResolveVerificationURI covers the console's device-code response,
// which sends verification_uri_complete as a path relative to its own
// origin (e.g. "/console/device?..."); zenLogin must resolve it against the
// server before showing it to the user, or the printed prompt is unusable.
func TestResolveVerificationURI(t *testing.T) {
	cases := []struct {
		name   string
		server string
		target string
		want   string
	}{
		{
			name:   "relative path against console server",
			server: "https://opencode.ai/console",
			target: "/console/device?user_code=ZSMX-RKCG&client_id=opencode-cli",
			want:   "https://opencode.ai/console/device?user_code=ZSMX-RKCG&client_id=opencode-cli",
		},
		{
			name:   "already absolute",
			server: "https://opencode.ai/console",
			target: "https://opencode.ai/console/device?user_code=ABCD-1234",
			want:   "https://opencode.ai/console/device?user_code=ABCD-1234",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveVerificationURI(tc.server, tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("resolveVerificationURI(%q, %q) = %q, want %q", tc.server, tc.target, got, tc.want)
			}
		})
	}
}

// TestOpencodeApplySendsOrgHeaderFromMetadata covers the actual chat-request
// bug: opencode.ai's inference gateway rejects an otherwise-valid bearer
// token with "Workspace selection is required" unless every request also
// carries x-opencode-org-id. That id lives in the credential's metadata
// (set at login by zenAccount), so Apply must forward it as a header.
func TestOpencodeApplySendsOrgHeaderFromMetadata(t *testing.T) {
	writeAuth(t, map[string]any{
		"opencode": map[string]any{
			"type": "oauth", "access": "tok", "refresh": "r",
			"expires":  time.Now().Add(time.Hour).UnixMilli(),
			"metadata": map[string]string{"orgID": "org-123"},
		},
	})
	r := &Resolved{ID: "opencode"}
	if err := (opencodeTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.APIKey != "tok" {
		t.Errorf("APIKey = %q, want the stored access token", r.APIKey)
	}
	if got := r.Options.Headers["x-opencode-org-id"]; got != "org-123" {
		t.Errorf("x-opencode-org-id header = %q, want org-123", got)
	}
}

// TestOpencodeApplyOmitsOrgHeaderWithoutMetadata: a credential from before
// this metadata existed (or a plain API key) must not crash or send a junk
// header.
func TestOpencodeApplyOmitsOrgHeaderWithoutMetadata(t *testing.T) {
	writeAuth(t, map[string]any{
		"opencode": map[string]any{"type": "api", "key": "sk-test"},
	})
	r := &Resolved{ID: "opencode"}
	if err := (opencodeTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.APIKey != "sk-test" {
		t.Errorf("APIKey = %q, want the stored key", r.APIKey)
	}
	if _, ok := r.Options.Headers["x-opencode-org-id"]; ok {
		t.Errorf("headers = %v, want no org header without metadata", r.Options.Headers)
	}
}

// TestOpencodeApplySendsBrandedUserAgent: confirmed by packet-capturing the
// real TS client — opencode.ai's edge rejects any request to zen/v1 or
// inference/openai/v1 whose User-Agent does not start with "opencode/",
// answering with a fabricated "FreeUsageLimitError: Rate limit exceeded"
// regardless of whether the credential is valid. Every other provider's
// default (or Go's bare "Go-http-client/1.1") trips this, which is why a
// correctly authenticated request could still come back looking exactly
// like an invalid key.
func TestOpencodeApplySendsBrandedUserAgent(t *testing.T) {
	writeAuth(t, map[string]any{
		"opencode": map[string]any{"type": "api", "key": "sk-test"},
	})
	r := &Resolved{ID: "opencode"}
	if err := (opencodeTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	got := r.Options.Headers["User-Agent"]
	if !strings.HasPrefix(got, "opencode/") {
		t.Errorf("User-Agent = %q, want a value starting with \"opencode/\"", got)
	}
}

// TestZenAccountPicksOrgAlphabetically ports credential()'s org selection:
// when an account belongs to several orgs, the one used (and the one whose
// id becomes x-opencode-org-id on every request) is the alphabetically
// first by name, then by id.
func TestZenAccountPicksOrgAlphabetically(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q, want the bearer token", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/user":
			w.Write([]byte(`{"id":"user-1","email":"a@example.com"}`))
		case "/api/orgs":
			w.Write([]byte(`[{"id":"org-b","name":"Zeta"},{"id":"org-a","name":"Acme"}]`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	metadata, err := zenAccount(context.Background(), server.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"accountID": "user-1",
		"email":     "a@example.com",
		"orgID":     "org-a",
		"orgName":   "Acme",
	}
	for key, value := range want {
		if metadata[key] != value {
			t.Errorf("metadata[%q] = %q, want %q", key, metadata[key], value)
		}
	}
}

// TestZenOrgIDPrefersMetadata: a stored org id must never trigger a network
// call — most requests take this path.
func TestZenOrgIDPrefersMetadata(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer server.Close()

	info := &auth.Info{Type: "oauth", Metadata: map[string]string{"orgID": "org-cached"}}
	got := zenOrgID(context.Background(), server.URL, "tok", info)
	if got != "org-cached" {
		t.Errorf("orgID = %q, want the cached one", got)
	}
	if called {
		t.Error("zenOrgID hit the network despite having a cached org id")
	}
}

// TestZenOrgIDResolvesLiveForOAuthWithoutMetadata: an OAuth credential
// stored before this session's metadata fix existed has no org id on disk
// yet, but its session token is still valid against /api/orgs, so it should
// self-heal on the next call rather than staying broken until a re-login.
func TestZenOrgIDResolvesLiveForOAuthWithoutMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user":
			w.Write([]byte(`{"id":"user-1","email":"a@example.com"}`))
		case "/api/orgs":
			w.Write([]byte(`[{"id":"org-live","name":"Acme"}]`))
		}
	}))
	defer server.Close()

	info := &auth.Info{Type: "oauth"}
	got := zenOrgID(context.Background(), server.URL, "tok-live", info)
	if got != "org-live" {
		t.Errorf("orgID = %q, want the live-resolved one", got)
	}
}

// TestZenOrgIDNeverAttemptsLiveResolutionForAPIKey: opencode.ai's console
// API (api/user, api/orgs, api/config) answers a plain Zen API key with a
// flat 401 Unauthorized — confirmed directly against the real account, not
// assumed. Attempting the live path for an "api"-type credential can only
// waste a request every cache window, so it must never even try.
func TestZenOrgIDNeverAttemptsLiveResolutionForAPIKey(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"_tag":"Unauthorized"}`))
	}))
	defer server.Close()

	info := &auth.Info{Type: "api", Key: "sk-test"}
	got := zenOrgID(context.Background(), server.URL, "sk-test", info)
	if got != "" {
		t.Errorf("orgID = %q, want empty (no org is resolvable for an API key)", got)
	}
	if called {
		t.Error("zenOrgID attempted a live fetch for an API-key credential, which the console API can never accept")
	}
}

// TestResolveMergesRegisteredOverlays is the other half of the chat-request
// bug: Resolve() used to look up a provider straight from the public
// models.dev catalog, so a CatalogOverlay (the opencode/Zen account's own
// endpoint and models) never reached the client actually used to send a
// message — only the separate model-listing path saw it. A connected
// account kept resolving to the public, unauthenticated endpoint no matter
// what the overlay said.
func TestResolveMergesRegisteredOverlays(t *testing.T) {
	// Isolate from any real opencode/Zen credential on this machine, for the
	// same reason as TestApplyOverlaysMergesWithoutLosingCatalogFields.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("ZENOVERLAYTEST_API_KEY", "secret")
	before := len(registry)
	Register(stubOverlay{byID: byID{"zen-overlay-stub"}, catalog: modelsdev.Catalog{
		"zenoverlaytest": {
			ID:  "zenoverlaytest",
			NPM: "@ai-sdk/openai-compatible",
			API: "https://overlay.example.com/v1",
		},
	}})
	t.Cleanup(func() { registry = registry[:before] })

	client, err := FromConfig(context.Background(), "zenoverlaytest", &config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got := baseOf(t, client); got != "https://overlay.example.com/v1" {
		t.Errorf("base = %q, want the overlay's endpoint", got)
	}
}

func TestZenConfigConvertsRemoteProviders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config" {
			t.Errorf("path = %s, want /api/config", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q, want the bearer token", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-org-id") != "org-1" {
			t.Errorf("x-org-id = %q, want org-1", r.Header.Get("x-org-id"))
		}
		w.Write([]byte(`{"config":{"provider":{
			"acme":{"name":"Acme","npm":"@ai-sdk/openai-compatible","api":"https://acme.example.com/v1",
				"models":{"acme-large":{"name":"Acme Large","tool_call":true,
					"limit":{"context":200000,"output":8192},
					"cost":{"input":1.5,"output":7.5}}}}
		}}}`))
	}))
	defer server.Close()

	catalog, err := zenConfig(context.Background(), server.URL, "tok", "org-1")
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog["acme"]
	if !ok {
		t.Fatalf("expected an 'acme' provider, got %v", catalog)
	}
	if entry.Name != "Acme" || entry.API != "https://acme.example.com/v1" {
		t.Errorf("entry = %+v, want the remote name and api", entry)
	}
	model := entry.Models["acme-large"]
	if model.Limit.Context != 200000 || model.Limit.Output != 8192 {
		t.Errorf("limit = %+v, want the remote limits", model.Limit)
	}
	if model.Cost == nil || model.Cost.Input != 1.5 {
		t.Errorf("cost = %+v, want the remote cost", model.Cost)
	}
	if !model.ToolCall {
		t.Error("tool_call should have carried across")
	}
}

// TestZenConfigDecodesPerModelProviderOverride: opencode.ai routes some
// models (Claude, Gemini, most of GPT-5-family/Grok/Muse-Spark) through a
// different upstream API than the account's default OpenAI-compatible Chat
// Completions endpoint, named per model by a `provider` object. Without
// decoding it, those requests went to the wrong endpoint entirely and came
// back as a bare "Endpoint is unavailable" with no indication why.
func TestZenConfigDecodesPerModelProviderOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"config":{"provider":{
			"opencode":{"name":"OpenCode","npm":"@ai-sdk/openai-compatible","api":"https://opencode.ai/inference/openai/v1",
				"models":{
					"claude-haiku-4-5":{"name":"Claude Haiku 4.5","provider":{"npm":"@ai-sdk/anthropic","api":"https://opencode.ai/inference/anthropic/v1"}},
					"gpt-5":{"name":"GPT-5","provider":{"npm":"@ai-sdk/openai"}},
					"big-pickle":{"name":"Big Pickle"}
				}}
		}}}`))
	}))
	defer server.Close()

	catalog, err := zenConfig(context.Background(), server.URL, "tok", "")
	if err != nil {
		t.Fatal(err)
	}
	models := catalog["opencode"].Models

	claude := models["claude-haiku-4-5"].Provider
	if claude == nil || claude.NPM != "@ai-sdk/anthropic" || claude.API != "https://opencode.ai/inference/anthropic/v1" {
		t.Errorf("claude-haiku-4-5.Provider = %+v, want the anthropic override", claude)
	}
	gpt5 := models["gpt-5"].Provider
	if gpt5 == nil || gpt5.NPM != "@ai-sdk/openai" || gpt5.API != "" {
		t.Errorf("gpt-5.Provider = %+v, want the openai (Responses) override with no api of its own", gpt5)
	}
	if models["big-pickle"].Provider != nil {
		t.Errorf("big-pickle.Provider = %+v, want nil (no override -> the provider's own default protocol)", models["big-pickle"].Provider)
	}
}

// TestZenConfigTreats404AsNoOverrides: an account with no overrides is the
// normal case, not an error.
func TestZenConfigTreats404AsNoOverrides(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	catalog, err := zenConfig(context.Background(), server.URL, "tok", "")
	if err != nil {
		t.Fatalf("a 404 must not be an error: %v", err)
	}
	if len(catalog) != 0 {
		t.Errorf("catalog = %v, want empty", catalog)
	}
}

// TestZenModelList covers the API-key fallback: opencode.ai's console API
// (/api/config) rejects a plain Zen API key outright, but the inference
// gateway's own GET /models — a standard OpenAI-compatible listing endpoint
// — accepts it, and is the only way an API-key-authenticated account can
// learn which models it may actually use.
func TestZenModelList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %s, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("Authorization = %q, want the bearer key", r.Header.Get("Authorization"))
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "opencode/") {
			t.Errorf("User-Agent = %q, want it to start with opencode/", r.Header.Get("User-Agent"))
		}
		w.Write([]byte(`{"object":"list","data":[{"id":"big-pickle","object":"model"},{"id":"claude-opus-5","object":"model"}]}`))
	}))
	defer server.Close()

	ids, err := zenModelList(context.Background(), server.URL, "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"big-pickle", "claude-opus-5"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("ids = %v, want %v", ids, want)
	}
}

// TestOverlayFallsBackToModelListForAPIKey is the end-to-end version of
// TestZenModelList: when /api/config fails for an "api"-type credential,
// Overlay must still prune the picker using the /models fallback rather
// than giving up and leaving the account showing the full, unfiltered
// public catalog.
func TestOverlayFallsBackToModelListForAPIKey(t *testing.T) {
	writeAuth(t, map[string]any{
		"opencode": map[string]any{"type": "api", "key": "sk-test"},
	})
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"_tag":"Unauthorized"}`))
	}))
	defer console.Close()
	t.Setenv("GOCODE_CONSOLE_SERVER", console.URL)

	inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"big-pickle"},{"id":"claude-opus-5"}]}`))
	}))
	defer inference.Close()

	previous := zenPublicBaseURL
	zenPublicBaseURL = inference.URL
	defer func() { zenPublicBaseURL = previous }()

	catalog, err := (opencodeTransform{}).Overlay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog["opencode"]
	if !ok {
		t.Fatalf("expected an opencode overlay entry, got %v", catalog)
	}
	want := []string{"big-pickle", "claude-opus-5"}
	if len(entry.Whitelist) != len(want) || entry.Whitelist[0] != want[0] || entry.Whitelist[1] != want[1] {
		t.Errorf("whitelist = %v, want %v", entry.Whitelist, want)
	}
}

// stubOverlay contributes a fixed catalog, standing in for a Zen account.
type stubOverlay struct {
	byID
	catalog modelsdev.Catalog
	err     error
}

func (stubOverlay) Apply(context.Context, *Resolved) error { return nil }

func (s stubOverlay) Overlay(context.Context) (modelsdev.Catalog, error) {
	return s.catalog, s.err
}

func TestApplyOverlaysMergesWithoutLosingCatalogFields(t *testing.T) {
	// Isolate from any real opencode/Zen credential on this machine: the
	// always-registered opencodeTransform is also a CatalogOverlay, and
	// without this it would make a live network call and add its own
	// entries on top of the stub's.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := modelsdev.Catalog{
		"acme": {
			ID:   "acme",
			Name: "Acme",
			Env:  []string{"ACME_API_KEY"},
			NPM:  "@ai-sdk/openai-compatible",
			API:  "https://public.example.com/v1",
			Models: map[string]modelsdev.Model{
				"m1": {ID: "m1", Name: "Public M1", Limit: modelsdev.Limit{Context: 1000}},
				"m2": {ID: "m2", Name: "Untouched"},
			},
		},
		"other": {ID: "other", Name: "Other"},
	}
	before := len(registry)
	Register(stubOverlay{byID: byID{"zen-stub"}, catalog: modelsdev.Catalog{
		"acme": {
			API: "https://org.example.com/v1",
			Models: map[string]modelsdev.Model{
				"m1": {Limit: modelsdev.Limit{Context: 200000, Output: 8192}},
				"m3": {ID: "m3", Name: "Org Only"},
			},
		},
	}})
	t.Cleanup(func() { registry = registry[:before] })

	merged := ApplyOverlays(context.Background(), base)

	acme := merged["acme"]
	if acme.API != "https://org.example.com/v1" {
		t.Errorf("api = %q, want the overlay's", acme.API)
	}
	// Fields the overlay did not set must survive.
	if acme.Name != "Acme" || len(acme.Env) != 1 || acme.NPM == "" {
		t.Errorf("overlay clobbered fields it did not set: %+v", acme)
	}
	if got := acme.Models["m1"]; got.Limit.Context != 200000 || got.Name != "Public M1" {
		t.Errorf("m1 = %+v, want the overlay's limit and the catalog's name", got)
	}
	if _, ok := acme.Models["m2"]; !ok {
		t.Error("a model the overlay did not mention must survive")
	}
	if _, ok := acme.Models["m3"]; !ok {
		t.Error("a model only the overlay defines must be added")
	}
	if _, ok := merged["other"]; !ok {
		t.Error("a provider the overlay did not mention must survive")
	}
	// The input must not be mutated.
	if base["acme"].API != "https://public.example.com/v1" {
		t.Error("ApplyOverlays mutated its input catalog")
	}
}

// TestApplyOverlaysPrunesToWhitelist: opencode.ai's /api/config returns a
// `whitelist` alongside its model map — confirmed against the real
// account's data that the two are always the same set, i.e. the server
// already filters `models` to the account's entitlement. Without pruning
// the merged catalog to that whitelist, the picker offered every
// publicly-listed model regardless of what this account can actually use,
// which is indistinguishable from a bug until a non-entitled model is
// tried and the inference gateway rejects it.
func TestApplyOverlaysPrunesToWhitelist(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := modelsdev.Catalog{
		"acme": {
			ID:   "acme",
			Name: "Acme",
			Models: map[string]modelsdev.Model{
				"public-only":  {ID: "public-only", Name: "Not entitled"},
				"entitled-one": {ID: "entitled-one", Name: "Public name"},
			},
		},
	}
	before := len(registry)
	Register(stubOverlay{byID: byID{"zen-stub"}, catalog: modelsdev.Catalog{
		"acme": {
			Whitelist: []string{"entitled-one", "entitled-two"},
			Models: map[string]modelsdev.Model{
				"entitled-two": {ID: "entitled-two", Name: "Org-only entitlement"},
			},
		},
	}})
	t.Cleanup(func() { registry = registry[:before] })

	merged := ApplyOverlays(context.Background(), base)

	acme := merged["acme"]
	if _, ok := acme.Models["public-only"]; ok {
		t.Error("a model outside the whitelist survived pruning")
	}
	if got, ok := acme.Models["entitled-one"]; !ok || got.Name != "Public name" {
		t.Errorf("entitled-one = %+v, ok=%v, want the whitelisted public model kept", got, ok)
	}
	if _, ok := acme.Models["entitled-two"]; !ok {
		t.Error("a whitelisted model the overlay itself defines must survive")
	}
	if len(acme.Models) != 2 {
		t.Errorf("models = %v, want exactly the 2 whitelisted entries", acme.Models)
	}
}

// TestApplyOverlaysSurvivesFailure: losing the per-account catalog must not
// take the public one down with it.
func TestApplyOverlaysSurvivesFailure(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := modelsdev.Catalog{"acme": {ID: "acme", Name: "Acme"}}
	before := len(registry)
	Register(stubOverlay{byID: byID{"zen-stub"}, err: context.DeadlineExceeded})
	t.Cleanup(func() { registry = registry[:before] })

	merged := ApplyOverlays(context.Background(), base)
	if len(merged) != 1 || merged["acme"].Name != "Acme" {
		t.Errorf("merged = %v, want the base catalog unchanged", merged)
	}
}

// TestApplyOverlaysNoopWithoutCredential is the common path: nobody has a Zen
// account, so nothing should change and nothing should be fetched.
func TestApplyOverlaysNoopWithoutCredential(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOCODE_AUTH_CONTENT", "")
	base := modelsdev.Catalog{"acme": {ID: "acme", Name: "Acme"}}
	merged := ApplyOverlays(context.Background(), base)
	if len(merged) != 1 {
		t.Errorf("merged = %v, want the base catalog", merged)
	}
}
