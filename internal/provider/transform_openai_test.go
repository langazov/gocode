package provider

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/auth"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

// TestGeneratePKCE checks the two properties the flow depends on: the verifier
// is drawn from the RFC 7636 unreserved alphabet at the length openai.ts uses,
// and the challenge is its S256 digest — a mismatch fails the exchange with an
// opaque error from the server.
func TestGeneratePKCE(t *testing.T) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	verifier, challenge, err := generatePKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) != 43 {
		t.Errorf("verifier length = %d, want 43", len(verifier))
	}
	for _, c := range verifier {
		if !strings.ContainsRune(alphabet, c) {
			t.Fatalf("verifier contains %q, which is outside the unreserved alphabet", c)
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	if want := base64.RawURLEncoding.EncodeToString(sum[:]); challenge != want {
		t.Errorf("challenge = %q, want the S256 digest %q", challenge, want)
	}
	if strings.ContainsAny(challenge, "+/=") {
		t.Errorf("challenge %q must be base64url with no padding", challenge)
	}
}

func TestGeneratePKCEIsRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		verifier, _, err := generatePKCE()
		if err != nil {
			t.Fatal(err)
		}
		if seen[verifier] {
			t.Fatal("generatePKCE repeated a verifier")
		}
		seen[verifier] = true
	}
}

func TestChatGPTAuthorizeURL(t *testing.T) {
	raw := chatgptAuthorizeURL("http://localhost:1455/auth/callback", "chal", "st")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "auth.openai.com" || parsed.Path != "/oauth/authorize" {
		t.Errorf("endpoint = %s%s, want auth.openai.com/oauth/authorize", parsed.Host, parsed.Path)
	}
	want := map[string]string{
		"response_type":              "code",
		"client_id":                  chatgptClientID,
		"redirect_uri":               "http://localhost:1455/auth/callback",
		"scope":                      "openid profile email offline_access",
		"code_challenge":             "chal",
		"code_challenge_method":      "S256",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
		"state":                      "st",
		"originator":                 "opencode",
	}
	query := parsed.Query()
	for key, value := range want {
		if got := query.Get(key); got != value {
			t.Errorf("query %s = %q, want %q", key, got, value)
		}
	}
}

// TestChatGPTAccountIDFromClaims covers both claim shapes extractAccountID
// reads, and the fallback from id_token to access_token.
func TestChatGPTAccountIDFromClaims(t *testing.T) {
	jwt := func(claims map[string]any) string {
		payload, _ := json.Marshal(claims)
		return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
	}
	cases := []struct {
		name   string
		tokens auth.TokenResponse
		want   string
	}{
		{
			name:   "top-level claim",
			tokens: auth.TokenResponse{IDToken: jwt(map[string]any{"chatgpt_account_id": "acct-1"})},
			want:   "acct-1",
		},
		{
			name: "namespaced claim",
			tokens: auth.TokenResponse{IDToken: jwt(map[string]any{
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-2"},
			})},
			want: "acct-2",
		},
		{
			name: "falls back to the access token",
			tokens: auth.TokenResponse{
				IDToken:     jwt(map[string]any{"unrelated": true}),
				AccessToken: jwt(map[string]any{"chatgpt_account_id": "acct-3"}),
			},
			want: "acct-3",
		},
		{
			name:   "no claim anywhere",
			tokens: auth.TokenResponse{IDToken: "not-a-jwt"},
			want:   "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := chatgptAccountID(&c.tokens); got != c.want {
				t.Errorf("chatgptAccountID = %q, want %q", got, c.want)
			}
		})
	}
}

// TestOpenAIAuthMethodsAddChatGPT: the API-key path must survive alongside the
// new subscription login.
func TestOpenAIAuthMethodsAddChatGPT(t *testing.T) {
	methods := methodsFor("openai", modelsdev.Provider{Env: []string{"OPENAI_API_KEY"}})
	var oauth, key bool
	for _, method := range methods {
		if method.Type == MethodOAuth && strings.Contains(method.Label, "ChatGPT") {
			oauth = true
			if method.Login == nil {
				t.Error("the ChatGPT method has no login flow attached")
			}
		}
		key = key || method.Type == MethodKey
	}
	if !oauth {
		t.Error("expected a ChatGPT oauth method for openai")
	}
	if !key {
		t.Error("the plain API-key method must still be offered")
	}
}

// TestOpenAITransformLeavesRequestsAlone: adding a login method must not
// change how an API-key user's requests are formed.
func TestOpenAITransformLeavesRequestsAlone(t *testing.T) {
	r := &Resolved{ID: "openai", Protocol: ProtocolOpenAI, BaseURL: "https://api.openai.com/v1", APIKey: "sk-test"}
	if err := applyTransforms(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if r.BaseURL != "https://api.openai.com/v1" || r.APIKey != "sk-test" {
		t.Errorf("transform altered the resolved provider: %+v", r)
	}
	if r.Options.Sign != nil || r.Options.Endpoint != nil || len(r.Options.Headers) != 0 {
		t.Errorf("transform installed request hooks it should not have: %+v", r.Options)
	}
}
