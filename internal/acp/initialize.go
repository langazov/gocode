package acp

import (
	"context"
	"encoding/json"
)

// clientCaps is what the client advertised in initialize, normalised across
// versions. Omitted means unsupported in both (protocol/v1/initialization
// "Capabilities"); v1 marks support with booleans or objects, v2 with
// objects only, so presence of a non-null value is the test for both.
type clientCaps struct {
	readTextFile   bool
	writeTextFile  bool
	terminal       bool
	terminalAuth   bool
	elicitForm     bool
	elicitURL      bool
	booleanOptions bool
	info           map[string]any
}

// supported reports whether a capability marker is present: true for v1's
// booleans, any non-null object for v1's and v2's object markers.
func supported(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	switch string(raw) {
	case "null", "false":
		return false
	}
	return true
}

type rawCaps map[string]json.RawMessage

func (c rawCaps) sub(key string) rawCaps {
	var out rawCaps
	if raw, ok := c[key]; ok && supported(raw) {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func (c rawCaps) has(key string) bool { return supported(c[key]) }

func parseClientCaps(raw rawCaps) clientCaps {
	fs := raw.sub("fs")
	elicitation := raw.sub("elicitation")
	return clientCaps{
		readTextFile:   fs.has("readTextFile"),
		writeTextFile:  fs.has("writeTextFile"),
		terminal:       raw.has("terminal"),
		terminalAuth:   raw.sub("auth").has("terminal"),
		elicitForm:     elicitation.has("form"),
		elicitURL:      elicitation.has("url"),
		booleanOptions: raw.sub("session").sub("configOptions").has("boolean"),
	}
}

// negotiate picks the protocol version: the client's if we speak it,
// otherwise our latest (protocol/v1/initialization "Version Negotiation").
func negotiate(requested int) int {
	if requested >= ProtocolV1 && requested <= LatestProtocol {
		return requested
	}
	return LatestProtocol
}

// authMethodID is the one protocol-driven login method: it succeeds when the
// user has already signed in with `gocode auth login`.
const (
	authMethodID         = "gocode-login"
	terminalAuthMethodID = "gocode-terminal-login"
)

func (a *Agent) initialize(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		ProtocolVersion    int            `json:"protocolVersion"`
		ClientCapabilities rawCaps        `json:"clientCapabilities"`
		Capabilities       rawCaps        `json:"capabilities"`
		ClientInfo         map[string]any `json:"clientInfo"`
		Info               map[string]any `json:"info"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	version := negotiate(in.ProtocolVersion)
	// Read capabilities under the name the negotiated version uses; a v1
	// client never sends `capabilities`, a v2 one never `clientCapabilities`.
	caps := in.ClientCapabilities
	info := in.ClientInfo
	if version == ProtocolV2 {
		caps, info = in.Capabilities, in.Info
		if caps == nil {
			caps = in.ClientCapabilities
		}
	}
	client := parseClientCaps(caps)
	client.info = info
	if version == ProtocolV2 {
		// v2 has no client execution surface (protocol/v2/migration "Client
		// file system and terminal execution removed"); a stray v1 marker in
		// a v2 initialize must not switch it back on.
		client.readTextFile, client.writeTextFile, client.terminal = false, false, false
	}

	a.mu.Lock()
	a.version = version
	a.client = client
	a.initialized = true
	a.mu.Unlock()

	if version == ProtocolV1 {
		return a.initializeV1(client), nil
	}
	return a.initializeV2(client), nil
}

func (a *Agent) implementation() obj {
	return obj{"name": "gocode", "title": "gocode", "version": a.host.Version}
}

func (a *Agent) authMethodsV1(client clientCaps) []obj {
	methods := []obj{{
		"id":          authMethodID,
		"name":        "Log in with gocode",
		"description": "Run `gocode auth login` in a terminal, then retry.",
	}}
	// A terminal method may only be offered to a client that can reproduce
	// the agent invocation interactively (protocol/v1/authentication).
	if client.terminalAuth {
		methods = append(methods, obj{
			"id":          terminalAuthMethodID,
			"type":        "terminal",
			"name":        "Log in from the terminal",
			"description": "Sign in to a model provider interactively.",
			"args":        a.loginArgs(),
		})
	}
	return methods
}

func (a *Agent) authMethodsV2(client clientCaps) []obj {
	methods := []obj{{
		"methodId":    authMethodID,
		"type":        "agent",
		"name":        "Log in with gocode",
		"description": "Run `gocode auth login` in a terminal, then retry.",
	}}
	if client.terminalAuth {
		methods = append(methods, obj{
			"methodId":    terminalAuthMethodID,
			"type":        "terminal",
			"name":        "Log in from the terminal",
			"description": "Sign in to a model provider interactively.",
			"args":        a.loginArgs(),
		})
	}
	return methods
}

func (a *Agent) loginArgs() []string {
	if len(a.host.LoginArgs) > 0 {
		return a.host.LoginArgs
	}
	return []string{"--login"}
}

// initializeV1 advertises the v1 surface. Mirrors the response built in
// packages/opencode/src/acp/service.ts's initialize, extended with the
// capabilities this port implements beyond it (delete, additional roots,
// logout).
func (a *Agent) initializeV1(client clientCaps) obj {
	return obj{
		"protocolVersion": ProtocolV1,
		"agentCapabilities": obj{
			"loadSession": true,
			"promptCapabilities": obj{
				"image":           true,
				"audio":           false,
				"embeddedContext": true,
			},
			"mcpCapabilities": obj{
				"http": true,
				"sse":  true,
			},
			"sessionCapabilities": obj{
				"list":                  obj{},
				"resume":                obj{},
				"close":                 obj{},
				"delete":                obj{},
				"additionalDirectories": obj{},
			},
			"auth": obj{"logout": obj{}},
			"_meta": obj{
				"gocode": obj{"fork": true},
			},
		},
		"agentInfo":   a.implementation(),
		"authMethods": a.authMethodsV1(client),
	}
}

// initializeV2 advertises the v2 surface: role-agnostic `capabilities` and
// `info`, object support markers, and session-scoped groups under
// `session` (protocol/v2/initialization). Advertising `session` commits to
// the baseline methods new/list/resume/close/prompt/cancel/update.
func (a *Agent) initializeV2(client clientCaps) obj {
	return obj{
		"protocolVersion": ProtocolV2,
		"info":            a.implementation(),
		"capabilities": obj{
			"session": obj{
				"prompt": obj{
					"image":           obj{},
					"embeddedContext": obj{},
				},
				"mcp": obj{
					"stdio": obj{},
					"http":  obj{},
				},
				"delete":                obj{},
				"additionalDirectories": obj{},
			},
			"_meta": obj{
				"gocode": obj{"fork": true},
			},
		},
		// Returning methods commits the agent to both auth/login and
		// auth/logout (protocol/v2/authentication).
		"authMethods": a.authMethodsV2(client),
	}
}

// authenticate serves v1 authenticate and v2 auth/login. The one
// protocol-driven method succeeds when credentials already exist; there is no
// way to type a secret over ACP, and form elicitation must never be used for
// one (protocol/v1/elicitation "Form mode").
func (a *Agent) authenticate(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		MethodID string `json:"methodId"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	switch in.MethodID {
	case authMethodID:
	case terminalAuthMethodID:
		return nil, invalidParams("%s is a terminal method: run it in a terminal instead of calling authenticate", in.MethodID)
	default:
		return nil, invalidParams("unknown authentication method %q", in.MethodID)
	}
	if a.host.Auth != nil && !a.host.Auth.Authenticated(ctx) {
		return nil, authRequired("No credentials for the configured model provider. Run `gocode auth login` in a terminal, then try again.")
	}
	return obj{}, nil
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
