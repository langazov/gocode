package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/llm/openai"
	"github.com/langazov/gocode-go/internal/llm/openairesponses"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// chatgptTestCredential stores an OAuth credential for the openai provider,
// isolated to this test, and returns a Resolved pointing at the default
// public endpoint the way a plain resolve would.
func chatgptTestCredential(t *testing.T, info auth.Info) *Resolved {
	t.Helper()
	writeAuth(t, map[string]any{"openai": info})
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "")
	return &Resolved{
		ID:       "openai",
		Protocol: ProtocolOpenAI,
		BaseURL:  openai.DefaultBaseURL,
		Entry:    modelsdev.Provider{ID: "openai", NPM: "@ai-sdk/openai"},
	}
}

// signedTestJWT builds an unsigned JWT whose payload carries the given claim,
// the shape auth.JWTClaim parses. Signature verification is out of scope —
// the real flow reads claims for labeling, not authorization.
func signedTestJWT(claim string, value any) string {
	payload, _ := json.Marshal(map[string]any{claim: value})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// TestOpenAIApplyRoutesChatGPTLoginToCodexBackend is the core fix: an OAuth
// credential must leave Apply pointed at the codex backend with the account
// header, speaking the Responses wire protocol — never at api.openai.com as
// a plain bearer, which 401s with "Missing scopes".
func TestOpenAIApplyRoutesChatGPTLoginToCodexBackend(t *testing.T) {
	r := chatgptTestCredential(t, auth.Info{
		Type:      "oauth",
		Access:    "access-token",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_123",
	})
	if err := (openaiTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.BaseURL != chatgptCodexBaseURL {
		t.Errorf("BaseURL = %q, want %q", r.BaseURL, chatgptCodexBaseURL)
	}
	if got := r.Options.Headers["chatgpt-account-id"]; got != "acct_123" {
		t.Errorf("chatgpt-account-id header = %q, want acct_123", got)
	}
	if r.Protocol != ProtocolOpenAIResponses {
		t.Errorf("Protocol = %q, want %q", r.Protocol, ProtocolOpenAIResponses)
	}
	if !r.Options.DropMaxOutputTokens {
		t.Errorf("DropMaxOutputTokens = false, want true: the codex backend 400s on max_output_tokens")
	}
	if r.APIKey != "access-token" {
		t.Errorf("APIKey = %q, want the access token", r.APIKey)
	}
}

// TestOpenAIApplyLeavesAPIKeyUsersAlone: the rewrite is scoped to the
// subscription credential. An API key (stored or from env) keeps the public
// endpoint, Chat Completions, and no extra headers.
func TestOpenAIApplyLeavesAPIKeyUsersAlone(t *testing.T) {
	for name, info := range map[string]auth.Info{
		"stored api key": {Type: "api", Key: "sk-test"},
		"no credential":  {},
	} {
		t.Run(name, func(t *testing.T) {
			r := chatgptTestCredential(t, info)
			if err := (openaiTransform{}).Apply(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			if r.BaseURL != openai.DefaultBaseURL {
				t.Errorf("BaseURL = %q, want untouched default", r.BaseURL)
			}
			if r.Protocol != ProtocolOpenAI {
				t.Errorf("Protocol = %q, want untouched %q", r.Protocol, ProtocolOpenAI)
			}
			if len(r.Options.Headers) != 0 {
				t.Errorf("Headers = %v, want none", r.Options.Headers)
			}
			if r.Options.DropMaxOutputTokens {
				t.Errorf("DropMaxOutputTokens = true for a %s credential, want false", name)
			}
		})
	}
}

// TestOpenAIApplyEnvKeyBeatsStoredSubscription: an env API key is an explicit
// API-billing choice and wins the resolution — a stored ChatGPT login must
// not hijack it to the codex backend, which only accepts subscription tokens.
func TestOpenAIApplyEnvKeyBeatsStoredSubscription(t *testing.T) {
	r := chatgptTestCredential(t, auth.Info{
		Type:      "oauth",
		Access:    "access-token",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_123",
	})
	// The env key the resolution would have picked over the store.
	t.Setenv("OPENAI_API_KEY", "sk-explicit-choice")
	r.APIKey = "sk-explicit-choice"
	if err := (openaiTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.BaseURL != openai.DefaultBaseURL {
		t.Errorf("BaseURL = %q, want the public API: the env key is an explicit billing choice", r.BaseURL)
	}
	if r.Protocol != ProtocolOpenAI {
		t.Errorf("Protocol = %q, want %q", r.Protocol, ProtocolOpenAI)
	}
	if r.APIKey != "sk-explicit-choice" {
		t.Errorf("APIKey = %q, want the env key untouched", r.APIKey)
	}
}

// TestOpenAIApplyRespectsExplicitBaseURL: a user-configured relay must not be
// hijacked to the codex backend — the rewrite only repairs the default
// public endpoint.
func TestOpenAIApplyRespectsExplicitBaseURL(t *testing.T) {
	r := chatgptTestCredential(t, auth.Info{
		Type:      "oauth",
		Access:    "access-token",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_123",
	})
	r.BaseURL = "https://my-relay.example.com/v1"
	if err := (openaiTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.BaseURL != "https://my-relay.example.com/v1" {
		t.Errorf("BaseURL = %q, want the explicit override kept", r.BaseURL)
	}
	if r.Protocol != ProtocolOpenAI {
		t.Errorf("Protocol = %q, want %q — the relay speaks Chat Completions", r.Protocol, ProtocolOpenAI)
	}
}

// TestOpenAIApplyRecoversLegacyAccountID: credentials stored before login
// captured the account id must not force a re-login — the id is still in the
// access token's own claims.
func TestOpenAIApplyRecoversLegacyAccountID(t *testing.T) {
	token := signedTestJWT("chatgpt_account_id", "acct_from_jwt")
	r := chatgptTestCredential(t, auth.Info{
		Type:    "oauth",
		Access:  token,
		Refresh: "refresh-token",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	})
	if err := (openaiTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got := r.Options.Headers["chatgpt-account-id"]; got != "acct_from_jwt" {
		t.Errorf("chatgpt-account-id = %q, want acct_from_jwt", got)
	}
	if r.BaseURL != chatgptCodexBaseURL {
		t.Errorf("BaseURL = %q, want the codex backend", r.BaseURL)
	}
}

// TestOpenAIApplyFailsWithoutAccountID: when neither the stored credential
// nor the access token carries an account id, the failure must be a
// constructive re-login instruction at construction time — not a bare
// runtime 401 with nothing pointing at the cause.
func TestOpenAIApplyFailsWithoutAccountID(t *testing.T) {
	r := chatgptTestCredential(t, auth.Info{
		Type:    "oauth",
		Access:  "not-a-jwt",
		Refresh: "refresh-token",
		Expires: time.Now().Add(time.Hour).UnixMilli(),
	})
	err := (openaiTransform{}).Apply(context.Background(), r)
	if err == nil {
		t.Fatal("Apply succeeded with no account id anywhere, want a re-login error")
	}
	if !strings.Contains(err.Error(), "providers login") {
		t.Errorf("error %q should tell the user to log in again", err)
	}
}

// TestOpenAIApplyClearsModelOverrides: per-model provider overrides carry
// their own endpoints, which modelRoutedClient would honor — routing a model
// back to api.openai.com and around the codex rewrite. Under a subscription
// they must be stripped.
func TestOpenAIApplyClearsModelOverrides(t *testing.T) {
	r := chatgptTestCredential(t, auth.Info{
		Type:      "oauth",
		Access:    "access-token",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_123",
	})
	r.Models = map[string]modelsdev.Model{
		"gpt-5.1": {ID: "gpt-5.1", Provider: &modelsdev.ProviderOverride{NPM: "@ai-sdk/openai", API: "https://api.openai.com/v1"}},
		"gpt-4o":  {ID: "gpt-4o"},
	}
	if err := (openaiTransform{}).Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if m := r.Models["gpt-5.1"]; m.Provider != nil {
		t.Errorf("gpt-5.1 override survived: %+v", m.Provider)
	}
}

// TestChatGPTModelEligible pins the Phase 2 gate: subscription-usable
// families pass, everything the plan cannot run is pruned.
func TestChatGPTModelEligible(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"gpt-5.1", true},
		{"gpt-5.1-codex", true},
		{"codex-mini-latest", true},
		{"o3", true},
		{"o3-pro", true},
		{"o4-mini", true},
		{"gpt-4.1", true},
		{"gpt-4.1-mini", true},
		{"gpt-4o", false},
		{"gpt-4o-mini", false},
		{"gpt-4-turbo", false},
		{"davinci-002", false},
		{"text-embedding-3-large", false},
		{"", false},
	}
	for _, c := range cases {
		if got := chatgptModelEligible(c.id); got != c.want {
			t.Errorf("chatgptModelEligible(%q) = %v, want %v", c.id, got, c.want)
		}
	}
}

// TestOpenAIFetchModelsGatesSubscriptionModelsOffline: with an OAuth
// credential and the backend unreachable, the model list degrades to the
// offline catalog gate rather than emptying the picker; any other
// credential keeps the catalog list (nil nil → LiveModels falls back).
func TestOpenAIFetchModelsGatesSubscriptionModelsOffline(t *testing.T) {
	// A dead endpoint so the live fetch fails and the fallback shows.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer dead.Close()
	chatgptModelsCache.invalidate()
	defer chatgptModelsCache.invalidate()

	catalog := map[string]modelsdev.Model{
		"gpt-5.1": {ID: "gpt-5.1"},
		"gpt-4o":  {ID: "gpt-4o"},
	}

	r := chatgptTestCredential(t, auth.Info{
		Type:      "oauth",
		Access:    "access-token",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_123",
	})
	r.Models = catalog
	r.BaseURL = dead.URL
	r.APIKey = "access-token"
	r.Header("chatgpt-account-id", "acct_123")
	got, err := (openaiTransform{}).FetchModels(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["gpt-5.1"]; !ok {
		t.Errorf("eligible gpt-5.1 was pruned from the subscription list")
	}
	if _, ok := got["gpt-4o"]; ok {
		t.Errorf("ineligible gpt-4o survived the subscription gate")
	}

	r = chatgptTestCredential(t, auth.Info{Type: "api", Key: "sk-test"})
	r.Models = catalog
	// No account header: an API key never gets the codex rewrite, so the
	// guard must skip the fetch before any network call.
	if got, err := (openaiTransform{}).FetchModels(context.Background(), r); err != nil || got != nil {
		t.Errorf("API-key FetchModels = (%v, %v), want (nil, nil) so LiveModels falls back to the catalog", got, err)
	}
}

// TestOpenAIFetchModelsFetchesCodexList: with a live codex backend the list
// comes from the wire — list-visible models only — carrying the account
// header a subscription token requires.
func TestOpenAIFetchModelsFetchesCodexList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Errorf("Authorization = %q, want the bearer token", r.Header.Get("Authorization"))
		}
		if r.Header.Get("chatgpt-account-id") != "acct_123" {
			t.Errorf("chatgpt-account-id = %q, want acct_123", r.Header.Get("chatgpt-account-id"))
		}
		if r.URL.Query().Get("client_version") == "" {
			t.Error("client_version query parameter is required")
		}
		w.Write([]byte(`{"models":[
			{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","context_window":500000,
			 "visibility":"list","input_modalities":["text","image"],
			 "supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}],
			 "model_messages":{"instructions_template":"MUST NOT BE KEPT"}},
			{"slug":"gpt-reserve","display_name":"GPT-Reserve","visibility":"hide"},
			{"slug":"","visibility":"list"}
		]}`))
	}))
	defer server.Close()
	chatgptModelsCache.invalidate()
	defer chatgptModelsCache.invalidate()

	r := chatgptTestCredential(t, auth.Info{
		Type:      "oauth",
		Access:    "access-token",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_123",
	})
	r.BaseURL = server.URL
	r.APIKey = "access-token"
	r.Header("chatgpt-account-id", "acct_123")
	r.Models = map[string]modelsdev.Model{"gpt-4o": {ID: "gpt-4o"}}

	got, err := (openaiTransform{}).FetchModels(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d models, want only the list-visible one: %v", len(got), got)
	}
	model, ok := got["gpt-6.1-sol"]
	if !ok {
		t.Fatal("expected gpt-6.1-sol from the live list")
	}
	if model.Name != "GPT-6.1-Sol" || model.Limit.Context != 500000 {
		t.Errorf("model = %+v, want the wire display name and context window", model)
	}
	if r.LiveContextLimits["gpt-6.1-sol"].Limit.Context != 500000 {
		t.Error("successfully retrieved context limit not available for persistence")
	}
	if !model.Attachment {
		t.Error("an image-modality model should be marked attachment-capable")
	}
	if !model.Reasoning || len(model.ReasoningOptions) != 1 || len(model.ReasoningOptions[0].Values) != 2 {
		t.Errorf("reasoning options = %+v, want the wire effort levels", model.ReasoningOptions)
	}
	if _, ok := got["gpt-4o"]; ok {
		t.Error("a live list must replace the catalog's, not merge with it")
	}
}

// TestOpenAIFetchModelsSkipsCustomRelay: a custom baseURL is a deliberate
// relay choice; the codex list is not behind it, and the picker falls back
// to the catalog list untouched.
func TestOpenAIFetchModelsSkipsCustomRelay(t *testing.T) {
	r := chatgptTestCredential(t, auth.Info{
		Type:      "oauth",
		Access:    "access-token",
		Refresh:   "refresh-token",
		Expires:   time.Now().Add(time.Hour).UnixMilli(),
		AccountID: "acct_123",
	})
	r.BaseURL = "https://relay.internal/v1"
	r.APIKey = "access-token"
	r.Models = map[string]modelsdev.Model{"gpt-4o": {ID: "gpt-4o"}}
	if got, err := (openaiTransform{}).FetchModels(context.Background(), r); err != nil || got != nil {
		t.Errorf("FetchModels = (%v, %v), want (nil, nil) for a custom relay", got, err)
	}
}

// TestDefaultClientOpenAIResponsesProtocol: the new protocol must build the
// Responses wire client pointed at the rewritten base URL, not a Chat
// Completions client — the two request bodies are not interchangeable.
func TestDefaultClientOpenAIResponsesProtocol(t *testing.T) {
	r := &Resolved{
		ID:       "openai",
		Protocol: ProtocolOpenAIResponses,
		BaseURL:  chatgptCodexBaseURL,
		APIKey:   "access-token",
	}
	client, err := r.defaultClient()
	if err != nil {
		t.Fatal(err)
	}
	responses, ok := client.(*openairesponses.Client)
	if !ok {
		t.Fatalf("defaultClient built %T, want *openairesponses.Client", client)
	}
	if responses.BaseURL != chatgptCodexBaseURL {
		t.Errorf("client BaseURL = %q, want %q", responses.BaseURL, chatgptCodexBaseURL)
	}
}
