package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anomalyco/opencode-go/internal/global"
	"github.com/anomalyco/opencode-go/internal/tool"
)

// defaultTimeout mirrors DEFAULT_TIMEOUT in mcp/index.ts — the code
// constant actually used when a server has no configured timeout (the
// TS CLI's own docstrings claim 5000ms, but the code uses 30s).
const defaultTimeout = 30 * time.Second

// Status mirrors the TS connection Status union: "connected" | "disabled" |
// "failed" | "needs_auth" | "needs_client_registration".
type Status struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func (s Status) Connected() bool { return s.Status == "connected" }

// connection holds one server's live state.
type connection struct {
	name         string
	config       ServerConfig
	status       Status
	session      *sdkmcp.ClientSession
	tools        []*sdkmcp.Tool
	instructions string
}

// Service is this port's MCP.Service: connects configured servers, tracks
// their status, and exposes their tools/prompts/resources.
type Service struct {
	mu        sync.RWMutex
	conns     map[string]*connection
	directory string
	client    *sdkmcp.Client
	registry  *tool.Registry

	// watchCtx/watchCancel bound every background reconnect-retry goroutine
	// (see watchAndReconnect/reconnectLoop): canceled once, by Close, so a
	// process shutdown stops every pending retry instead of leaking
	// goroutines that keep hammering a dead server forever.
	watchCtx    context.Context
	watchCancel context.CancelFunc

	// reconnectMinBackoff/reconnectMaxBackoff bound the retry delay for a
	// server that failed to connect (initially, or after an established
	// connection dropped): doubling from a 5s floor up to a 60s cap by
	// default. Not part of the TS port (index.ts has no auto-reconnect at
	// all — a dropped server just sits "failed" until the user re-runs
	// `mcp connect`/`mcp auth`) but requested directly for this Go port's
	// long-running TUI/serve processes. Per-instance (not a package
	// constant) so tests can shrink them without a shared mutable global
	// racing other tests' background goroutines.
	reconnectMinBackoff time.Duration
	reconnectMaxBackoff time.Duration
}

// NewService creates the shared MCP client identity ("opencode", matching
// createClient in index.ts) and registers directory as the sole root.
func NewService(directory string) *Service {
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "opencode", Version: "0.1.0"}, nil)
	client.AddRoots(&sdkmcp.Root{URI: "file://" + directory})
	watchCtx, watchCancel := context.WithCancel(context.Background())
	return &Service{
		conns:               map[string]*connection{},
		directory:           directory,
		client:              client,
		watchCtx:            watchCtx,
		watchCancel:         watchCancel,
		reconnectMinBackoff: 5 * time.Second,
		reconnectMaxBackoff: 60 * time.Second,
	}
}

// SetRegistry wires a tool registry that the service keeps in sync on its
// own from then on: whenever a server (re)connects — the initial load, a
// manual reconnect, or an automatic retry after a drop — its tools are
// registered into registry without the caller calling RegisterTools again.
func (s *Service) SetRegistry(registry *tool.Registry) {
	s.mu.Lock()
	s.registry = registry
	s.mu.Unlock()
}

// setReconnectBackoff overrides the retry backoff bounds; test-only
// (unexported) so it's unreachable from production code that might
// otherwise be tempted to spin-loop reconnect attempts.
func (s *Service) setReconnectBackoff(minBackoff, maxBackoff time.Duration) {
	s.mu.Lock()
	s.reconnectMinBackoff = minBackoff
	s.reconnectMaxBackoff = maxBackoff
	s.mu.Unlock()
}

// Load connects every enabled server in parallel, mirroring the
// concurrency:"unbounded" initial load in mcp/index.ts, and blocks until
// every one of them has resolved (connected or failed). Used where the
// caller needs a definitive one-shot answer, like `mcp list`/`mcp debug`;
// it does not retry a failure and does not watch a success for a later
// drop — see LoadAsync for the backgrounded, auto-reconnecting equivalent
// used at process boot.
func (s *Service) Load(ctx context.Context, servers map[string]ServerConfig) {
	var wg sync.WaitGroup
	for name, cfg := range servers {
		wg.Add(1)
		go func(name string, cfg ServerConfig) {
			defer wg.Done()
			s.connect(ctx, name, cfg, modePassive, nil)
		}(name, cfg)
	}
	wg.Wait()
}

// LoadAsync is Load's non-blocking counterpart, run in its own goroutine
// per server so process boot (bootStack) never waits on a slow or
// unreachable MCP server: every configured server is marked "connecting"
// synchronously before this returns (so Statuses() never reports one as
// simply missing during startup), then connected in the background. A
// server that fails to connect — or later drops after connecting, via the
// per-session watcher every successful connect spawns — is retried with
// backoff (reconnectLoop) until it succeeds or the service is Closed.
func (s *Service) LoadAsync(servers map[string]ServerConfig) {
	for name, cfg := range servers {
		s.setConn(name, &connection{name: name, config: cfg, status: Status{Status: "connecting"}})
	}
	for name, cfg := range servers {
		go func(name string, cfg ServerConfig) {
			if status := s.connect(s.watchCtx, name, cfg, modePassive, nil); status.Status == "failed" {
				s.reconnectLoop(name, cfg)
			}
		}(name, cfg)
	}
}

// Connect (re)connects one server, matching the CLI/API's dynamic connect.
func (s *Service) Connect(ctx context.Context, name string, cfg ServerConfig) Status {
	return s.connect(ctx, name, cfg, modePassive, nil)
}

// Disconnect closes a server's live session (config is untouched; a later
// Connect reconnects it), matching disconnect() in index.ts.
func (s *Service) Disconnect(name string) {
	s.mu.Lock()
	conn, ok := s.conns[name]
	if ok {
		delete(s.conns, name)
	}
	s.mu.Unlock()
	if ok && conn.session != nil {
		conn.session.Close()
	}
}

func (s *Service) connect(ctx context.Context, name string, cfg ServerConfig, mode oauthMode, onAuthURL func(string)) Status {
	if !cfg.IsEnabled() {
		status := Status{Status: "disabled"}
		s.setConn(name, &connection{name: name, config: cfg, status: status})
		return status
	}

	timeout := time.Duration(cfg.TimeoutOr(int(defaultTimeout/time.Millisecond))) * time.Millisecond
	connectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var session *sdkmcp.ClientSession
	var err error
	switch cfg.Type {
	case "local":
		session, err = s.connectLocal(connectCtx, name, cfg)
	case "remote":
		session, err = s.connectRemote(connectCtx, name, cfg, mode, onAuthURL)
	default:
		err = fmt.Errorf("mcp: unknown server type %q", cfg.Type)
	}

	if err != nil {
		status := classifyError(err)
		s.setConn(name, &connection{name: name, config: cfg, status: status})
		return status
	}

	var tools []*sdkmcp.Tool
	var instructions string
	if res := session.InitializeResult(); res != nil {
		instructions = strings.TrimSpace(res.Instructions)
		if res.Capabilities != nil && res.Capabilities.Tools != nil {
			for t, terr := range session.Tools(connectCtx, nil) {
				if terr != nil {
					session.Close()
					status := classifyError(terr)
					s.setConn(name, &connection{name: name, config: cfg, status: status})
					return status
				}
				tools = append(tools, t)
			}
		}
	}

	status := Status{Status: "connected"}
	conn := &connection{name: name, config: cfg, status: status, session: session, tools: tools, instructions: instructions}
	s.setConn(name, conn)
	s.registerConnTools(conn)
	go s.watchAndReconnect(name, cfg, session)
	return status
}

// watchAndReconnect blocks until session's underlying connection closes —
// whether from an explicit Close() elsewhere or the server/process dying
// out from under it — then, unless the service itself is shutting down or
// this session has already been superseded by a newer connect/disconnect,
// marks the server failed and hands off to reconnectLoop. Every successful
// connect (initial, manual, or a retry) spawns exactly one of these for its
// session.
func (s *Service) watchAndReconnect(name string, cfg ServerConfig, session *sdkmcp.ClientSession) {
	session.Wait()

	select {
	case <-s.watchCtx.Done():
		return
	default:
	}

	s.mu.Lock()
	conn, ok := s.conns[name]
	current := ok && conn.session == session
	if current {
		conn.session = nil
		conn.tools = nil
		conn.instructions = ""
		conn.status = Status{Status: "failed", Error: "connection closed"}
	}
	s.mu.Unlock()
	if !current {
		return
	}

	s.reconnectLoop(name, cfg)
}

// reconnectLoop retries connect with capped exponential backoff until it
// succeeds (connect's own success path starts a fresh watchAndReconnect for
// the new session, so this loop's job ends there), the server is taken
// over by someone else's Connect/Disconnect in the meantime, or the
// service is closed.
func (s *Service) reconnectLoop(name string, cfg ServerConfig) {
	s.mu.RLock()
	backoff, maxBackoff := s.reconnectMinBackoff, s.reconnectMaxBackoff
	s.mu.RUnlock()
	for {
		select {
		case <-s.watchCtx.Done():
			return
		case <-time.After(backoff):
		}

		s.mu.RLock()
		conn, ok := s.conns[name]
		takenOver := !ok || conn.session != nil
		s.mu.RUnlock()
		if takenOver {
			return
		}

		if status := s.connect(s.watchCtx, name, cfg, modePassive, nil); status.Status == "connected" {
			return
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// registerConnTools registers one connected server's tools into the
// service's wired registry (see SetRegistry), if any is set. A no-op
// before SetRegistry has been called, e.g. for the CLI's short-lived
// `mcp list`/`mcp debug` services that never register tools at all.
func (s *Service) registerConnTools(conn *connection) {
	s.mu.RLock()
	registry := s.registry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	timeout := time.Duration(conn.config.TimeoutOr(int(defaultTimeout/time.Millisecond))) * time.Millisecond
	for _, def := range conn.tools {
		registry.Register(&mcpTool{clientName: conn.name, def: def, session: conn.session, timeout: timeout})
	}
}

func (s *Service) setConn(name string, conn *connection) {
	s.mu.Lock()
	old := s.conns[name]
	s.conns[name] = conn
	s.mu.Unlock()
	if old != nil && old.session != nil && old.session != conn.session {
		old.session.Close()
	}
}

// connectLocal mirrors connectLocal() in index.ts: spawn the command over
// stdio via [sdkmcp.CommandTransport].
func (s *Service) connectLocal(ctx context.Context, name string, cfg ServerConfig) (*sdkmcp.ClientSession, error) {
	if len(cfg.Command) == 0 {
		return nil, errors.New("mcp: local server has no command")
	}
	// Deliberately not exec.CommandContext(ctx, ...): ctx here is connect()'s
	// short-lived per-connect timeout, canceled the moment connect() returns
	// (including on the success path, right after this function returns) —
	// tying the subprocess to it would kill the server the instant the
	// initial handshake finished, breaking every tool call made afterwards.
	// The subprocess's lifetime is instead governed by CommandTransport's own
	// graceful-shutdown-then-SIGTERM Close(), invoked when the session is
	// closed (Disconnect/Service.Close).
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	if cfg.Cwd != "" {
		cmd.Dir = cfg.Cwd
	} else {
		cmd.Dir = s.directory
	}
	env := os.Environ()
	for k, v := range cfg.Environment {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	if logFile, err := mcpLogFile(name); err == nil {
		cmd.Stderr = logFile
	}
	transport := &sdkmcp.CommandTransport{Command: cmd}
	return s.client.Connect(ctx, transport, nil)
}

// mcpLogFile opens (creating if needed) the per-server log file that a local
// MCP server's stderr is redirected to, instead of the process's own
// stderr — a local server's diagnostic output would otherwise interleave
// with and corrupt the TUI's alternate-screen rendering.
func mcpLogFile(name string) (*os.File, error) {
	dir := global.Resolve().Log
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "mcp-"+sanitize(name)+".log")
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

// connectRemote mirrors connectRemote() in index.ts: try streamable HTTP,
// then fall back to SSE (the legacy 2024-11-05 transport), both with the
// same OAuth handler and custom headers attached.
func (s *Service) connectRemote(ctx context.Context, name string, cfg ServerConfig, mode oauthMode, onAuthURL func(string)) (*sdkmcp.ClientSession, error) {
	if cfg.URL == "" {
		return nil, errors.New("mcp: remote server has no url")
	}

	var oauthHandler sdkauth.OAuthHandler
	if !cfg.OAuth.Disabled {
		h, err := newOAuthHandler(name, cfg, mode, onAuthURL)
		if err != nil {
			return nil, err
		}
		oauthHandler = h
	}
	httpClient := &http.Client{Transport: &headerRoundTripper{headers: cfg.Headers}}

	streamable := &sdkmcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient, OAuthHandler: oauthHandler}
	session, err := s.client.Connect(ctx, streamable, nil)
	if err == nil {
		return session, nil
	}
	firstErr := err

	sse := &sdkmcp.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: httpClient}
	session, err = s.client.Connect(ctx, sse, nil)
	if err != nil {
		// Prefer the first (streamable) error unless the SSE attempt hit an
		// auth-class failure the streamable attempt didn't (matches TS: an
		// auth-class error stops the fallback chain, so the more informative
		// one — whichever classifies as auth — wins).
		if errors.Is(err, errAuthRequired) || !errors.Is(firstErr, errAuthRequired) {
			return nil, err
		}
		return nil, firstErr
	}
	return session, nil
}

// headerRoundTripper injects a remote server's configured static headers
// (mcp.headers) into every outgoing request, matching connectRemote's
// requestInit.headers in index.ts. It composes with OAuthHandler, which
// sets the Authorization header separately (the SDK transport calls it
// directly, not through this client).
type headerRoundTripper struct {
	headers map[string]string
	base    http.RoundTripper
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	base := h.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// classifyError maps a connect failure to a Status, mirroring connectRemote's
// UnauthorizedError handling in index.ts.
func classifyError(err error) Status {
	if errors.Is(err, errAuthRequired) {
		return Status{Status: "needs_auth"}
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "regist") || strings.Contains(lower, "client_id") {
		return Status{Status: "needs_client_registration", Error: msg}
	}
	return Status{Status: "failed", Error: msg}
}

// Status returns every configured server's current connection status,
// sorted by name.
func (s *Service) Statuses() map[string]Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Status, len(s.conns))
	for name, conn := range s.conns {
		out[name] = conn.status
	}
	return out
}

// ServerNames returns the connected server names, sorted.
func (s *Service) ServerNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.conns))
	for name, conn := range s.conns {
		if conn.status.Connected() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Instructions returns each connected server's trimmed instructions text,
// keyed by server name, matching instructions() in index.ts.
func (s *Service) Instructions() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]string{}
	for name, conn := range s.conns {
		if conn.status.Connected() && conn.instructions != "" {
			out[name] = conn.instructions
		}
	}
	return out
}

// Close disconnects every live session (best-effort), for process shutdown.
// Close cancels every background reconnect loop first, then closes every
// live session — in that order, so a watcher whose session.Wait() unblocks
// as a result always sees watchCtx already canceled and exits quietly
// instead of marking the server failed and starting a doomed reconnect
// attempt after the service is gone.
func (s *Service) Close() {
	s.watchCancel()
	s.mu.Lock()
	conns := s.conns
	s.conns = map[string]*connection{}
	s.mu.Unlock()
	for _, conn := range conns {
		if conn.session != nil {
			conn.session.Close()
		}
	}
}

// RegisterTools adds every currently-connected server's tools into
// registry, named via ToolName — the merge session/tools.ts does per-turn,
// done once here at boot instead (this port connects all configured
// servers once at startup rather than per-instance/session; see
// go-port-gaps.md). Kept for callers that want an explicit one-shot sync
// (e.g. tests connecting a single server manually via Connect); process
// boot instead calls SetRegistry once and lets the service register each
// server automatically as it (re)connects.
func (s *Service) RegisterTools(registry *tool.Registry) {
	s.mu.RLock()
	conns := make([]*connection, 0, len(s.conns))
	for _, conn := range s.conns {
		if conn.status.Connected() {
			conns = append(conns, conn)
		}
	}
	s.mu.RUnlock()
	for _, conn := range conns {
		timeout := time.Duration(conn.config.TimeoutOr(int(defaultTimeout/time.Millisecond))) * time.Millisecond
		for _, def := range conn.tools {
			registry.Register(&mcpTool{clientName: conn.name, def: def, session: conn.session, timeout: timeout})
		}
	}
}

// Authenticate mirrors authenticate() in index.ts: a full interactive
// reconnect (opens a browser, waits for the OAuth redirect, then
// reconnects and re-lists tools), used by `opencode mcp auth <name>`.
func (s *Service) Authenticate(ctx context.Context, name string, cfg ServerConfig, onAuthURL func(string)) (Status, error) {
	if cfg.Type != "remote" {
		return Status{}, fmt.Errorf("mcp server %s is not a remote server", name)
	}
	if cfg.OAuth.Disabled {
		return Status{}, fmt.Errorf("mcp server %s has OAuth explicitly disabled", name)
	}
	status := s.connect(ctx, name, cfg, modeInteractive, onAuthURL)
	if status.Status != "connected" {
		if status.Error != "" {
			return status, errors.New(status.Error)
		}
		return status, fmt.Errorf("authentication failed: %s", status.Status)
	}
	return status, nil
}

// GetAuthStatus mirrors getAuthStatus() in index.ts: a simple 3-state
// client-perceived credential status, distinct from the connection Status
// above (which also covers non-OAuth failure modes).
func GetAuthStatus(name string, cfg ServerConfig) string {
	if cfg.Type != "remote" || cfg.URL == "" {
		return "not_authenticated"
	}
	entry, ok := StoreGet(name, cfg.URL)
	if !ok || !entry.HasTokens() {
		return "not_authenticated"
	}
	if entry.Expiry > 0 && entry.Expiry < time.Now().Unix() {
		return "expired"
	}
	return "authenticated"
}

// HasStoredTokens mirrors hasStoredTokens() in index.ts.
func HasStoredTokens(name string, cfg ServerConfig) bool {
	entry, ok := StoreGet(name, cfg.URL)
	return ok && entry.HasTokens()
}

// RemoveAuth mirrors removeAuth() in index.ts (the pending-OAuth-callback
// cancellation TS also does has no equivalent here: this port's callback
// server is a one-shot per authFetcher call, not a shared long-lived one).
func RemoveAuth(name string) error {
	return StoreRemove(name)
}

// SupportsOAuth mirrors supportsOAuth() in index.ts.
func SupportsOAuth(cfg ServerConfig) bool {
	return cfg.Type == "remote" && !cfg.OAuth.Disabled
}
