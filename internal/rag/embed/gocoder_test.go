package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/gocoder"
)

// fakeGocoderSite serves the public provider list with the given upstreams
// active, and stores an account pointing at it (with a trailing slash, as a
// user-typed URL might have).
func fakeGocoderSite(t *testing.T, providers ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/embeddings/providers" {
			http.NotFound(w, r)
			return
		}
		var out struct {
			Providers []gocoder.EmbeddingProvider `json:"providers"`
		}
		for _, name := range providers {
			out.Providers = append(out.Providers, gocoder.EmbeddingProvider{Name: name})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	withGocoderAccount(t, &gocoder.Account{URL: srv.URL + "/", Key: "gk_test"})
	return srv
}

// withGocoderAccount points the account store at a temp dir, optionally
// seeding it, so no test ever sees the real machine's sign-in.
func withGocoderAccount(t *testing.T, account *gocoder.Account) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if account != nil {
		if err := gocoder.SaveAccount(account); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResolveDefaultsToGocoderWhenSignedIn(t *testing.T) {
	srv := fakeGocoderSite(t, "openai", "openrouter")

	client, err := Resolve(context.Background(), Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.BaseURL != srv.URL+"/api" || client.APIKey != "gk_test" {
		t.Fatalf("endpoint = %q key = %q", client.BaseURL, client.APIKey)
	}
	if client.Provider != "openai" || client.Model != "text-embedding-3-small" || client.BatchSize != gocoderMaxBatch {
		t.Fatalf("client = %+v", client)
	}
}

// The provider set gocoder.org actually runs with: no direct OpenAI key, so
// OpenAI's model is reached through OpenRouter.
func TestResolveGocoderFallsBackToOpenRouter(t *testing.T) {
	fakeGocoderSite(t, "mistral", "nvidia", "openrouter")

	client, err := Resolve(context.Background(), Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.Provider != "openrouter" || client.Model != "openai/text-embedding-3-small" {
		t.Fatalf("client = %+v", client)
	}
}

func TestResolveGocoderRefusesOtherModelFamilies(t *testing.T) {
	fakeGocoderSite(t, "mistral", "nvidia")

	_, err := Resolve(context.Background(), Config{}, nil)
	if err == nil || !strings.Contains(err.Error(), "active: mistral, nvidia") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveGocoderRespectsExplicitChoice(t *testing.T) {
	fakeGocoderSite(t, "openai")
	ctx := context.Background()

	for _, cfg := range []Config{{Provider: "openai"}, {BaseURL: "http://localhost:1234/v1"}} {
		if client, ok, err := resolveGocoder(ctx, cfg); ok || err != nil || client != nil {
			t.Errorf("resolveGocoder(%+v) = %v, %v, %v; want fall-through", cfg, client, ok, err)
		}
	}

	client, ok, err := resolveGocoder(ctx, Config{Provider: GocoderProvider, Model: "openai/text-embedding-3-large"})
	if !ok || err != nil || client.Model != "text-embedding-3-large" {
		t.Fatalf("explicit gocoder = %+v, %v, %v", client, ok, err)
	}
}

func TestResolveGocoderWithoutAccount(t *testing.T) {
	withGocoderAccount(t, nil)

	if _, ok, err := resolveGocoder(context.Background(), Config{}); ok || err != nil {
		t.Fatalf("no account should fall through to openai, got ok=%v err=%v", ok, err)
	}
	_, err := Resolve(context.Background(), Config{Provider: GocoderProvider}, nil)
	if err == nil || !strings.Contains(err.Error(), "no gocoder.org account") {
		t.Fatalf("explicit gocoder without account: err = %v", err)
	}
}

func TestEmbedGocoderWireFormat(t *testing.T) {
	var path, auth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"provider":"openai","model":"m","dim":2,"vectors":[[1,0],[0,1]],"elapsedMs":"3ms"}`))
	}))
	defer srv.Close()

	client := New(srv.URL+"/api", "gk_x", "m")
	client.Provider = "openai"
	vectors, err := client.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/embeddings" || auth != "Bearer gk_x" || body["provider"] != "openai" {
		t.Fatalf("request path=%q auth=%q body=%v", path, auth, body)
	}
	if len(vectors) != 2 || vectors[0][0] != 1 || vectors[1][1] != 1 {
		t.Fatalf("vectors = %v", vectors)
	}
}

func TestEmbedGocoderErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"invalid or expired token"}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL+"/api", "gk_revoked", "m").Embed(context.Background(), []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "invalid or expired token") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmbedOmitsProviderForOpenAICompatible(t *testing.T) {
	var raw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		_, _ = w.Write([]byte(`{"data":[{"embedding":[1],"index":0}]}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "k", "m").Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if _, sent := raw["provider"]; sent {
		t.Fatalf("OpenAI-compatible request carried a provider field: %v", raw)
	}
}
