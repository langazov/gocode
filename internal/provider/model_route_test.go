package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/llm/anthropic"
	"github.com/langazov/gocode-go/internal/llm/gemini"
	"github.com/langazov/gocode-go/internal/llm/openai"
	"github.com/langazov/gocode-go/internal/llm/openairesponses"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

func TestModelsNeedRouting(t *testing.T) {
	cases := []struct {
		name   string
		models map[string]modelsdev.Model
		want   bool
	}{
		{"empty", map[string]modelsdev.Model{}, false},
		{"no overrides", map[string]modelsdev.Model{"m": {ID: "m"}}, false},
		{"one override", map[string]modelsdev.Model{
			"m": {ID: "m", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/anthropic"}},
		}, true},
		{"override with empty npm is not routing", map[string]modelsdev.Model{
			"m": {ID: "m", Provider: &modelsdev.ProviderOverride{}},
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modelsNeedRouting(tc.models); got != tc.want {
				t.Errorf("modelsNeedRouting = %v, want %v", got, tc.want)
			}
		})
	}
}

// respondingServer answers any request with a minimal valid stream for the
// given protocol and records that it was hit, so the test can assert which
// server a given model's request actually reached.
func respondingServer(t *testing.T, body string) (*httptest.Server, *bool) {
	t.Helper()
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hit
}

// TestModelRoutedClientDispatchesByOverride is the core of opencode/Zen's
// fix: a single provider whose catalog mixes plain OpenAI-compatible models
// with Claude/Gemini/GPT-5-family overrides must send each model's request
// to its own protocol's endpoint, not the provider's default one.
func TestModelRoutedClientDispatchesByOverride(t *testing.T) {
	defaultSrv, defaultHit := respondingServer(t, "data: [DONE]\n\n")
	anthropicSrv, anthropicHit := respondingServer(t, `data: {"type":"message_stop"}`+"\n\n")
	geminiSrv, geminiHit := respondingServer(t, "data: {}\n\n")
	responsesSrv, responsesHit := respondingServer(t, "data: [DONE]\n\n")

	resolved := &Resolved{
		ID:       "opencode",
		Protocol: ProtocolOpenAI,
		BaseURL:  defaultSrv.URL,
		APIKey:   "test-key",
		Models: map[string]modelsdev.Model{
			"plain-model":     {ID: "plain-model"},
			"claude-model":    {ID: "claude-model", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/anthropic", API: anthropicSrv.URL}},
			"gemini-model":    {ID: "gemini-model", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/google", API: geminiSrv.URL}},
			"responses-model": {ID: "responses-model", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/openai", API: responsesSrv.URL}},
		},
	}

	client, err := resolved.Client()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*modelRoutedClient); !ok {
		t.Fatalf("Client() = %T, want a *modelRoutedClient since Models declares overrides", client)
	}

	for _, tc := range []struct {
		modelID string
		hit     *bool
		others  []*bool
	}{
		{"plain-model", defaultHit, []*bool{anthropicHit, geminiHit, responsesHit}},
		{"claude-model", anthropicHit, []*bool{defaultHit, geminiHit, responsesHit}},
		{"gemini-model", geminiHit, []*bool{defaultHit, anthropicHit, responsesHit}},
		{"responses-model", responsesHit, []*bool{defaultHit, anthropicHit, geminiHit}},
	} {
		t.Run(tc.modelID, func(t *testing.T) {
			*defaultHit, *anthropicHit, *geminiHit, *responsesHit = false, false, false, false
			err := client.Stream(context.Background(), llm.Request{ModelID: tc.modelID, Messages: []llm.Message{llm.UserText("1", "hi")}}, func(llm.StreamEvent) {})
			if err != nil {
				t.Fatal(err)
			}
			if !*tc.hit {
				t.Errorf("%s: expected its own server to be hit", tc.modelID)
			}
			for _, other := range tc.others {
				if *other {
					t.Errorf("%s: a different model's server was hit instead", tc.modelID)
				}
			}
		})
	}
}

// TestModelRoutedClientFallsBackToProviderBaseURL: opencode/Zen's GPT-5
// family and friends carry no `api` of their own — just the npm package —
// so the routed client must fall back to the provider's own base URL
// (Zen's own inference endpoint) rather than the SDK's public default.
func TestModelRoutedClientFallsBackToProviderBaseURL(t *testing.T) {
	srv, hit := respondingServer(t, "data: [DONE]\n\n")
	resolved := &Resolved{
		ID:       "opencode",
		Protocol: ProtocolOpenAI,
		BaseURL:  srv.URL,
		APIKey:   "test-key",
		Models: map[string]modelsdev.Model{
			"gpt-5": {ID: "gpt-5", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/openai"}}, // no API
		},
	}
	client, err := resolved.Client()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5", Messages: []llm.Message{llm.UserText("1", "hi")}}, func(llm.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	if !*hit {
		t.Error("expected the provider's own base URL to be used when the override has no api of its own")
	}
}

// TestModelRoutedClientCarriesResolvedOptions: the account's own headers
// (x-opencode-org-id, User-Agent — see transform_opencode.go) must reach a
// routed model's request too, not just the provider's default client.
func TestModelRoutedClientCarriesResolvedOptions(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("x-opencode-org-id")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	resolved := &Resolved{
		ID:       "opencode",
		Protocol: ProtocolOpenAI,
		BaseURL:  "https://unused.example.com",
		APIKey:   "test-key",
		Models: map[string]modelsdev.Model{
			"gpt-5": {ID: "gpt-5", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/openai", API: srv.URL}},
		},
	}
	resolved.Header("x-opencode-org-id", "org-123")

	client, err := resolved.Client()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Stream(context.Background(), llm.Request{ModelID: "gpt-5", Messages: []llm.Message{llm.UserText("1", "hi")}}, func(llm.StreamEvent) {}); err != nil {
		t.Fatal(err)
	}
	if gotHeader != "org-123" {
		t.Errorf("x-opencode-org-id = %q, want org-123", gotHeader)
	}
}

func TestModelRoutedClientUnsupportedSDK(t *testing.T) {
	resolved := &Resolved{
		ID:       "opencode",
		Protocol: ProtocolOpenAI,
		BaseURL:  "https://unused.example.com",
		APIKey:   "test-key",
		Models: map[string]modelsdev.Model{
			"weird": {ID: "weird", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/mystery-vendor"}},
		},
	}
	client, err := resolved.Client()
	if err != nil {
		t.Fatal(err)
	}
	var gotErr error
	err = client.Stream(context.Background(), llm.Request{ModelID: "weird"}, func(e llm.StreamEvent) {
		if e.Type == llm.EventProviderError {
			gotErr = e.Error
		}
	})
	if err == nil || gotErr == nil {
		t.Fatal("expected an error for an unsupported SDK package")
	}
}

// TestModelRoutedClientCachesPerOverride confirms clientFor reuses a client
// for repeated requests to the same override rather than reconnecting (and
// re-authenticating headers) on every single message.
func TestModelRoutedClientCachesPerOverride(t *testing.T) {
	srv, _ := respondingServer(t, "data: [DONE]\n\n")
	resolved := &Resolved{
		ID:      "opencode",
		BaseURL: "https://unused.example.com",
		APIKey:  "test-key",
		Models: map[string]modelsdev.Model{
			"gpt-5": {ID: "gpt-5", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/openai", API: srv.URL}},
		},
	}
	routed := &modelRoutedClient{resolved: resolved}
	first, err := routed.clientFor(resolved.Models["gpt-5"].Provider)
	if err != nil {
		t.Fatal(err)
	}
	second, err := routed.clientFor(resolved.Models["gpt-5"].Provider)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("clientFor built a new client for the same override instead of reusing the cached one")
	}
}

// Sanity checks that each branch of clientFor actually builds the client
// type its comment claims, with the override's base URL and the resolved
// provider's own credential.
func TestModelRoutedClientBuildsExpectedClientTypes(t *testing.T) {
	resolved := &Resolved{ID: "opencode", BaseURL: "https://unused.example.com", APIKey: "test-key"}
	cases := []struct {
		npm  string
		want any
	}{
		{"@ai-sdk/anthropic", &anthropic.Client{}},
		{"@ai-sdk/google", &gemini.Client{}},
		{"@ai-sdk/openai", &openairesponses.Client{}},
		{"@ai-sdk/openai-compatible", &openai.Client{}},
	}
	for _, tc := range cases {
		t.Run(tc.npm, func(t *testing.T) {
			routed := &modelRoutedClient{resolved: resolved}
			client, err := routed.clientFor(&modelsdev.ProviderOverride{NPM: tc.npm, API: "https://example.com"})
			if err != nil {
				t.Fatal(err)
			}
			if got := clientTypeName(client); got != clientTypeName(tc.want) {
				t.Errorf("npm %q built %s, want %s", tc.npm, got, clientTypeName(tc.want))
			}
		})
	}
}

func clientTypeName(v any) string {
	switch v.(type) {
	case *anthropic.Client:
		return "anthropic.Client"
	case *gemini.Client:
		return "gemini.Client"
	case *openairesponses.Client:
		return "openairesponses.Client"
	case *openai.Client:
		return "openai.Client"
	default:
		return "unknown"
	}
}
