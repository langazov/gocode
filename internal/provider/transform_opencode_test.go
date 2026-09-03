package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/langazov/gocode-go/internal/modelsdev"
)

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

// TestApplyOverlaysSurvivesFailure: losing the per-account catalog must not
// take the public one down with it.
func TestApplyOverlaysSurvivesFailure(t *testing.T) {
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
