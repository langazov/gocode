package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

func testCatalog() modelsdev.Catalog {
	return modelsdev.Catalog{
		"anthropic": {
			Name: "Anthropic", Env: []string{"ANTHROPIC_API_KEY"},
			Models: map[string]modelsdev.Model{"claude": {ID: "claude", Name: "Claude"}},
		},
		"openai": {
			Name: "OpenAI", Env: []string{"OPENAI_API_KEY"},
			Models: map[string]modelsdev.Model{"gpt": {ID: "gpt", Name: "GPT"}},
		},
		"cohere": {
			Name: "Cohere", Env: []string{"CO_API_KEY"},
			Models: map[string]modelsdev.Model{"command": {ID: "command", Name: "Command"}},
		},
		// No env names at all: reachable only by the {PROVIDER}_API_KEY
		// convention ResolveAPIKey also assumes.
		"bespoke": {
			Name:   "Bespoke",
			Models: map[string]modelsdev.Model{"one": {ID: "one", Name: "One"}},
		},
	}
}

// isolate points auth.json at an empty temp dir so a developer's real
// credentials never decide a test's outcome.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "CO_API_KEY", "BESPOKE_API_KEY"} {
		t.Setenv(name, "")
	}
}

func availabilityOf(t *testing.T, cfg *config.Config) map[string]bool {
	t.Helper()
	a := newProviderAvailability(cfg)
	out := map[string]bool{}
	for id, entry := range testCatalog() {
		out[id] = a.available(id, entry)
	}
	return out
}

// The catalog is the database, not the list: with no credentials anywhere,
// nothing is offered.
func TestNoCredentialsMeansNoProviders(t *testing.T) {
	isolate(t)
	for id, ok := range availabilityOf(t, nil) {
		if ok {
			t.Fatalf("%s should not be available without any credential", id)
		}
	}
}

// source: "env" — any of the catalog's env names.
func TestEnvKeyMakesAProviderAvailable(t *testing.T) {
	isolate(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	got := availabilityOf(t, nil)
	if !got["anthropic"] {
		t.Fatal("an env key should make the provider available")
	}
	if got["openai"] || got["cohere"] {
		t.Fatalf("only the configured provider is available, got %v", got)
	}
}

// A provider whose catalog entry names no env var still honours the
// conventional {PROVIDER}_API_KEY.
func TestConventionalEnvNameIsHonoured(t *testing.T) {
	isolate(t)
	t.Setenv("BESPOKE_API_KEY", "sk-test")
	if !availabilityOf(t, nil)["bespoke"] {
		t.Fatal("{PROVIDER}_API_KEY should make a provider with no catalog env available")
	}
}

// source: "api" — anything stored by `opencode auth login`.
func TestStoredAuthMakesAProviderAvailable(t *testing.T) {
	isolate(t)
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"openai":{"type":"api","key":"sk-stored"}}`)

	got := availabilityOf(t, nil)
	if !got["openai"] {
		t.Fatal("a stored api key should make the provider available")
	}
	if got["anthropic"] {
		t.Fatal("other providers stay unavailable")
	}
}

func TestStoredOAuthMakesAProviderAvailable(t *testing.T) {
	isolate(t)
	t.Setenv("OPENCODE_AUTH_CONTENT",
		`{"anthropic":{"type":"oauth","access":"tok","refresh":"r","expires":99999999999999}}`)
	if !availabilityOf(t, nil)["anthropic"] {
		t.Fatal("an oauth entry should make the provider available")
	}
}

// source: "config" — a declared provider is listed even with no key, because
// its options may carry a baseURL to a gateway that needs none.
func TestConfigDeclaredProviderIsAvailableWithoutAKey(t *testing.T) {
	isolate(t)
	cfg := &config.Config{Provider: map[string]config.Provider{"cohere": {Name: "Cohere"}}}
	if !availabilityOf(t, cfg)["cohere"] {
		t.Fatal("a config-declared provider should be available")
	}
}

// The config gates still apply on top of the credential check.
func TestDisabledAndEnabledProviderGatesStillApply(t *testing.T) {
	isolate(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("OPENAI_API_KEY", "sk-test")

	disabled := &config.Config{DisabledProviders: []string{"anthropic"}}
	if availabilityOf(t, disabled)["anthropic"] {
		t.Fatal("disabled_providers should win over a present key")
	}
	if !availabilityOf(t, disabled)["openai"] {
		t.Fatal("other credentialed providers stay available")
	}

	allowlist := &config.Config{EnabledProviders: []string{"openai"}}
	if availabilityOf(t, allowlist)["anthropic"] {
		t.Fatal("enabled_providers is an allowlist")
	}
	if !availabilityOf(t, allowlist)["openai"] {
		t.Fatal("the allowlisted provider stays available")
	}
}

// --- the endpoints -----------------------------------------------------------

func serveWith(t *testing.T, cfg *config.Config, path string, out any) {
	t.Helper()
	catalog := testCatalog()
	server := &Server{Config: cfg, Models: modelsdev.NewWithCatalog(catalog)}
	request := httptest.NewRequest(http.MethodGet, path, nil)
	recorder := httptest.NewRecorder()
	server.Mux().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s = %d: %s", path, recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

// The user-visible effect: the model dialog offers only reachable models
// instead of the entire models.dev catalog.
func TestModelListOnlyOffersReachableProviders(t *testing.T) {
	isolate(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	var models []modelEntry
	serveWith(t, nil, "/api/model", &models)
	if len(models) != 1 || models[0].ProviderID != "anthropic" || models[0].ID != "claude" {
		t.Fatalf("expected only the anthropic model, got %+v", models)
	}
}

func TestModelListIsEmptyWithoutCredentials(t *testing.T) {
	isolate(t)
	var models []modelEntry
	serveWith(t, nil, "/api/model", &models)
	if len(models) != 0 {
		t.Fatalf("expected no models, got %+v", models)
	}
}

func TestProviderListMatchesTheSameRule(t *testing.T) {
	isolate(t)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	var providers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	serveWith(t, nil, "/api/provider", &providers)
	if len(providers) != 1 || providers[0].ID != "openai" {
		t.Fatalf("expected only openai, got %+v", providers)
	}
}

// A provider declared only in config exists nowhere in the catalog, so the
// provider list has to add it explicitly.
func TestConfigOnlyProviderIsListed(t *testing.T) {
	isolate(t)
	cfg := &config.Config{Provider: map[string]config.Provider{
		"gateway": {Name: "Corp Gateway", Models: map[string]config.Model{"m1": {Name: "M1"}}},
	}}

	var providers []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	serveWith(t, cfg, "/api/provider", &providers)
	if len(providers) != 1 || providers[0].ID != "gateway" || providers[0].Name != "Corp Gateway" {
		t.Fatalf("expected the config-only provider, got %+v", providers)
	}

	var models []modelEntry
	serveWith(t, cfg, "/api/model", &models)
	if len(models) != 1 || models[0].ProviderID != "gateway" {
		t.Fatalf("expected the config provider's model, got %+v", models)
	}
}
