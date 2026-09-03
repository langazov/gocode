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

	"github.com/langazov/gocode-go/internal/auth"
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
)

// openaiTransform adds the ChatGPT Pro/Plus subscription login to the openai
// provider. It contributes no request changes — an API-key user is unaffected
// — only the OAuth method and the refresh that credential needs.
type openaiTransform struct{ byID }

func (openaiTransform) Apply(_ context.Context, _ *Resolved) error { return nil }

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
		Type:    "oauth",
		Access:  tokens.AccessToken,
		Refresh: tokens.RefreshToken,
		Expires: tokens.ExpiresAt(),
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
