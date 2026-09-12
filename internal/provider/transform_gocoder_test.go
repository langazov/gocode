package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
)

// gocoderFixture writes a signed-in gocoder.json into an isolated data dir
// and serves the free-models endpoint.
func gocoderFixture(t *testing.T, signedIn bool, models ...string) (*httptest.Server, func()) {
	t.Helper()
	isolated := t.TempDir()
	t.Setenv("GOCODE_TEST_HOME", isolated)
	t.Setenv("XDG_DATA_HOME", isolated)
	t.Setenv("GOCODE_INFERENCE_URL", "")

	var account string
	if signedIn {
		account = `{"url":"` + "" + `","userId":"u1","email":"a@b.c","displayName":"A",
			"keyId":"k1","keyPrefix":"gk_ab12","key":"gk_testsecret","createdAt":"2026-09-12T00:00:00Z"}`
		account = strings.ReplaceAll(account, "\n", "")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/inference/models/free" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer gk_testsecret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"missing Bearer token"}}`))
			return
		}
		list := make([]map[string]any, 0, len(models))
		for i, id := range models {
			list = append(list, map[string]any{
				"id": id, "name": "Model " + string(rune('A'+i)),
				"contextLength": 32768, "modality": "text->text", "free": true,
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": list, "count": len(list)})
	}))
	t.Cleanup(srv.Close)

	if signedIn {
		// account URL points at the fake site
		fixed := strings.Replace(account, `"url":""`, `"url":"`+srv.URL+`"`, 1)
		path := filepath.Join(global.Resolve().Data, "gocoder.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(fixed), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return srv, func() { _ = os.Remove(filepath.Join(global.Resolve().Data, "gocoder.json")) }
}

func TestGocoderOverlaySignedOutIsEmpty(t *testing.T) {
	gocoderFixture(t, false)
	catalog, err := gocoderTransform{}.Overlay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 0 {
		t.Fatalf("signed-out overlay = %+v, want empty", catalog)
	}
}

func TestGocoderOverlayContributesCatalogEntry(t *testing.T) {
	_, cleanup := gocoderFixture(t, true)
	defer cleanup()
	catalog, err := gocoderTransform{}.Overlay(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := catalog["gocoder"]
	if !ok {
		t.Fatal("overlay missing gocoder entry")
	}
	if entry.Name != "gocoder.org" || Protocol(entry.NPM) != ProtocolOpenAI {
		t.Errorf("entry = %+v", entry)
	}
}

func TestGocoderApplySetsKeyAndBaseURL(t *testing.T) {
	srv, cleanup := gocoderFixture(t, true, "x/y:free")
	defer cleanup()

	r := &Resolved{ID: "gocoder"}
	if err := (gocoderTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.APIKey != "gk_testsecret" {
		t.Errorf("APIKey = %q", r.APIKey)
	}
	if !strings.HasPrefix(r.BaseURL, srv.URL+"/v1") {
		t.Errorf("BaseURL = %q (want local site /v1)", r.BaseURL)
	}
}

// The base URL must carry the /v1 segment: the OpenAI client appends
// "/chat/completions" to it verbatim, so a bare host 404s at the proxy.
// This is a regression pin for the exact bug that shipped 404s.
func TestGocoderProductionBaseURLHasV1(t *testing.T) {
	if !strings.HasSuffix(gocoder.InferenceBaseURL(), "/v1") {
		t.Fatalf("InferenceBaseURL = %q, must end in /v1", gocoder.InferenceBaseURL())
	}
}

func TestGocoderApplyWithoutAccountLeavesNoKey(t *testing.T) {
	gocoderFixture(t, false)
	r := &Resolved{ID: "gocoder"}
	if err := (gocoderTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.APIKey != "" {
		t.Errorf("APIKey = %q, want empty when signed out", r.APIKey)
	}
}

func TestGocoderFetchModelsListsFreeModels(t *testing.T) {
	_, cleanup := gocoderFixture(t, true, "a/one:free", "b/two:free")
	defer cleanup()
	// reset cache between tests
	gocoderModelCache.entries = map[string]gocoderCacheEntry{}

	models, err := (gocoderTransform{}).FetchModels(context.Background(), &Resolved{ID: "gocoder"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %d (%+v)", len(models), models)
	}
	m := models["a/one:free"]
	if m.Name != "Model A" || !m.ToolCall || m.Limit.Context != 32768 {
		t.Errorf("mapped model = %+v", m)
	}
}

func TestGocoderFetchModelsRequiresAuth(t *testing.T) {
	gocoderFixture(t, false)
	if _, err := (gocoderTransform{}).FetchModels(context.Background(), &Resolved{ID: "gocoder"}); err == nil {
		t.Fatal("expected error when not logged in")
	}
}

func TestGocoderFetchModelsSurfacesAuthFailure(t *testing.T) {
	// Signed in, but the site rejects the key: the fetch error must
	// propagate (not silently return an empty list) so LiveModels falls
	// back to the catalog path rather than caching "no models" as success.
	srv, cleanup := gocoderFixture(t, true, "a/one:free")
	defer cleanup()
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"issue a token"}}`))
	})
	gocoderModelCache.entries = map[string]gocoderCacheEntry{}
	if _, err := (gocoderTransform{}).FetchModels(context.Background(), &Resolved{ID: "gocoder"}); err == nil {
		t.Fatal("expected auth failure to propagate")
	}
}

func TestFreeToCatalogMapsModality(t *testing.T) {
	models := freeToCatalog([]gocoder.FreeModel{{
		ID: "v1", Name: "Vision", ContextLength: 8192,
		Modality: "text+image->text", Free: true,
	}})
	if !models["v1"].Attachment {
		t.Error("image modality should set Attachment")
	}
}
