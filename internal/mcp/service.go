package mcp

import (
	"context"
	"encoding/json"
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

	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/tool"
)

// defaultTimeout is the connect timeout when a server has no configured
// timeout. Deliberate divergence from DEFAULT_TIMEOUT in mcp/index.ts (30s,
// despite its docstrings claiming 5000ms): here 60s, because a remote
// connect is not one request — OAuth discovery, (dynamic) client
// registration and token exchange all run inside this budget, and 30s cut
// real flows short (`mcp auth stripe` died mid-browser-flow with
// "context deadline exceeded"). A dead server still fails fast: the
// streamable attempt usually errors in single-digit seconds, and only a
// server that accepts the connection then stalls burns the full budget.
const defaultTimeout = 60 * time.Second

// defaultToolTimeout is what one MCP tool call may take when the server has
// no configured timeout. Deliberate divergence from the TS port: there the
// per-call timeout falls through to the TS SDK's DEFAULT_REQUEST_TIMEOUT_MSEC
// (60s), here it is 5 minutes — remote MCP servers regularly host slow tools
// (code search over a monorepo, batch database queries) and a 60s ceiling
// reports those as failures mid-run instead of letting them finish. A
// per-server `mcp.<name>.timeout` (ms) still overrides this, as it does the
// 60s connect budget above.
const defaultToolTimeout = 300 * time.Second

// interactiveAuthTimeout is how long `gocode mcp auth <name>` waits for the
// user to finish the browser consent round-trip, enforced by oauth.go's
// authFetcher. The connect deadline is widened by this much while an
// interactive flow is in flight (see connect), so OAuth discovery, dynamic
// client registration and the final token exchange don't eat into the user's
// browser time and the authFetcher's own deadline — with its clearer
// "authorization timed out" error — is what fires, not the connect one.
// TS's authenticate() waits for the callback unboundedly; a hard ceiling
// keeps a forgotten browser tab from parking the CLI forever.
const interactiveAuthTimeout = 300 * time.Second

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
	// toolCount/estTokens are the latest measurement of this server's tool
	// set: live from the current connection, kept when the server is later
	// disabled mid-run, or restored from the persisted measurement in
	// data/mcp-usage.json (usage.go) for one that booted disabled — either
	// way the dialog can show what re-enabling it would cost back. Zeros
	// mean never measured anywhere: no live connect ever succeeded and no
	// measurement is stored, so the tool set is genuinely unknown.
	toolCount int
	estTokens int
}

// ServerInfo is one configured server for API surfaces that need more than
// the live status: the management endpoints' clients (the TUI's MCP dialog,
// the desktop app) render type, endpoint and enable state alongside the
// status dot. Disabled servers are included — a server turned off is still
// configured, and the dialog is where it gets turned back on.
type ServerInfo struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	URL     string   `json:"url,omitempty"`
	Command []string `json:"command,omitempty"`
	Enabled bool     `json:"enabled"`
	Status  Status   `json:"status"`
	// ToolCount is how many tools the server contributes when connected;
	// EstTokens the estimated context those tool definitions consume.
	// CostLive is true when those figures are in the context right now
	// (connected); false means last-known — a server disabled mid-run keeps
	// its measurement so the dialog can show what re-enabling costs, while
	// one that never connected this run reports zeros.
	ToolCount int          `json:"toolCount"`
	EstTokens int          `json:"estTokens"`
	CostLive  bool         `json:"costLive"`
	Config    ServerConfig `json:"-"`
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

	// usageProbed records the disabled servers this process has already
	// started a measure-once probe for (see probeUsageOnce): a server with
	// no stored measurement is probed at most once per run, however many
	// times connect() sees it disabled (Load, LoadAsync, Connect).
	usageProbed map[string]bool

	// usageProbeEnabled gates the background measure-once probe; test-only
	// (unexported setter), the same pattern as setReconnectBackoff, so a
	// test can assert seeded-from-store figures without the probe racing.
	usageProbeEnabled bool
}

// NewService creates the shared MCP client identity ("gocode", matching
// createClient in index.ts) and registers directory as the sole root.
func NewService(directory string) *Service {
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "gocode", Version: "0.1.0"}, nil)
	client.AddRoots(&sdkmcp.Root{URI: "file://" + directory})
	watchCtx, watchCancel := context.WithCancel(context.Background())
	return &Service{
		conns:               map[string]*connection{},
		usageProbed:         map[string]bool{},
		usageProbeEnabled:   true,
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
		s.unregisterConnTools(conn)
		conn.session.Close()
	}
}

// Disable is Disconnect plus the "disabled" bookkeeping: the server's entry
// stays (so it remains listed with a disabled status) and its config is
// remembered for a later Enable/Connect. Unlike Disconnect, a disabled
// server is never retried by a reconnect loop — reconnectLoop exits once
// the conn it retries holds no live session.
func (s *Service) Disable(name string) {
	s.mu.Lock()
	conn, ok := s.conns[name]
	if ok {
		delete(s.conns, name)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	s.unregisterConnTools(conn)
	// Persist the measurement a live connection carries, so the figures
	// survive a restart: the next boot finds the server disabled in config
	// and its cost in the usage store. Redundant with the persist-on-
	// connect in connect()'s success path when the figures are unchanged,
	// but a conn that never had a session (booted disabled, figures
	// restored from the store) should not rewrite the store either.
	if conn.session != nil {
		_ = UsageSet(name, conn.usageEntry())
	}
	if conn.session != nil {
		conn.session.Close()
	}
	s.setConn(name, &connection{
		name:   name,
		config: conn.config.WithEnabled(false),
		status: Status{Status: "disabled"},
		// Keep the last measurement: the dialog shows what re-enabling
		// this server would cost back, which is the number the user just
		// acted on — dropping it would blank the row the moment the
		// disable lands.
		toolCount: conn.toolCount,
		estTokens: conn.estTokens,
	})
}

// Enable reconnects a disabled server: connect() re-registers its tools and
// restarts the reconnect watcher, exactly like the initial LoadAsync path.
func (s *Service) Enable(ctx context.Context, name string, cfg ServerConfig) Status {
	return s.connect(ctx, name, cfg.WithEnabled(true), modePassive, nil)
}

func (s *Service) connect(ctx context.Context, name string, cfg ServerConfig, mode oauthMode, onAuthURL func(string)) Status {
	if !cfg.IsEnabled() {
		status := Status{Status: "disabled"}
		conn := &connection{name: name, config: cfg, status: status}
		seeded := seedUsage(name, cfg, conn)
		s.setConn(name, conn)
		// No usable measurement stored (never connected anywhere, or the
		// endpoint changed since it was taken): measure once in the
		// background rather than showing "—" until the first manual
		// enable. The probe never touches a registry — the tool
		// descriptions are counted, not advertised — and leaves the
		// server disabled, so nothing enters any model's context.
		if !seeded && s.usageProbeOn() && !s.markUsageProbed(name) {
			go s.probeUsageOnce(name, cfg, conn)
		}
		return status
	}

	timeout := time.Duration(cfg.TimeoutOr(int(defaultTimeout/time.Millisecond))) * time.Millisecond
	// An interactive auth (`mcp auth <name>`) runs the browser consent flow
	// inside this same deadline, and the plain connect budget is far too
	// small to hold a human round-trip: extend it by the authFetcher's own
	// wait (see oauth.go) plus headroom for the OAuth discovery and token
	// exchange on either side of it, so the deadline that fires is the
	// authFetcher's — whose error names the real problem ("authorization
	// timed out") — instead of a bare "context deadline exceeded" pointing
	// at the MCP endpoint URL.
	if mode == modeInteractive {
		timeout += interactiveAuthTimeout + time.Minute
	}
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
		conn := &connection{name: name, config: cfg, status: status}
		seedUsage(name, cfg, conn)
		s.setConn(name, conn)
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
	conn := &connection{
		name:         name,
		config:       cfg,
		status:       status,
		session:      session,
		tools:        tools,
		instructions: instructions,
		toolCount:    len(tools),
		estTokens:    toolTokens(name, tools),
	}
	s.setConn(name, conn)
	s.registerConnTools(conn)
	// Persist the measurement on every successful (re)connect, not only on
	// a later Disable: the store stays fresh as a server's tool set
	// changes, and a boot that finds the server disabled in config always
	// has a figure to seed from — even when the disable came from a config
	// edit outside this process rather than the dialog.
	_ = UsageSet(name, conn.usageEntry())
	go s.watchAndReconnect(name, cfg, session)
	return status
}

// usageEntry snapshots a connection's measurement with the endpoint
// fingerprint UsageGet validates against.
func (conn *connection) usageEntry() UsageEntry {
	return UsageEntry{
		Type:      conn.config.Type,
		URL:       conn.config.URL,
		Command:   conn.config.Command,
		ToolCount: conn.toolCount,
		EstTokens: conn.estTokens,
	}
}

// seedUsage fills a not-yet-published connection's context-cost figures
// from the persisted measurement when they are otherwise unknown, so every
// non-connected row — disabled at boot, failed, needs-auth — still shows
// what a successful enable would cost back instead of "—". Reports whether
// a measurement was found; a stored entry with zero tools counts as found
// (the server genuinely exposes none), which is what keeps such a server
// from being re-probed on every boot.
func seedUsage(name string, cfg ServerConfig, conn *connection) bool {
	if conn.toolCount > 0 || conn.estTokens > 0 {
		return true
	}
	entry, ok := UsageGet(name, cfg)
	if !ok {
		return false
	}
	conn.toolCount = entry.ToolCount
	conn.estTokens = entry.EstTokens
	return true
}

// probeUsageOnce measures a disabled server's tool set once, in the
// background: connect the endpoint with no registry involved (the tool
// descriptions are counted, never advertised — nothing enters any model's
// context), persist the figures for the next boot, then fold them into the
// disabled connection so the dialog's row picks them up on its next
// refresh. The probe session is closed before this returns — the server
// ends exactly as disabled as it started.
func (s *Service) probeUsageOnce(name string, cfg ServerConfig, conn *connection) {
	entry, ok := s.measure(s.watchCtx, name, cfg)
	if !ok {
		return
	}
	_ = UsageSet(name, entry)
	s.mu.Lock()
	// Only a conn that is still the one this probe was spawned for, and
	// still not connected, takes the figures: an Enable in the meantime
	// replaced it with a live measurement of its own.
	if cur, exists := s.conns[name]; exists && cur == conn && !cur.status.Connected() {
		cur.toolCount = entry.ToolCount
		cur.estTokens = entry.EstTokens
	}
	s.mu.Unlock()
}

// measure connects cfg's endpoint once, counts its tools and estimates the
// context tokens their definitions consume — the same estimate a live
// connect records — and closes the session again. It registers nothing
// anywhere: no registry is consulted, so measuring a disabled server never
// adds its tool descriptions to the context. ok is false when the endpoint
// could not be connected or its tools not listed (down, or needs auth);
// nothing is persisted in that case, and the next boot tries again.
func (s *Service) measure(ctx context.Context, name string, cfg ServerConfig) (UsageEntry, bool) {
	timeout := time.Duration(cfg.TimeoutOr(int(defaultTimeout/time.Millisecond))) * time.Millisecond
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var session *sdkmcp.ClientSession
	var err error
	switch cfg.Type {
	case "local":
		session, err = s.connectLocal(probeCtx, name, cfg)
	case "remote":
		// Passive: a server that needs interactive auth fails fast here
		// (errAuthRequired) instead of opening a browser — measuring a
		// disabled server must never demand user interaction.
		session, err = s.connectRemote(probeCtx, name, cfg, modePassive, nil)
	default:
		return UsageEntry{}, false
	}
	if err != nil {
		return UsageEntry{}, false
	}
	defer session.Close()

	var tools []*sdkmcp.Tool
	if res := session.InitializeResult(); res != nil && res.Capabilities != nil && res.Capabilities.Tools != nil {
		for t, terr := range session.Tools(probeCtx, nil) {
			if terr != nil {
				return UsageEntry{}, false
			}
			tools = append(tools, t)
		}
	}
	return UsageEntry{
		Type:      cfg.Type,
		URL:       cfg.URL,
		Command:   cfg.Command,
		ToolCount: len(tools),
		EstTokens: toolTokens(name, tools),
	}, true
}

// markUsageProbed records that name's measure-once probe has been started
// in this process, and reports whether it had already been started.
func (s *Service) markUsageProbed(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	already := s.usageProbed[name]
	s.usageProbed[name] = true
	return already
}

// usageProbeOn reads the probe gate under the lock.
func (s *Service) usageProbeOn() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.usageProbeEnabled
}

// setUsageProbeEnabled turns the background measure-once probe off (or back
// on); test-only, for the same reason as setReconnectBackoff.
func (s *Service) setUsageProbeEnabled(enabled bool) {
	s.mu.Lock()
	s.usageProbeEnabled = enabled
	s.mu.Unlock()
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
	if !ok || conn.session != session {
		s.mu.Unlock()
		return
	}
	// Capture the tool list before the conn is cleared below:
	// unregisterConnTools recomputes names from conn.tools, which is set to
	// nil on the very next lines — unregistering first would be a no-op.
	tools := conn.tools
	conn.session = nil
	conn.tools = nil
	conn.instructions = ""
	conn.status = Status{Status: "failed", Error: "connection closed"}
	s.mu.Unlock()
	// The dropped session's tools must stop being advertised the moment the
	// session dies, not only when a retry eventually reconnects — between
	// the drop and the reconnect the registry would otherwise still list
	// tools whose session is gone.
	s.unregisterConnTools(&connection{name: name, tools: tools})

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
	timeout := time.Duration(conn.config.TimeoutOr(int(defaultToolTimeout/time.Millisecond))) * time.Millisecond
	for _, def := range conn.tools {
		registry.Register(&mcpTool{clientName: conn.name, def: def, session: conn.session, timeout: timeout})
	}
}

// unregisterConnTools removes every tool a connection registered from the
// wired registry, the mirror of registerConnTools. Names are recomputed from
// the conn's own tools (ToolName), not remembered from registration, so the
// two paths can never disagree about what was registered.
func (s *Service) unregisterConnTools(conn *connection) {
	s.mu.RLock()
	registry := s.registry
	s.mu.RUnlock()
	if registry == nil {
		return
	}
	for _, def := range conn.tools {
		registry.Unregister(ToolName(conn.name, def.Name))
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

// Servers returns every configured server — connected, disabled or failed —
// sorted by name, with type, endpoint, enable state and live status. It is
// the listing surface the management dialog builds its rows from; Statuses
// remains the lighter status-only view the sidebar polls.
func (s *Service) Servers() []ServerInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServerInfo, 0, len(s.conns))
	for name, conn := range s.conns {
		info := ServerInfo{
			Name:      name,
			Type:      conn.config.Type,
			URL:       conn.config.URL,
			Command:   conn.config.Command,
			Enabled:   conn.config.IsEnabled(),
			Status:    conn.status,
			ToolCount: conn.toolCount,
			EstTokens: conn.estTokens,
			CostLive:  conn.status.Connected(),
			Config:    conn.config,
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toolTokens estimates the context tokens one server's tool
// definitions consume — every tool's name, description and input schema as
// the provider serializes them. The heuristic is chars/4, the same coarse
// estimate session.estimateTokens uses for compaction budgeting: an exact
// count would need the provider's own tokenizer, which the process never
// sees for tool definitions. Names are recomputed via ToolName so the
// estimate covers what is actually advertised, prefix included.
func toolTokens(name string, tools []*sdkmcp.Tool) int {
	total := 0
	for _, def := range tools {
		full := ToolName(name, def.Name)
		total += (len(full) + 3) / 4
		total += (len(def.Description) + 3) / 4
		if schema, ok := def.InputSchema.(map[string]any); ok {
			if encoded, err := json.Marshal(schema); err == nil {
				total += (len(encoded) + 3) / 4
			}
		}
	}
	return total
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
		timeout := time.Duration(conn.config.TimeoutOr(int(defaultToolTimeout/time.Millisecond))) * time.Millisecond
		for _, def := range conn.tools {
			registry.Register(&mcpTool{clientName: conn.name, def: def, session: conn.session, timeout: timeout})
		}
	}
}

// Authenticate mirrors authenticate() in index.ts: a full interactive
// reconnect (opens a browser, waits for the OAuth redirect, then
// reconnects and re-lists tools), used by `gocode mcp auth <name>`.
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
