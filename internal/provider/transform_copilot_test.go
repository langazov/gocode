package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

// copilotAuth writes an auth.json holding a Copilot OAuth entry, isolated to
// this test's XDG data dir.
func copilotAuth(t *testing.T, entry map[string]any) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	payload, err := json.Marshal(map[string]any{"github-copilot": entry})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "auth.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCopilotUsesStoredTokenAndHeaders(t *testing.T) {
	copilotAuth(t, map[string]any{"type": "oauth", "access": "gho_a", "refresh": "gho_r"})

	r := &Resolved{ID: "github-copilot", Protocol: ProtocolOpenAI}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	// TS reads auth.refresh for the bearer token, not auth.access.
	if r.APIKey != "gho_r" {
		t.Errorf("APIKey = %q, want the refresh token", r.APIKey)
	}
	if r.BaseURL != copilotDefaultAPI {
		t.Errorf("baseURL = %q, want %q", r.BaseURL, copilotDefaultAPI)
	}
	for key, want := range map[string]string{
		"X-GitHub-Api-Version": copilotAPIVersion,
		"Openai-Intent":        "conversation-edits",
		"x-initiator":          "user",
	} {
		if got := r.Options.Headers[key]; got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
}

// TestCopilotEnterpriseBaseURL: an enterprise install is reached through a
// derived copilot-api host, not the public endpoint.
func TestCopilotEnterpriseBaseURL(t *testing.T) {
	copilotAuth(t, map[string]any{
		"type": "oauth", "access": "a", "refresh": "r",
		"enterpriseUrl": "company.ghe.com",
	})
	r := &Resolved{ID: "github-copilot", Protocol: ProtocolOpenAI}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if want := "https://copilot-api.company.ghe.com"; r.BaseURL != want {
		t.Errorf("baseURL = %q, want %q", r.BaseURL, want)
	}
}

func TestCopilotNormalizeDomain(t *testing.T) {
	cases := map[string]string{
		"company.ghe.com":          "company.ghe.com",
		"https://company.ghe.com":  "company.ghe.com",
		"http://company.ghe.com/":  "company.ghe.com",
		"https://company.ghe.com/": "company.ghe.com",
		"":                         "",
	}
	for input, want := range cases {
		if got := normalizeDomain(input); got != want {
			t.Errorf("normalizeDomain(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestCopilotFetchModelsFilters checks the live list replaces the catalog's
// and drops models the picker hides or that cannot call tools.
func TestCopilotFetchModelsFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("Authorization = %q, want the bearer token", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-GitHub-Api-Version") != copilotAPIVersion {
			t.Errorf("missing API version header")
		}
		w.Write([]byte(`{"data":[
			{"id":"good","name":"Good","model_picker_enabled":true,
			 "capabilities":{"family":"gpt","limits":{"max_context_window_tokens":128000,"max_output_tokens":16000},
			                 "supports":{"tool_calls":true,"vision":true,"reasoning_effort":["low","high"]}}},
			{"id":"hidden","name":"Hidden","model_picker_enabled":false,
			 "capabilities":{"family":"gpt","limits":{},"supports":{"tool_calls":true}}},
			{"id":"no-tools","name":"No Tools","model_picker_enabled":true,
			 "capabilities":{"family":"gpt","limits":{},"supports":{"tool_calls":false}}}
		]}`))
	}))
	defer server.Close()

	models, err := copilotModels(context.Background(), server.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want only the picker-enabled tool-calling one: %v", len(models), models)
	}
	model, ok := models["good"]
	if !ok {
		t.Fatal("expected the 'good' model to survive filtering")
	}
	if model.Limit.Context != 128000 || model.Limit.Output != 16000 {
		t.Errorf("limits = %+v, want context 128000 / output 16000", model.Limit)
	}
	if !model.Reasoning {
		t.Error("a model advertising reasoning_effort should be marked reasoning-capable")
	}
	if !model.Attachment {
		t.Error("a vision model should be marked attachment-capable")
	}
}

// TestCopilotLiveModelsFallsBackToCatalog: a failed fetch must leave the
// catalog's models in place rather than emptying the picker.
func TestCopilotLiveModelsFallsBackToCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	r := &Resolved{
		ID:      "github-copilot",
		BaseURL: server.URL,
		APIKey:  "tok",
		Models:  map[string]modelsdev.Model{"from-catalog": {ID: "from-catalog"}},
	}
	got := r.LiveModels(context.Background())
	if _, ok := got["from-catalog"]; !ok {
		t.Errorf("expected the catalog models to survive a failed fetch, got %v", got)
	}
}

// TestCopilotAuthMethodsIncludeOAuth: the transform must contribute a device
// flow on top of the env/key methods every catalog entry gets.
func TestCopilotAuthMethodsIncludeOAuth(t *testing.T) {
	methods := methodsFor("github-copilot", modelsdev.Provider{Env: []string{"GITHUB_TOKEN"}})
	var kinds []string
	for _, method := range methods {
		kinds = append(kinds, method.Type)
	}
	if len(methods) < 3 {
		t.Fatalf("methods = %v, want oauth + env + key", kinds)
	}
	if methods[0].Type != MethodOAuth || methods[0].Login == nil {
		t.Errorf("first method = %+v, want a runnable oauth flow", methods[0])
	}
	var hasEnv, hasKey bool
	for _, method := range methods {
		hasEnv = hasEnv || method.Type == MethodEnv
		hasKey = hasKey || method.Type == MethodKey
	}
	if !hasEnv || !hasKey {
		t.Errorf("methods = %v, want the catalog-derived env and key methods too", kinds)
	}
}

// TestMethodsForCatalogOnlyProvider is the other half: a provider with no
// transform still gets loggable methods purely from its catalog entry, which
// is what makes the ~180 code-less providers work.
func TestMethodsForCatalogOnlyProvider(t *testing.T) {
	methods := methodsFor("some-new-provider", modelsdev.Provider{Env: []string{"SOME_NEW_API_KEY"}})
	if len(methods) != 2 {
		t.Fatalf("got %d methods, want env + key", len(methods))
	}
	if methods[0].Type != MethodEnv || methods[0].Env[0] != "SOME_NEW_API_KEY" {
		t.Errorf("first method = %+v, want an env method naming the catalog's variable", methods[0])
	}
	if methods[1].Type != MethodKey {
		t.Errorf("second method = %+v, want the manual key method", methods[1])
	}
}

func TestEnvSatisfied(t *testing.T) {
	t.Setenv("TEST_PROVIDER_KEY", "")
	method := Method{Type: MethodEnv, Env: []string{"TEST_PROVIDER_KEY"}}
	if method.EnvSatisfied() {
		t.Error("EnvSatisfied should be false when the variable is unset")
	}
	t.Setenv("TEST_PROVIDER_KEY", "value")
	if !method.EnvSatisfied() {
		t.Error("EnvSatisfied should be true once the variable is set")
	}
}

// TestCopilotModelCacheAvoidsRefetch is the regression for "the dialogs are
// shown with delays": the model list is fetched over the network, and
// /api/model is on the interface's hot path — opening the model dialog
// measured 140ms+ with a copilot credential against ~3ms without one.
func TestCopilotModelCacheAvoidsRefetch(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"data":[{"id":"m","name":"M","model_picker_enabled":true,
			"capabilities":{"family":"gpt","limits":{},"supports":{"tool_calls":true}}}]}`))
	}))
	defer server.Close()

	copilotModelCache.invalidate()
	t.Cleanup(copilotModelCache.invalidate)

	for i := 0; i < 5; i++ {
		models, err := copilotModelCache.get(context.Background(), server.URL, "tok")
		if err != nil {
			t.Fatal(err)
		}
		if len(models) != 1 {
			t.Fatalf("call %d returned %d models, want 1", i, len(models))
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("hit the network %d times, want 1 — the list must be memoized", got)
	}
}

// TestCopilotModelCacheKeysOnCredential: a different token is a different
// account, and must not be served the first account's models.
func TestCopilotModelCacheKeysOnCredential(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	copilotModelCache.invalidate()
	t.Cleanup(copilotModelCache.invalidate)

	copilotModelCache.get(context.Background(), server.URL, "token-a")
	copilotModelCache.get(context.Background(), server.URL, "token-b")
	if got := calls.Load(); got != 2 {
		t.Errorf("made %d requests for two different tokens, want 2", got)
	}
}

// TestCopilotModelCacheMemoizesFailure: an unusable credential otherwise costs
// a slow failing round trip on every dialog open.
func TestCopilotModelCacheMemoizesFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	copilotModelCache.invalidate()
	t.Cleanup(copilotModelCache.invalidate)

	for i := 0; i < 3; i++ {
		if _, err := copilotModelCache.get(context.Background(), server.URL, "bad"); err == nil {
			t.Fatal("expected the failure to surface")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("retried a failing credential %d times, want 1", got)
	}
}

// TestInvalidateModelCaches: after a login the previous account's models must
// not linger.
func TestInvalidateModelCaches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	copilotModelCache.invalidate()
	t.Cleanup(copilotModelCache.invalidate)

	copilotModelCache.get(context.Background(), server.URL, "tok")
	InvalidateModelCaches()
	copilotModelCache.get(context.Background(), server.URL, "tok")
	if got := calls.Load(); got != 2 {
		t.Errorf("made %d requests, want 2 — invalidation must force a refetch", got)
	}
}
