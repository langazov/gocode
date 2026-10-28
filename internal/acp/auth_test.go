package acp

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/langazov/gocode-go/internal/jsonrpc"
)

// fakeAuth records logins and drives the UI the way the real host does.
type fakeAuth struct {
	mu      sync.Mutex
	logins  []string
	present bool
	run     func(ctx context.Context, ui LoginUI) error
}

func (f *fakeAuth) Authenticated(context.Context) bool { return f.present }
func (f *fakeAuth) Logout(context.Context) error       { return nil }

func (f *fakeAuth) Methods(context.Context) []AuthMethod {
	return []AuthMethod{
		{ID: "web-login", Name: "Log in on the web", NeedsURL: true},
		{ID: "terminal-signup", Name: "Sign up in the terminal", Args: []string{"--login", "--login-target=signup"}},
	}
}

func (f *fakeAuth) Login(ctx context.Context, methodID string, ui LoginUI) error {
	f.mu.Lock()
	f.logins = append(f.logins, methodID)
	f.mu.Unlock()
	return f.run(ctx, ui)
}

func methodIDs(t *testing.T, result map[string]any, key string) []string {
	t.Helper()
	methods, _ := result["authMethods"].([]any)
	var ids []string
	for _, raw := range methods {
		method, _ := raw.(map[string]any)
		id, _ := method[key].(string)
		ids = append(ids, id)
	}
	return ids
}

func TestAuthMethodsIncludeHostMethodsAndFilterTerminal(t *testing.T) {
	auth := &fakeAuth{}
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{auth: auth})
	var result map[string]any
	f.client.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}, &result)
	got := methodIDs(t, result, "id")
	want := []string{authMethodID, "web-login"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("without auth.terminal: methods %v, want %v", got, want)
	}

	f.client.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{"auth": map[string]any{"terminal": true}}}, &result)
	got = methodIDs(t, result, "id")
	if len(got) != 4 || got[2] != "terminal-signup" || got[3] != terminalAuthMethodID {
		t.Fatalf("with auth.terminal: methods %v", got)
	}
	methods, _ := result["authMethods"].([]any)
	signup, _ := methods[2].(map[string]any)
	if signup["type"] != "terminal" {
		t.Fatalf("terminal method shape: %v", signup)
	}
}

func TestAuthenticateRunsURLLoginScopedToRequest(t *testing.T) {
	completed := make(chan string, 1)
	auth := &fakeAuth{run: func(ctx context.Context, ui LoginUI) error {
		if err := ui.OpenURL(ctx, "https://login.example.test/start", "Log in"); err != nil {
			return err
		}
		ui.Done()
		return nil
	}}
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{
		auth:       auth,
		clientCaps: map[string]any{"elicitation": map[string]any{"url": map[string]any{}}},
	})
	f.client.handle("elicitation/create", func(map[string]any) (any, error) {
		return map[string]any{"action": "accept"}, nil
	})
	f.client.conn.OnNotify("elicitation/complete", func(params json.RawMessage) {
		var decoded map[string]any
		json.Unmarshal(params, &decoded)
		id, _ := decoded["elicitationId"].(string)
		completed <- id
	})

	f.client.call("authenticate", map[string]any{"methodId": "web-login"}, nil)
	request := f.client.waitRequest("elicitation/create")
	if request["mode"] != "url" || request["url"] != "https://login.example.test/start" {
		t.Fatalf("elicitation: %v", request)
	}
	if request["requestId"] == nil || request["sessionId"] != nil {
		t.Fatalf("a login elicitation is request-scoped: %v", request)
	}
	if id := <-completed; id != request["elicitationId"] {
		t.Fatalf("completed %q, elicited %v", id, request["elicitationId"])
	}
}

func TestAuthenticateDeclinedIsAuthRequired(t *testing.T) {
	auth := &fakeAuth{run: func(ctx context.Context, ui LoginUI) error {
		return ui.OpenURL(ctx, "https://login.example.test/start", "Log in")
	}}
	f := newFixture(t, ProtocolV2, &scriptedProvider{}, fixtureOptions{
		auth:       auth,
		clientCaps: map[string]any{"elicitation": map[string]any{"url": map[string]any{}}},
	})
	f.client.handle("elicitation/create", func(map[string]any) (any, error) {
		return map[string]any{"action": "decline"}, nil
	})
	err := f.client.try("auth/login", map[string]any{"methodId": "web-login"}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != codeAuthRequired {
		t.Fatalf("declined login: want auth_required, got %v", err)
	}
}

func TestAuthenticateWithoutURLElicitationReportsNoURL(t *testing.T) {
	var got error
	auth := &fakeAuth{run: func(ctx context.Context, ui LoginUI) error {
		got = ui.OpenURL(ctx, "https://login.example.test/start", "Log in")
		return nil
	}}
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{auth: auth})
	f.client.call("authenticate", map[string]any{"methodId": "web-login"}, nil)
	if !errors.Is(got, ErrNoURL) {
		t.Fatalf("OpenURL without URL elicitation: %v, want ErrNoURL", got)
	}
}

func TestAuthenticateRefusesTerminalAndUnknownMethods(t *testing.T) {
	auth := &fakeAuth{}
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{auth: auth})
	for _, id := range []string{"terminal-signup", terminalAuthMethodID, "nope"} {
		err := f.client.try("authenticate", map[string]any{"methodId": id}, nil)
		var rpcErr *jsonrpc.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("%s: want invalid params, got %v", id, err)
		}
	}
	if len(auth.logins) != 0 {
		t.Fatalf("refused methods ran a login: %v", auth.logins)
	}
}

func TestAuthenticateFormElicitationCollectsAnswers(t *testing.T) {
	answers := make(chan map[string]string, 1)
	auth := &fakeAuth{run: func(ctx context.Context, ui LoginUI) error {
		got, ok, err := ui.Ask(ctx, "Deployment", []FormField{{Key: "deploymentType", Label: "Type", Options: []string{"github.com", "enterprise"}}})
		if err != nil || !ok {
			return errors.New("form failed")
		}
		answers <- got
		return nil
	}}
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{
		auth:       auth,
		clientCaps: map[string]any{"elicitation": map[string]any{"form": map[string]any{}}},
	})
	f.client.handle("elicitation/create", func(params map[string]any) (any, error) {
		return map[string]any{"action": "accept", "content": map[string]any{"deploymentType": "enterprise"}}, nil
	})
	f.client.call("authenticate", map[string]any{"methodId": "web-login"}, nil)
	if got := <-answers; got["deploymentType"] != "enterprise" {
		t.Fatalf("answers %v", got)
	}
	request := f.client.waitRequest("elicitation/create")
	if request["mode"] != "form" || request["requestId"] == nil {
		t.Fatalf("form elicitation: %v", request)
	}
}
