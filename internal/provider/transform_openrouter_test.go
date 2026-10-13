package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// TestOpenRouterAttributionHeadersApplied: applying the transform to a
// resolved openrouter provider must set the three app-attribution headers —
// HTTP-Referer is the one OpenRouter requires before it creates an app page
// at all, the title names it in rankings, and the category files it as a
// terminal coding assistant.
func TestOpenRouterAttributionHeadersApplied(t *testing.T) {
	r := &Resolved{ID: "openrouter", Protocol: ProtocolOpenAI}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"HTTP-Referer":            "https://gocoder.org",
		"X-OpenRouter-Title":      "gocode",
		"X-OpenRouter-Categories": "cli-agent",
	}
	for key, value := range want {
		if got := r.Options.Headers[key]; got != value {
			t.Errorf("%s = %q, want %q", key, got, value)
		}
	}
}

// TestOpenRouterAttributionHeadersReachRequests: the headers set by the
// transform are what the stream client merges into every request, so the
// proof they reach openrouter.ai is the proof the options plumbing works —
// resolve a provider, apply headers, and observe them on the wire.
func TestOpenRouterAttributionHeadersReachRequests(t *testing.T) {
	var referer, title string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer = r.Header.Get("HTTP-Referer")
		title = r.Header.Get("X-OpenRouter-Title")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	r := &Resolved{
		ID:       "openrouter",
		Protocol: ProtocolOpenAI,
		BaseURL:  srv.URL,
		APIKey:   "test-key",
	}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	client, err := r.Client()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Stream(context.Background(), llm.Request{ModelID: "m", Messages: []llm.Message{llm.UserText("1", "hi")}}, func(llm.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	if referer != "https://gocoder.org" {
		t.Errorf("HTTP-Referer = %q, want https://gocoder.org", referer)
	}
	if title != "gocode" {
		t.Errorf("X-OpenRouter-Title = %q, want gocode", title)
	}
}

// TestOpenRouterAttributionHeadersFreshMap: the exported helper returns a
// fresh map per call, so one caller's mutation (an embeddings client adding
// its own header, a test stubbing one out) cannot leak into another's wire
// format.
func TestOpenRouterAttributionHeadersFreshMap(t *testing.T) {
	first := OpenRouterAttributionHeaders()
	first["HTTP-Referer"] = "mutated"
	if got := OpenRouterAttributionHeaders()["HTTP-Referer"]; got != "https://gocoder.org" {
		t.Errorf("HTTP-Referer = %q after another caller mutated its map — helper must return a fresh map", got)
	}
}

// TestOpenRouterDoesNotTouchOtherFields: attribution is the transform's whole
// job — it must not redirect the endpoint, change the protocol, or supply a
// credential. OpenRouter stays a plain catalog provider on every axis but the
// headers.
func TestOpenRouterDoesNotTouchOtherFields(t *testing.T) {
	r := &Resolved{
		ID:       "openrouter",
		Protocol: ProtocolOpenAI,
		BaseURL:  "https://openrouter.ai/api/v1",
		APIKey:   "key",
		Entry:    modelsdev.Provider{API: "https://openrouter.ai/api/v1"},
	}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.BaseURL != "https://openrouter.ai/api/v1" {
		t.Errorf("BaseURL = %q — the transform must not move the endpoint", r.BaseURL)
	}
	if r.Protocol != ProtocolOpenAI {
		t.Errorf("Protocol = %q, want untouched", r.Protocol)
	}
	if r.APIKey != "key" {
		t.Errorf("APIKey = %q, want untouched", r.APIKey)
	}
}

// TestOpenRouterAttributionNotSentToOtherProviders: the headers are
// OpenRouter's vocabulary, not a general convention — sending
// X-OpenRouter-* anywhere else is noise at best. The gocoder.org proxy case
// is covered by construction: it resolves through a different provider id
// whose transforms (gocoderTransform) set none of these.
func TestOpenRouterAttributionNotSentToOtherProviders(t *testing.T) {
	r := &Resolved{ID: "openai", Protocol: ProtocolOpenAI, Config: &config.Provider{}}
	if err := applyTransforms(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	for key := range OpenRouterAttributionHeaders() {
		if _, ok := r.Options.Headers[key]; ok {
			t.Errorf("%s set on provider %q — attribution belongs to openrouter only", key, r.ID)
		}
	}
}
