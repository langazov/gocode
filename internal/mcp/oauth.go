package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

// errAuthRequired is returned by the passive AuthorizationCodeFetcher used
// for automatic/background connects (Load, Connect, status probes): it
// never opens a browser, so a server that actually needs interactive login
// fails fast with this sentinel instead of hanging — classifyError (in
// service.go) turns it into status "needs_auth", mirroring connectRemote's
// UnauthorizedError handling in index.ts (which likewise never blocks on
// user interaction outside the explicit `mcp auth` command/API route).
var errAuthRequired = errors.New("mcp: interactive authorization required")

// oauthMode controls whether newOAuthHandler's AuthorizationCodeFetcher may
// actually perform the interactive browser flow.
type oauthMode int

const (
	modePassive     oauthMode = iota // status probing: never opens a browser
	modeInteractive                  // `opencode mcp auth <name>`: opens one and waits
)

// defaultCallbackPort/messagePath mirror the default port/path
// oauth-callback.ts listens on.
const (
	defaultCallbackPort = 19876
	callbackPath        = "/mcp/oauth/callback"
)

// newOAuthHandler builds the go-sdk auth.OAuthHandler for one server. It
// wires this port's own persistence (store.go) into the handler's
// NewTokenSource/InitialTokenSource/PreregisteredClient hooks, mirroring
// McpOAuthProvider's clientInformation()/tokens()/saveTokens() — priority
// for the client identity is config clientId, then a previously stored one
// (from a prior dynamic registration), then dynamic registration itself,
// exactly matching clientInformation() in oauth-provider.ts.
func newOAuthHandler(name string, cfg ServerConfig, mode oauthMode, onAuthURL func(url string)) (sdkauth.OAuthHandler, error) {
	redirectURL := cfg.OAuth.RedirectURI
	port := cfg.OAuth.CallbackPort
	if port == 0 {
		port = defaultCallbackPort
	}
	if redirectURL == "" {
		redirectURL = fmt.Sprintf("http://127.0.0.1:%d%s", port, callbackPath)
	}

	handlerCfg := &sdkauth.AuthorizationCodeHandlerConfig{
		RedirectURL:              redirectURL,
		RequestRefreshToken:      true,
		AuthorizationCodeFetcher: authFetcher(mode, port, redirectURL, onAuthURL),
		NewTokenSource: func(ctx context.Context, oc *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			return newPersistingTokenSource(name, cfg.URL, oc, tok), nil
		},
	}

	switch {
	case cfg.OAuth.ClientID != "":
		handlerCfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: cfg.OAuth.ClientID}
		if cfg.OAuth.ClientSecret != "" {
			handlerCfg.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: cfg.OAuth.ClientSecret}
		}
	default:
		if entry, ok := StoreGet(name, cfg.URL); ok && entry.ClientID != "" {
			handlerCfg.PreregisteredClient = &oauthex.ClientCredentials{ClientID: entry.ClientID}
			if entry.ClientSecret != "" {
				handlerCfg.PreregisteredClient.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: entry.ClientSecret}
			}
		} else {
			handlerCfg.DynamicClientRegistrationConfig = &sdkauth.DynamicClientRegistrationConfig{
				Metadata: &oauthex.ClientRegistrationMetadata{
					ClientName:    "opencode",
					ClientURI:     "https://opencode.ai",
					RedirectURIs:  []string{redirectURL},
					GrantTypes:    []string{"authorization_code", "refresh_token"},
					ResponseTypes: []string{"code"},
					Scope:         cfg.OAuth.Scope,
				},
			}
		}
	}

	if entry, ok := StoreGet(name, cfg.URL); ok && entry.HasTokens() && entry.AuthURL != "" && entry.TokenURL != "" {
		oc := &oauth2.Config{
			ClientID:     entry.ClientID,
			ClientSecret: entry.ClientSecret,
			Endpoint:     oauth2.Endpoint{AuthURL: entry.AuthURL, TokenURL: entry.TokenURL},
			RedirectURL:  redirectURL,
		}
		tok := &oauth2.Token{AccessToken: entry.AccessToken, RefreshToken: entry.RefreshToken}
		if entry.Expiry > 0 {
			tok.Expiry = time.Unix(entry.Expiry, 0)
		}
		handlerCfg.InitialTokenSource = newPersistingTokenSource(name, cfg.URL, oc, tok)
	}

	return sdkauth.NewAuthorizationCodeHandler(handlerCfg)
}

// authFetcher implements sdkauth.AuthorizationCodeFetcher: in passive mode
// it declines immediately (errAuthRequired); in interactive mode it opens a
// one-shot local HTTP server to catch the redirect, opens the browser (or
// reports the URL via onAuthURL), and waits up to 5 minutes — mirroring
// authenticate()'s waitForCallback flow in index.ts, simplified to a single
// ephemeral listener per call since this port's `mcp auth <name>` is always
// a one-at-a-time synchronous CLI/API invocation (no need for
// oauth-callback.ts's shared, multi-pending-state server).
func authFetcher(mode oauthMode, port int, redirectURL string, onAuthURL func(string)) sdkauth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
		if mode == modePassive {
			return nil, errAuthRequired
		}

		type result struct {
			code, state, iss string
		}
		ch := make(chan result, 1)
		mux := http.NewServeMux()
		path := callbackPath
		if redirectURL != "" {
			if u, err := url.Parse(redirectURL); err == nil && u.Path != "" {
				path = u.Path
			}
		}
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			select {
			case ch <- result{code: q.Get("code"), state: q.Get("state"), iss: q.Get("iss")}:
			default:
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, "<html><body>Authentication successful. You can close this window.</body></html>")
		})
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			return nil, fmt.Errorf("mcp: could not start OAuth callback listener on port %d: %w", port, err)
		}
		srv := &http.Server{Handler: mux}
		go srv.Serve(listener)
		defer srv.Close()

		if onAuthURL != nil {
			onAuthURL(args.URL)
		} else {
			openBrowser(args.URL)
		}

		select {
		case res := <-ch:
			return &sdkauth.AuthorizationResult{Code: res.code, State: res.state, Iss: res.iss}, nil
		case <-time.After(5 * time.Minute):
			return nil, errors.New("mcp: authorization timed out after 5 minutes")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// persistingTokenSource wraps an oauth2.Config-backed TokenSource (which
// auto-refreshes using the resolved client_id/secret/endpoint) and persists
// the token — and the client identity that produced it — to this port's
// mcp-auth.json whenever it changes, mirroring saveTokens() in
// oauth-provider.ts.
type persistingTokenSource struct {
	mu    sync.Mutex
	inner oauth2.TokenSource
	last  string
	name  string
	url   string
	oc    *oauth2.Config
}

func newPersistingTokenSource(name, url string, oc *oauth2.Config, tok *oauth2.Token) *persistingTokenSource {
	p := &persistingTokenSource{name: name, url: url, oc: oc}
	p.inner = oc.TokenSource(context.Background(), tok)
	p.persist(tok)
	return p
}

func (p *persistingTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.inner.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	changed := tok.AccessToken != p.last
	p.mu.Unlock()
	if changed {
		p.persist(tok)
	}
	return tok, nil
}

func (p *persistingTokenSource) persist(tok *oauth2.Token) {
	p.mu.Lock()
	p.last = tok.AccessToken
	p.mu.Unlock()
	entry := Entry{
		ServerURL:    p.url,
		ClientID:     p.oc.ClientID,
		ClientSecret: p.oc.ClientSecret,
		AuthURL:      p.oc.Endpoint.AuthURL,
		TokenURL:     p.oc.Endpoint.TokenURL,
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
	}
	if !tok.Expiry.IsZero() {
		entry.Expiry = tok.Expiry.Unix()
	}
	_ = StoreSet(p.name, entry) // best-effort, matching TS's fire-and-forget persistence
}
