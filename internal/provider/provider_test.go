package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/langazov/gocode-go/internal/modelsdev"
)

const fixture = `{
  "anthropic": {
    "id": "anthropic",
    "name": "Anthropic",
    "env": ["ANTHROPIC_API_KEY"],
    "models": {}
  }
}`

func newCatalog(t *testing.T) *modelsdev.Service {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("GOCODE_MODELS_PATH", "")
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fixture))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GOCODE_MODELS_URL", srv.URL)
	return modelsdev.New()
}

func TestResolveKeyFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-env")
	service := New(newCatalog(t))
	key, err := service.ResolveAPIKey(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-ant-env" {
		t.Fatalf("expected env key, got %s", key)
	}
}

func TestResolveKeyMissing(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("GOCODE_AUTH_CONTENT", "")
	service := New(newCatalog(t))
	if _, err := service.ResolveAPIKey(context.Background(), "anthropic"); err == nil {
		t.Fatal("expected error when no credentials available")
	}
}

func TestResolveKeyFromAuthStore(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GOCODE_AUTH_CONTENT", `{"anthropic":{"type":"api","key":"sk-ant-stored"}}`)
	service := New(newCatalog(t))
	key, err := service.ResolveAPIKey(context.Background(), "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if key != "sk-ant-stored" {
		t.Fatalf("expected stored key, got %s", key)
	}
}

func TestResolveKeyUnknownProvider(t *testing.T) {
	service := New(newCatalog(t))
	if _, err := service.ResolveAPIKey(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}
