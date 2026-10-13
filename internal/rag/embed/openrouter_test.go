package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/provider"
)

// TestResolveOpenRouterSendsAttributionHeaders: embedding traffic a user's
// own OpenRouter key generates must attribute to the same app entry as their
// chat traffic (openrouter.ai/docs/app-attribution) — without HTTP-Referer,
// OpenRouter creates no app page at all, so the header going missing here
// would silently split one product's usage across two unattributed streams.
func TestResolveOpenRouterSendsAttributionHeaders(t *testing.T) {
	// A user signed in to gocoder.org would take that path instead; clear the
	// machine's account so this resolves the direct provider.
	withGocoderAccount(t, nil)

	t.Setenv("OPENROUTER_API_KEY", "sk-or-test")
	t.Setenv("OPENROUTER_BASE_URL", "")

	var gotReferer, gotTitle, gotCategories string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-OpenRouter-Title")
		gotCategories = r.Header.Get("X-OpenRouter-Categories")
		var out response
		out.Data = append(out.Data, struct {
			Embedding []float32 `json:"embedding"`
			Index     int       `json:"index"`
		}{Embedding: []float32{1}})
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)

	client, err := Resolve(context.Background(), Config{Provider: "openrouter", BaseURL: srv.URL}, provider.New(modelsdev.New()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if gotReferer != "https://gocoder.org" {
		t.Errorf("HTTP-Referer = %q, want https://gocoder.org", gotReferer)
	}
	if gotTitle != "GoCode" {
		t.Errorf("X-OpenRouter-Title = %q, want GoCode", gotTitle)
	}
	if gotCategories != "cli-agent" {
		t.Errorf("X-OpenRouter-Categories = %q, want cli-agent", gotCategories)
	}
}

// TestResolveOpenAIHasNoAttributionHeaders: the headers are OpenRouter's
// vocabulary; a direct OpenAI embeddings client must not send them.
func TestResolveOpenAIHasNoAttributionHeaders(t *testing.T) {
	withGocoderAccount(t, nil)
	t.Setenv("OPENAI_API_KEY", "sk-test")

	client, err := Resolve(context.Background(), Config{Provider: "openai"}, provider.New(modelsdev.New()))
	if err != nil {
		t.Fatal(err)
	}
	if len(client.ExtraHeaders) != 0 {
		t.Errorf("ExtraHeaders = %v, want none for a non-OpenRouter provider", client.ExtraHeaders)
	}
}
