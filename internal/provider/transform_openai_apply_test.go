package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// TestOpenAIFetchModelsGatesSubscriptionModels: with an OAuth credential the
// model list is the eligible subset; any other credential keeps the catalog
// list (nil nil → LiveModels falls back to it).
func TestOpenAIFetchModelsGatesSubscriptionModels(t *testing.T) {
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
	if got, err := (openaiTransform{}).FetchModels(context.Background(), r); err != nil || got != nil {
		t.Errorf("API-key FetchModels = (%v, %v), want (nil, nil) so LiveModels falls back to the catalog", got, err)
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
