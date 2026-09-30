// Package acp implements the Agent side of the Agent Client Protocol
// (agentclientprotocol.com): the JSON-RPC surface an editor uses to drive
// gocode as a coding agent over stdio.
//
// Both stable protocol versions are served. The version is negotiated once per
// connection in initialize (protocol/v1/initialization, protocol/v2/migration
// "Supporting v1 and v2 side by side"), and from then on the connection speaks
// exactly one: the method table, the capability shapes, and every
// session/update variant are chosen by Conn.version. Everything underneath —
// sessions, prompt turns, permission asks, event translation — is shared.
//
// Ports packages/opencode/src/acp (service.ts, event.ts, tool.ts,
// permission.ts, config-option.ts, usage.ts), which serves v1 over the
// TypeScript SDK. v2, the client fs/terminal delegation and the
// multi-directory runtime pool have no TypeScript counterpart.
package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/command"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/jsonrpc"
	"github.com/langazov/gocode-go/internal/mcp"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/session"
)

// Protocol versions this agent speaks.
const (
	ProtocolV1     = 1
	ProtocolV2     = 2
	LatestProtocol = ProtocolV2
)

// Runtime is one booted gocode runtime, rooted at one directory. The host
// (cmd/gocode) builds it from a stack; the package cannot import that stack
// directly, and only needs these pieces of it.
type Runtime struct {
	Directory   string
	Sessions    *session.Service
	Bus         *event.Bus
	Runner      *session.Runner
	Permissions *permission.Engine
	Questions   *question.Service
	Agents      *agent.Registry
	Commands    *command.Registry
	MCP         *mcp.Service
	// Models lists the models the user can reach, for the model selector.
	Models func(ctx context.Context) []Model
	// Close releases the runtime. Called once, when the agent shuts down.
	Close func() error
}

// Model is one selectable model.
type Model struct {
	ProviderID   string
	ProviderName string
	ID           string
	Name         string
	ContextLimit int
	Variants     []string
}

// Auth is the credential surface behind authenticate / auth/login and
// logout / auth/logout.
type Auth interface {
	// Authenticated reports whether the default model's provider has usable
	// credentials, which is what a successful login has to mean here: gocode
	// signs in with `gocode auth login`, not over the protocol.
	Authenticated(ctx context.Context) bool
	// Logout removes the stored credential of the default model's provider.
	Logout(ctx context.Context) error
}

// Host is what the agent needs from the process that runs it.
type Host struct {
	// Boot builds the runtime for an absolute directory. The agent calls it
	// at most once per directory and keeps the result.
	Boot func(ctx context.Context, directory string) (*Runtime, error)
	// Store reads and deletes sessions across every directory, without a
	// runtime: session/list must not boot one per project it lists.
	Store *session.Service
	// DefaultDirectory is used when a request that needs a runtime does not
	// name a directory of its own.
	DefaultDirectory string
	Version          string
	Auth             Auth
	// LoginArgs are appended to the client's own invocation of this agent to
	// run the interactive login (a terminal auth method). The client derives
	// the command itself; only the arguments come from here.
	LoginArgs []string
	// Log receives diagnostics. It must never write to stdout, which belongs
	// to the protocol.
	Log func(format string, args ...any)
}

// Agent serves one ACP connection.
type Agent struct {
	host Host
	conn *jsonrpc.Conn

	mu       sync.Mutex
	version  int
	client   clientCaps
	runtimes map[string]*runtimeEntry
	sessions map[string]*acpSession
	// ready is closed once initialize has negotiated a version; methods that
	// arrive earlier are refused.
	initialized bool

	// elicitations tracks outstanding URL-mode elicitation ids, which must be
	// unique per connection (protocol/v1/elicitation "URL completion").
	elicitations map[string]bool
}

// runtimeEntry boots a runtime once, however many requests race for it.
type runtimeEntry struct {
	once    sync.Once
	runtime *Runtime
	err     error
}

// Serve runs the agent over r/w until r is exhausted, then closes every
// runtime it booted.
func Serve(ctx context.Context, r io.Reader, w io.WriteCloser, host Host) error {
	agent := New(host, r, w)
	agent.conn.Listen()
	agent.shutdown()
	return nil
}

// New builds an agent on a newline-delimited JSON-RPC connection. Call
// Listen on Conn to start it; Serve does both.
func New(host Host, r io.Reader, w io.WriteCloser) *Agent {
	if host.Log == nil {
		host.Log = func(string, ...any) {}
	}
	a := &Agent{
		host:         host,
		conn:         jsonrpc.NewLineConn(w, r),
		runtimes:     map[string]*runtimeEntry{},
		sessions:     map[string]*acpSession{},
		elicitations: map[string]bool{},
	}
	a.conn.SetCancelRequest("$/cancel_request", "requestId")
	// Messages are handled in arrival order — initialize before whatever the
	// client pipelined behind it, a config change before the prompt that
	// follows it. A handler that has to wait (a v1 prompt lasts a whole
	// turn) returns a jsonrpc.Deferred, which frees the queue at once.
	a.conn.SetOrderedDispatch(true)
	// A client that closes its end still gets the replies to what it sent;
	// running turns are cancelled first, so a pending prompt answers promptly
	// with the cancelled stop reason instead of holding the process open.
	a.conn.SetDrainOnEOF(a.cancelAll)
	a.conn.SetMissingMethod(func(method string) (any, error) {
		return nil, &jsonrpc.RPCError{Code: jsonrpc.CodeMethodNotFound, Message: "Method not found: " + method}
	})
	a.register()
	return a
}

// Conn exposes the connection, for tests and for Serve.
func (a *Agent) Conn() *jsonrpc.Conn { return a.conn }

// register installs every method of both versions. Each handler checks the
// negotiated version itself, so a method that does not exist in the
// connection's version answers MethodNotFound exactly as an unknown one does.
func (a *Agent) register() {
	a.handle("initialize", anyVersion, a.initialize)
	// Authentication: v1 names, then v2 names.
	a.handle("authenticate", onlyV1, a.authenticate)
	a.handle("logout", onlyV1, a.logout)
	a.handle("auth/login", onlyV2, a.authenticate)
	a.handle("auth/logout", onlyV2, a.logout)
	// Sessions.
	a.handle("session/new", anyVersion, a.newSession)
	a.handle("session/load", onlyV1, a.loadSession)
	a.handle("session/resume", anyVersion, a.resumeSession)
	a.handle("session/list", anyVersion, a.listSessions)
	a.handle("session/close", anyVersion, a.closeSession)
	a.handle("session/delete", anyVersion, a.deleteSession)
	a.handle("session/prompt", anyVersion, a.prompt)
	a.handle("session/set_mode", onlyV1, a.setMode)
	a.handle("session/set_config_option", anyVersion, a.setConfigOption)
	// Extension: forking is an RFD, not part of either stable version, so it
	// is offered under an extension name (protocol/v1/extensibility).
	a.handle("_gocode/session/fork", anyVersion, a.forkSession)

	a.conn.OnNotify("session/cancel", func(params json.RawMessage) {
		if !a.ready() {
			return
		}
		var in struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(params, &in) == nil {
			a.cancel(in.SessionID)
		}
	})
}

type versionGate int

const (
	anyVersion versionGate = iota
	onlyV1
	onlyV2
)

// handle registers a method behind the initialize gate and its version gate.
func (a *Agent) handle(method string, gate versionGate, fn func(ctx context.Context, params json.RawMessage) (any, error)) {
	a.conn.HandleContext(method, func(ctx context.Context, params json.RawMessage) (any, error) {
		if method != "initialize" {
			if !a.ready() {
				return nil, &jsonrpc.RPCError{Code: jsonrpc.CodeInvalidRequest, Message: "connection not initialized: call initialize first"}
			}
			version := a.protocolVersion()
			if (gate == onlyV1 && version != ProtocolV1) || (gate == onlyV2 && version != ProtocolV2) {
				return nil, &jsonrpc.RPCError{Code: jsonrpc.CodeMethodNotFound, Message: "Method not found: " + method}
			}
		}
		return fn(ctx, params)
	})
}

func (a *Agent) ready() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.initialized
}

func (a *Agent) protocolVersion() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.version
}

func (a *Agent) clientCapabilities() clientCaps {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client
}

// runtime returns the runtime for directory, booting it on first use.
func (a *Agent) runtime(ctx context.Context, directory string) (*Runtime, error) {
	if directory == "" {
		directory = a.host.DefaultDirectory
	}
	directory = filepath.Clean(directory)
	a.mu.Lock()
	entry := a.runtimes[directory]
	if entry == nil {
		entry = &runtimeEntry{}
		a.runtimes[directory] = entry
	}
	a.mu.Unlock()
	entry.once.Do(func() {
		// Boot outlives the request that triggered it: the runtime serves
		// every later session in the directory.
		entry.runtime, entry.err = a.host.Boot(context.WithoutCancel(ctx), directory)
		if entry.err == nil {
			a.attach(entry.runtime)
		}
	})
	if entry.err != nil {
		return nil, internalError("starting gocode in %s: %v", directory, entry.err)
	}
	return entry.runtime, nil
}

// cancelAll cancels every session's foreground work.
func (a *Agent) cancelAll() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.sessions))
	for id := range a.sessions {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.cancel(id)
	}
}

// shutdown cancels every live turn and closes the booted runtimes.
func (a *Agent) shutdown() {
	a.mu.Lock()
	sessions := make([]*acpSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	entries := make([]*runtimeEntry, 0, len(a.runtimes))
	for _, entry := range a.runtimes {
		entries = append(entries, entry)
	}
	a.mu.Unlock()
	for _, s := range sessions {
		s.runtime.Sessions.Interrupt(s.id)
	}
	for _, entry := range entries {
		if entry.runtime != nil && entry.runtime.Close != nil {
			if err := entry.runtime.Close(); err != nil {
				a.host.Log("acp: closing runtime %s: %v", entry.runtime.Directory, err)
			}
		}
	}
}

// notify sends a notification, logging rather than failing on a dead pipe:
// every caller is reporting state, and a client that went away cannot be
// told about it.
func (a *Agent) notify(method string, params any) {
	if err := a.conn.Notify(method, params); err != nil {
		a.host.Log("acp: notify %s: %v", method, err)
	}
}

// update sends one session/update notification.
func (a *Agent) update(sessionID string, update obj) {
	a.notify("session/update", obj{"sessionId": sessionID, "update": update})
}

// call sends an agent→client request.
func (a *Agent) call(ctx context.Context, method string, params, out any) error {
	return a.conn.Call(ctx, method, params, out)
}

// obj is an outbound JSON object. Building updates as maps rather than
// structs gives the v2 three-state patch semantics for free: an absent key is
// "unchanged", a nil value is an explicit null ("clear"), anything else
// replaces (protocol/v2/migration "Updates are upserts").
type obj = map[string]any

// Error helpers. ACP reuses JSON-RPC's codes plus its own reserved range
// (protocol/v1/schema#errorcode).
const (
	codeAuthRequired     = -32000
	codeResourceNotFound = -32002
)

func invalidParams(format string, args ...any) error {
	return &jsonrpc.RPCError{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf(format, args...)}
}

func internalError(format string, args ...any) error {
	return &jsonrpc.RPCError{Code: jsonrpc.CodeInternalError, Message: fmt.Sprintf(format, args...)}
}

func authRequired(message string) error {
	return &jsonrpc.RPCError{Code: codeAuthRequired, Message: message}
}

func resourceNotFound(format string, args ...any) error {
	return &jsonrpc.RPCError{Code: codeResourceNotFound, Message: fmt.Sprintf(format, args...)}
}

// decode unmarshals params, turning a malformed payload into InvalidParams.
func decode(params json.RawMessage, into any) error {
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage("{}")
	}
	if err := json.Unmarshal(params, into); err != nil {
		return invalidParams("invalid params: %v", err)
	}
	return nil
}

// sessionNotFound reports an unknown session id the way both versions do.
func sessionNotFound(sessionID string) error {
	return resourceNotFound("session not found: %s", sessionID)
}
