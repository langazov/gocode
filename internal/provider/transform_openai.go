package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/llm/openai"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

func init() {
	Register(openaiTransform{byID{"openai"}})
}

// Ported from packages/core/src/plugin/provider/openai.ts.
const (
	chatgptClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatgptIssuer       = "https://auth.openai.com"
	chatgptCallbackPort = 1455
	chatgptScope        = "openid profile email offline_access"
	// chatgptCodexBaseURL is the backend a ChatGPT-plan subscription token
	// actually authorizes. The token's scopes do not transfer to
	// api.openai.com — posting it there as a plain bearer returns
	// 401 "Missing scopes: api.responses.write" even though the login itself
	// succeeded. The codex backend speaks OpenAI's Responses API
	// ({base}/responses), which is why Apply switches the wire protocol too.
	chatgptCodexBaseURL = "https://chatgpt.com/backend-api/codex"
)

// openaiTransform adds the ChatGPT Pro/Plus subscription login to the openai
// provider and routes subscription-authenticated requests to the backend that
// token unlocks. An API-key user is unaffected: Apply only rewrites the
// request when the stored credential is an OAuth one.
type openaiTransform struct{ byID }

// Apply routes a ChatGPT subscription credential to the codex backend.
//
// A subscription token is not an API-key-equivalent credential: it authorizes
// chatgpt.com/backend-api/codex only, and every request must also carry the
// chatgpt-account-id header naming the plan the token belongs to. Sending it
// to api.openai.com instead — as this port did before, by its own doc
// comment's admission "contributing no request changes" — surfaces as a bare
// 401 with nothing pointing at the real cause. This mirrors the fix upstream
// opencode shipped for the identical bug (V2 issue #34765, PR #34843).
func (openaiTransform) Apply(ctx context.Context, r *Resolved) error {
	info, err := chatgptCredentialInUse(ctx, r)
	if err != nil || info == nil {
		return err
	}
	// An explicitly configured endpoint (a relay, a self-hosted proxy) is a
	// deliberate choice by the user; the subscription rewrite only applies to
	// the default public API endpoint.
	if r.BaseURL != "" && r.BaseURL != openai.DefaultBaseURL {
		return nil
	}

	r.APIKey = info.Access

	// Credentials stored by a build before the account id was captured at
	// login can still be recovered here: the id rides in the access token's
	// own claims, so nobody has to re-login to pick it up.
	accountID := info.AccountID
	if accountID == "" {
		accountID = chatgptAccountIDFromToken(info.Access)
	}
	if accountID == "" {
		return fmt.Errorf(
			"openai: ChatGPT login is missing its account id — run `gocode providers login openai` again to refresh it")
	}

	r.BaseURL = chatgptCodexBaseURL
	r.Header("chatgpt-account-id", accountID)
	r.Protocol = ProtocolOpenAIResponses
	// The codex backend rejects the Responses-API max_output_tokens parameter
	// outright — every request carrying it dies 400 "Unsupported parameter:
	// max_output_tokens" (ses_f2c025586ffe5, 2026-09-24, among others that
	// day). The runner derives that cap from the catalog as a client-side
	// budget; the backend enforces its own, so the field goes.
	r.Options.DropMaxOutputTokens = true

	// A per-model override carries its own endpoint, and modelRoutedClient
	// would honor it — routing a model right back to api.openai.com and
	// around this rewrite. Under a subscription credential the codex backend
	// is the only endpoint any model has, so the overrides go.
	clearModelOverrides(r)
	return nil
}

// chatgptCredentialInUse returns the stored ChatGPT subscription credential
// only when it is the credential actually authenticating this provider.
//
// An env key or a configured apiKey is an explicit API-billing choice, and
// it wins the resolution (ResolveAPIKey checks env before the store). With
// one in play, rewriting to the codex backend would post an API key to an
// endpoint that only accepts subscription tokens — so neither the routing
// nor the model gate applies. This transform is registered for "openai"
// only, whose env list is exactly OPENAI_API_KEY.
func chatgptCredentialInUse(ctx context.Context, r *Resolved) (*auth.Info, error) {
	envNames := r.Entry.Env
	if len(envNames) == 0 {
		envNames = []string{"OPENAI_API_KEY"}
	}
	for _, name := range envNames {
		if os.Getenv(name) != "" {
			return nil, nil
		}
	}
	if r.Config != nil && r.Config.Options.APIKey != "" {
		return nil, nil
	}
	info, err := ResolveCredential(ctx, r.ID, r.Entry)
	if err != nil || info == nil || info.Type != "oauth" {
		return nil, err
	}
	return info, nil
}

// clearModelOverrides strips the per-model provider overrides that would
// otherwise re-route individual models away from the rewritten endpoint.
func clearModelOverrides(r *Resolved) {
	for id, model := range r.Models {
		if model.Provider != nil {
			model.Provider = nil
			r.Models[id] = model
		}
	}
}

func (openaiTransform) AuthMethods() []Method {
	return []Method{{
		Type:  MethodOAuth,
		Label: "ChatGPT Pro/Plus (browser)",
		Login: chatgptLogin,
	}}
}

// RefreshCredential renews a ChatGPT token, porting refresh() in openai.ts.
func (openaiTransform) RefreshCredential(ctx context.Context, info auth.Info) (auth.Info, error) {
	tokens, err := auth.RefreshGrant(ctx, nil, chatgptIssuer+"/oauth/token", chatgptClientID, info.Refresh, "")
	if err != nil {
		return auth.Info{}, err
	}
	next := info
	next.Access = tokens.AccessToken
	next.Refresh = tokens.RefreshToken
	next.Expires = tokens.ExpiresAt()
	if accountID := chatgptAccountID(tokens); accountID != "" {
		next.AccountID = accountID
	}
	return next, nil
}

// chatgptLogin runs the PKCE authorization-code flow: spin up the loopback
// listener OpenAI redirects back to, send the user to the consent page, then
// exchange the returned code.
func chatgptLogin(ctx context.Context, _ map[string]string) (Credential, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return Credential{}, err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return Credential{}, err
	}

	// Bind before printing the URL: if the port is taken, the user should hear
	// about it now rather than after authorizing in the browser.
	listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", chatgptCallbackPort))
	if err != nil {
		return Credential{}, fmt.Errorf("cannot listen on localhost:%d for the OAuth callback: %w", chatgptCallbackPort, err)
	}
	defer listener.Close()

	redirect := fmt.Sprintf("http://localhost:%d/auth/callback", chatgptCallbackPort)
	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)

	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/callback" {
			http.NotFound(w, r)
			return
		}
		query := r.URL.Query()
		if failure := firstNonEmpty(query.Get("error_description"), query.Get("error")); failure != "" {
			respondCallback(w, http.StatusBadRequest, "Authorization failed: "+failure)
			results <- result{err: fmt.Errorf("chatgpt authorization failed: %s", failure)}
			return
		}
		code := query.Get("code")
		if code == "" || query.Get("state") != state {
			message := "Invalid OAuth state"
			if code == "" {
				message = "Missing authorization code"
			}
			respondCallback(w, http.StatusBadRequest, message)
			results <- result{err: fmt.Errorf("chatgpt authorization failed: %s", message)}
			return
		}
		respondCallback(w, http.StatusOK, "Signed in. You can close this window and return to the terminal.")
		results <- result{code: code}
	})}
	go server.Serve(listener)
	defer server.Close()

	authorizeURL := chatgptAuthorizeURL(redirect, challenge, state)
	promptLogin(ctx, LoginPrompt{
		URL:     authorizeURL,
		Message: "Open this URL to sign in to ChatGPT:\n\n" + authorizeURL + "\n\nWaiting for the browser to complete authorization...",
	})

	var code string
	select {
	case <-ctx.Done():
		return Credential{}, ctx.Err()
	case received := <-results:
		if received.err != nil {
			return Credential{}, received.err
		}
		code = received.code
	}

	tokens, err := chatgptExchange(ctx, code, redirect, verifier)
	if err != nil {
		return Credential{}, err
	}
	return Credential{
		Type:      "oauth",
		Access:    tokens.AccessToken,
		Refresh:   tokens.RefreshToken,
		Expires:   tokens.ExpiresAt(),
		AccountID: chatgptAccountID(tokens),
	}, nil
}

// chatgptExchange trades the authorization code for tokens, porting
// exchange() in openai.ts.
func chatgptExchange(ctx context.Context, code, redirect, verifier string) (*auth.TokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {chatgptClientID},
		"code_verifier": {verifier},
	}
	return auth.PostTokenForm(ctx, nil, chatgptIssuer+"/oauth/token", form)
}

// chatgptAuthorizeURL ports authorizeURL(), including the two flow flags the
// TS implementation sends.
func chatgptAuthorizeURL(redirect, challenge, state string) string {
	query := url.Values{
		"response_type":              {"code"},
		"client_id":                  {chatgptClientID},
		"redirect_uri":               {redirect},
		"scope":                      {chatgptScope},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {"opencode"},
	}
	return chatgptIssuer + "/oauth/authorize?" + query.Encode()
}

// chatgptAccountID ports extractAccountID(): the account id is read from the
// id_token, falling back to the access token, purely to label the credential.
func chatgptAccountID(tokens *auth.TokenResponse) string {
	for _, token := range []string{tokens.IDToken, tokens.AccessToken} {
		if token == "" {
			continue
		}
		if id := auth.JWTClaim(token, "chatgpt_account_id"); id != "" {
			return id
		}
		if id := auth.JWTClaim(token, "https://api.openai.com/auth", "chatgpt_account_id"); id != "" {
			return id
		}
	}
	return ""
}

// chatgptAccountIDFromToken recovers the account id from a bare access token
// — the fallback path for credentials stored by a build that predates the
// login flow capturing it (chatgptAccountID above), so an existing login
// keeps working without a re-login.
func chatgptAccountIDFromToken(accessToken string) string {
	if id := auth.JWTClaim(accessToken, "chatgpt_account_id"); id != "" {
		return id
	}
	return auth.JWTClaim(accessToken, "https://api.openai.com/auth", "chatgpt_account_id")
}

// chatgptModelEligible reports whether a model id is usable through the
// ChatGPT subscription backend. The public catalog lists the whole OpenAI
// range, but a subscription token only opens the codex models: without this
// filter the picker offers gpt-4o et al, which then fail at request time
// with an error indistinguishable from an outage. Ports the ALLOWED_MODELS
// gate in packages/opencode/src/plugin/openai/codex.ts, as a prefix/exact
// match rather than a list that ages.
func chatgptModelEligible(id string) bool {
	if id == "" {
		return false
	}
	for _, prefix := range []string{"gpt-5", "codex-", "o3", "o4-mini", "gpt-4.1"} {
		if strings.HasPrefix(id, prefix) {
			return true
		}
	}
	// Exact ids that share no prefix with the families above.
	switch id {
	case "codex-mini", "gpt-4o-codex", "chatgpt-4o-latest":
		return true
	}
	return false
}

// FetchModels implements ModelSource, gating the catalog's model list to the
// models a ChatGPT subscription actually unlocks. A non-OAuth credential
// (or none) keeps the full public list: the gate only applies to a logged-in
// subscription. Purely offline — a filter, not a fetch — so it needs neither
// the modelCache plumbing nor invalidation on login/logout.
func (openaiTransform) FetchModels(ctx context.Context, r *Resolved) (map[string]modelsdev.Model, error) {
	info, err := chatgptCredentialInUse(ctx, r)
	if err != nil || info == nil {
		// Not a subscription login in use: the caller falls back to the
		// catalog list (see Resolved.LiveModels), which is exactly what an
		// API key is entitled to.
		return nil, nil
	}
	out := make(map[string]modelsdev.Model, len(r.Models))
	for id, model := range r.Models {
		if chatgptModelEligible(id) {
			out[id] = model
		}
	}
	return out, nil
}

// generatePKCE ports generatePKCE(): a 43-character verifier drawn from the
// RFC 7636 unreserved alphabet, and its S256 challenge.
func generatePKCE() (verifier, challenge string, err error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	bytes := make([]byte, 43)
	if _, err := rand.Read(bytes); err != nil {
		return "", "", err
	}
	out := make([]byte, len(bytes))
	for i, b := range bytes {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	verifier = string(out)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomURLSafe(size int) (string, error) {
	bytes := make([]byte, size)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func respondCallback(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>gocode</title>"+
		"<body style=\"font:16px system-ui;padding:3rem;text-align:center\"><p>%s</p></body>", message)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

var (
	_ Transform    = openaiTransform{}
	_ AuthProvider = openaiTransform{}
	_ Refresher    = openaiTransform{}
)
