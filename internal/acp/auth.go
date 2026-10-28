package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/jsonrpc"
)

// Auth is the credential surface behind authenticate / auth/login and
// logout / auth/logout.
type Auth interface {
	// Authenticated reports whether the default model's provider has usable
	// credentials. It backs the gocode-login method, which only confirms a
	// sign-in that already happened.
	Authenticated(ctx context.Context) bool
	// Logout removes the stored credential of the default model's provider.
	Logout(ctx context.Context) error
	// Methods lists the interactive login methods, in the order to offer
	// them: gocoder.org sign-in and sign-up, each provider's OAuth flows,
	// API-key entry, and terminal methods.
	Methods(ctx context.Context) []AuthMethod
	// Login runs a non-terminal method from Methods to completion, putting
	// whatever the user has to see or answer in front of them through ui.
	Login(ctx context.Context, methodID string, ui LoginUI) error
}

// AuthMethod is one login method as initialize advertises it.
type AuthMethod struct {
	ID          string
	Name        string
	Description string
	// Args, when set, make this a terminal method: the client relaunches its
	// own agent command with Args appended in an interactive terminal. It is
	// only offered to a client that advertised auth.terminal
	// (protocol/v1/authentication "Terminal").
	Args []string
	// NeedsURL marks a method that can only run through URL-mode
	// elicitation or a browser the agent opens itself. It is still offered
	// to clients without URL elicitation: the agent runs on the user's
	// machine and opens the page there.
	NeedsURL bool
}

// LoginUI is how a login flow reaches the user over the connection.
type LoginUI interface {
	// OpenURL asks the client to open url for the user, with message as the
	// instruction beside it. It returns ErrDeclined when the user refused,
	// and ErrNoURL when the client cannot open URLs — the caller then opens
	// the page itself.
	OpenURL(ctx context.Context, url, message string) error
	// Ask collects non-secret answers through a form. Form elicitation must
	// never carry a secret (protocol/v1/elicitation "Form mode"); passwords
	// and keys go through a page OpenURL shows. ok is false when the user
	// cancelled; ErrNoForm when the client has no form elicitation.
	Ask(ctx context.Context, message string, fields []FormField) (answers map[string]string, ok bool, err error)
	// Done reports that the external interaction OpenURL started finished,
	// so the client can dismiss whatever it showed.
	Done()
}

// FormField is one non-secret question of LoginUI.Ask.
type FormField struct {
	Key   string
	Label string
	// Options make the field a single choice; empty is free text.
	Options []string
	// Optional fields may be left blank.
	Optional bool
}

var (
	ErrDeclined = errors.New("the user declined")
	ErrNoURL    = errors.New("the client cannot open URLs")
	ErrNoForm   = errors.New("the client cannot show forms")
)

// loginTimeout bounds one interactive login: a device flow or a sign-in page
// left open past it is abandoned, and the client gets an error.
const loginTimeout = 15 * time.Minute

// authMethodID is the one protocol-driven method that runs no flow: it
// succeeds when credentials already exist (`gocode auth login`, an earlier
// ACP login, or an environment variable). terminalAuthMethodID is the
// interactive provider login a terminal-capable client can launch.
const (
	authMethodID         = "gocode-login"
	terminalAuthMethodID = "gocode-terminal-login"
)

// authMethods is every method this connection may use: the built-in check,
// the host's methods, and the provider terminal login. Terminal methods are
// filtered out for clients that cannot run them.
func (a *Agent) authMethods(ctx context.Context, client clientCaps) []AuthMethod {
	methods := []AuthMethod{{
		ID:          authMethodID,
		Name:        "Use existing credentials",
		Description: "Continue with the credentials gocode already has (`gocode auth login`, an environment variable, or a gocoder.org sign-in).",
	}}
	var extra []AuthMethod
	if a.host.Auth != nil {
		extra = a.host.Auth.Methods(ctx)
	}
	extra = append(extra, AuthMethod{
		ID:          terminalAuthMethodID,
		Name:        "Log in from the terminal",
		Description: "Sign in to a model provider interactively.",
		Args:        a.loginArgs(),
	})
	seen := map[string]bool{authMethodID: true}
	for _, method := range extra {
		if seen[method.ID] || (len(method.Args) > 0 && !client.terminalAuth) {
			continue
		}
		seen[method.ID] = true
		methods = append(methods, method)
	}
	return methods
}

func (a *Agent) loginArgs() []string {
	if len(a.host.LoginArgs) > 0 {
		return a.host.LoginArgs
	}
	return []string{"--login"}
}

func (a *Agent) authMethodsV1(ctx context.Context, client clientCaps) []obj {
	methods := a.authMethods(ctx, client)
	out := make([]obj, 0, len(methods))
	for _, method := range methods {
		entry := obj{"id": method.ID, "name": method.Name, "description": method.Description}
		if len(method.Args) > 0 {
			entry["type"], entry["args"] = "terminal", method.Args
		}
		out = append(out, entry)
	}
	return out
}

func (a *Agent) authMethodsV2(ctx context.Context, client clientCaps) []obj {
	methods := a.authMethods(ctx, client)
	out := make([]obj, 0, len(methods))
	for _, method := range methods {
		entry := obj{"methodId": method.ID, "type": "agent", "name": method.Name, "description": method.Description}
		if len(method.Args) > 0 {
			entry["type"], entry["args"] = "terminal", method.Args
		}
		out = append(out, entry)
	}
	return out
}

// authenticate serves v1 authenticate and v2 auth/login.
//
// gocode-login only confirms credentials exist. Every other agent method runs
// a real flow — a sign-in page, an OAuth consent page, a device code — that
// can take minutes, so it answers through a jsonrpc.Deferred and the
// connection keeps serving other requests meanwhile. Cancelling the request
// abandons the flow.
func (a *Agent) authenticate(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		MethodID string `json:"methodId"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	client := a.clientCapabilities()
	var method *AuthMethod
	for _, candidate := range a.authMethods(ctx, withTerminalAuth(client)) {
		if candidate.ID == in.MethodID {
			method = &candidate
			break
		}
	}
	switch {
	case method == nil:
		return nil, invalidParams("unknown authentication method %q", in.MethodID)
	case len(method.Args) > 0:
		// protocol/v1/authentication: the client MUST NOT pass a terminal
		// method; it launches the login process instead.
		return nil, invalidParams("%s is a terminal method: run it in a terminal instead of calling authenticate", in.MethodID)
	case method.ID == authMethodID:
		if a.host.Auth != nil && !a.host.Auth.Authenticated(ctx) {
			return nil, authRequired("No credentials for the configured model provider. Choose a login method, or run `gocode auth login` in a terminal, then try again.")
		}
		return obj{}, nil
	case a.host.Auth == nil:
		return nil, invalidParams("unknown authentication method %q", in.MethodID)
	}
	requestID, _ := jsonrpc.RequestID(ctx)
	ui := &elicitLoginUI{agent: a, requestID: requestID, id: "login-" + method.ID + "-" + strings.Trim(string(requestID), `"`)}
	return jsonrpc.Deferred(func() (any, error) {
		ctx, cancel := context.WithTimeout(ctx, loginTimeout)
		defer cancel()
		defer ui.release()
		if err := a.host.Auth.Login(ctx, method.ID, ui); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, ErrDeclined) {
				return nil, authRequired("Login cancelled.")
			}
			return nil, authRequired(err.Error())
		}
		return obj{}, nil
	}), nil
}

// withTerminalAuth returns caps with terminal authentication switched on, so a
// method lookup sees every method — including terminal ones, which
// authenticate must recognise in order to refuse them by name.
func withTerminalAuth(caps clientCaps) clientCaps {
	caps.terminalAuth = true
	return caps
}

// logout serves v1 logout and v2 auth/logout.
func (a *Agent) logout(ctx context.Context, _ json.RawMessage) (any, error) {
	if a.host.Auth != nil {
		if err := a.host.Auth.Logout(ctx); err != nil {
			return nil, internalError("logout: %v", err)
		}
	}
	return obj{}, nil
}

// elicitLoginUI is LoginUI over request-scoped elicitation: a login runs
// before any session exists, so its elicitations carry the authenticate
// request's id instead of a sessionId (protocol/v1/elicitation "Scope").
type elicitLoginUI struct {
	agent     *Agent
	requestID json.RawMessage
	// id is the URL elicitation id; unique per connection because the
	// request id it embeds is.
	id     string
	opened bool
}

func (u *elicitLoginUI) scope(params obj) obj {
	if len(u.requestID) > 0 {
		params["requestId"] = u.requestID
	}
	return params
}

func (u *elicitLoginUI) OpenURL(ctx context.Context, target, message string) error {
	if !u.agent.clientCapabilities().elicitURL {
		return ErrNoURL
	}
	if parsed, err := url.Parse(target); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fmt.Errorf("refusing to open %q", target)
	}
	a := u.agent
	if !u.opened {
		a.mu.Lock()
		if a.elicitations[u.id] {
			a.mu.Unlock()
			return fmt.Errorf("a login with id %s is already in progress", u.id)
		}
		a.elicitations[u.id] = true
		a.mu.Unlock()
		u.opened = true
	}
	var result struct {
		Action string `json:"action"`
	}
	err := a.call(ctx, "elicitation/create", u.scope(obj{
		"mode":          "url",
		"elicitationId": u.id,
		"url":           target,
		"message":       message,
	}), &result)
	if err != nil {
		return fmt.Errorf("asking the client to open the login page: %w", err)
	}
	if result.Action != "accept" {
		return ErrDeclined
	}
	return nil
}

func (u *elicitLoginUI) Ask(ctx context.Context, message string, fields []FormField) (map[string]string, bool, error) {
	if !u.agent.clientCapabilities().elicitForm {
		return nil, false, ErrNoForm
	}
	properties := obj{}
	var required []string
	for _, field := range fields {
		property := obj{"type": "string", "title": field.Label}
		if len(field.Options) > 0 {
			property["enum"] = field.Options
		}
		properties[field.Key] = property
		if !field.Optional {
			required = append(required, field.Key)
		}
	}
	schema := obj{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	var result struct {
		Action  string         `json:"action"`
		Content map[string]any `json:"content"`
	}
	if err := u.agent.call(ctx, "elicitation/create", u.scope(obj{
		"mode":            "form",
		"message":         message,
		"requestedSchema": schema,
	}), &result); err != nil {
		return nil, false, err
	}
	if result.Action != "accept" {
		return nil, false, nil
	}
	answers := map[string]string{}
	for key, value := range result.Content {
		if text, ok := value.(string); ok {
			answers[key] = text
		}
	}
	return answers, true, nil
}

func (u *elicitLoginUI) Done() {
	if u.opened {
		u.agent.notify("elicitation/complete", obj{"elicitationId": u.id})
	}
}

// release frees the elicitation id once the login settles, however it ends.
func (u *elicitLoginUI) release() {
	if !u.opened {
		return
	}
	u.agent.mu.Lock()
	delete(u.agent.elicitations, u.id)
	u.agent.mu.Unlock()
}
