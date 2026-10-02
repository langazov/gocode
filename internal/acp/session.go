package acp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/jsonrpc"
	"github.com/langazov/gocode-go/internal/mcp"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/tool"
)

// acpSession is the connection-side state of one session the client has
// opened (new, load or resume). The durable session lives in the database;
// this is what only matters while the client is attached.
type acpSession struct {
	id             string
	cwd            string
	additionalDirs []string
	runtime        *Runtime
	// mcpNames are the client-supplied MCP servers connected for this
	// session, under their registry names.
	mcpNames []string

	mu sync.Mutex
	// turn is the prompt currently running, nil when idle.
	turn *turn
	// autoApprove answers permission asks without the client (the boolean
	// auto_approve config option).
	autoApprove bool
	// title is the last title reported, so a rename is noticed.
	title string
	// agentID is the last mode reported, so an agent-initiated switch is
	// noticed.
	agentID string
	// tools tracks every tool call reported this connection, by call id.
	tools map[string]*toolTrack
	// state is the last v2 state_update sent.
	state string
	// stepModel is the provider and model of the step in progress, for the
	// context window a usage update reports against.
	stepModel [2]string
	// permissionQueue serialises this session's permission prompts, porting
	// the per-session queues in packages/opencode/src/acp/permission.ts.
	permissionQueue chan func()
}

func newACPSession(id, cwd string, additional []string, rt *Runtime) *acpSession {
	s := &acpSession{
		id:              id,
		cwd:             cwd,
		additionalDirs:  additional,
		runtime:         rt,
		tools:           map[string]*toolTrack{},
		permissionQueue: make(chan func(), 64),
	}
	// The worker holds its own reference: release clears the field under
	// the lock and closes the channel, which is what ends the loop.
	queue := s.permissionQueue
	go func() {
		for job := range queue {
			job()
		}
	}()
	return s
}

func (a *Agent) session(sessionID string) (*acpSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[sessionID]
	if s == nil {
		return nil, sessionNotFound(sessionID)
	}
	return s, nil
}

// lifecycleParams are the environment parameters new/load/resume share
// (protocol/v2/migration "Consistent lifecycle requests").
type lifecycleParams struct {
	SessionID             string            `json:"sessionId"`
	Cwd                   string            `json:"cwd"`
	AdditionalDirectories []string          `json:"additionalDirectories"`
	MCPServers            []json.RawMessage `json:"mcpServers"`
	ReplayFrom            *struct {
		Type string `json:"type"`
	} `json:"replayFrom"`
}

func (p lifecycleParams) validate() error {
	if p.Cwd == "" {
		return invalidParams("cwd is required")
	}
	if !filepath.IsAbs(p.Cwd) {
		return invalidParams("cwd must be an absolute path: %q", p.Cwd)
	}
	for _, dir := range p.AdditionalDirectories {
		if !filepath.IsAbs(dir) {
			return invalidParams("additionalDirectories entries must be absolute paths: %q", dir)
		}
	}
	return nil
}

func (a *Agent) newSession(ctx context.Context, params json.RawMessage) (any, error) {
	var in lifecycleParams
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	servers, err := a.parseMCPServers(in.MCPServers)
	if err != nil {
		return nil, err
	}
	rt, err := a.runtime(ctx, in.Cwd)
	if err != nil {
		return nil, err
	}
	created, err := rt.Sessions.Create(ctx, session.CreateInput{Directory: in.Cwd})
	if err != nil {
		return nil, internalError("creating session: %v", err)
	}
	s := a.open(created.ID, in.Cwd, in.AdditionalDirectories, rt)
	s.title = created.Title
	if err := a.applyRoots(ctx, s); err != nil {
		return nil, err
	}
	a.connectMCP(ctx, s, servers)

	result := obj{"sessionId": created.ID}
	a.setupState(ctx, s, result)
	return a.afterSetup(s, result), nil
}

// loadSession is v1 session/load: resume with a full history replay before
// the response (protocol/v1/session-setup "Loading Sessions").
func (a *Agent) loadSession(ctx context.Context, params json.RawMessage) (any, error) {
	return a.reattach(ctx, params, true)
}

// resumeSession is session/resume in both versions. v1 never replays; v2
// replays when replayFrom asks it to.
func (a *Agent) resumeSession(ctx context.Context, params json.RawMessage) (any, error) {
	return a.reattach(ctx, params, false)
}

func (a *Agent) reattach(ctx context.Context, params json.RawMessage, load bool) (any, error) {
	var in lifecycleParams
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	if in.SessionID == "" {
		return nil, invalidParams("sessionId is required")
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	replay := load
	if !load && in.ReplayFrom != nil && a.protocolVersion() == ProtocolV2 {
		switch in.ReplayFrom.Type {
		case "start":
			replay = true
		default:
			// A replay cursor this agent does not know cannot be honoured
			// partially: replaying from the start would duplicate what the
			// client holds, not replaying would lose what it asked for.
			return nil, invalidParams("unsupported replayFrom type %q", in.ReplayFrom.Type)
		}
	}
	servers, err := a.parseMCPServers(in.MCPServers)
	if err != nil {
		return nil, err
	}
	info, err := a.host.Store.Get(ctx, in.SessionID)
	if err != nil {
		return nil, internalError("loading session: %v", err)
	}
	if info == nil {
		return nil, sessionNotFound(in.SessionID)
	}
	if filepath.Clean(info.Directory) != filepath.Clean(in.Cwd) {
		// The session's tools, config and history belong to its own
		// directory; resuming it elsewhere would run it against the wrong
		// project (additional roots may change, the cwd may not).
		return nil, invalidParams("session %s belongs to %s, not %s", in.SessionID, info.Directory, in.Cwd)
	}
	rt, err := a.runtime(ctx, info.Directory)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	existing := a.sessions[in.SessionID]
	a.mu.Unlock()
	var s *acpSession
	if existing != nil {
		s = existing
		s.mu.Lock()
		s.additionalDirs = in.AdditionalDirectories
		s.mu.Unlock()
	} else {
		s = a.open(in.SessionID, info.Directory, in.AdditionalDirectories, rt)
	}
	s.mu.Lock()
	s.title = info.Title
	s.agentID = info.Agent
	s.mu.Unlock()
	if err := a.applyRoots(ctx, s); err != nil {
		return nil, err
	}
	a.connectMCP(ctx, s, servers)

	if replay {
		if err := a.replay(ctx, s); err != nil {
			return nil, internalError("replaying session: %v", err)
		}
	}
	result := obj{}
	a.setupState(ctx, s, result)
	// The client already knows this session's id, so the v1 command list
	// goes out before the response: a load's replay is complete once it
	// responds, and nothing more may follow it for the session
	// (protocol/v1/session-setup "Loading Sessions").
	if a.protocolVersion() == ProtocolV1 {
		a.update(s.id, obj{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": a.availableCommands(s),
		})
	}
	return result, nil
}

// open registers a session with the connection.
func (a *Agent) open(sessionID, cwd string, additional []string, rt *Runtime) *acpSession {
	s := newACPSession(sessionID, cwd, additional, rt)
	if info, err := rt.Sessions.Get(context.Background(), sessionID); err == nil && info != nil {
		s.agentID = info.Agent
	}
	a.mu.Lock()
	a.sessions[sessionID] = s
	a.mu.Unlock()
	return s
}

// setupState fills the optional initial-state fields of a setup response:
// config options in both versions, modes in v1, commands in v2 (which may
// carry them in the response rather than only in a notification).
func (a *Agent) setupState(ctx context.Context, s *acpSession, result obj) {
	version := a.protocolVersion()
	result["configOptions"] = a.configOptions(ctx, s)
	if version == ProtocolV1 {
		if modes := a.modeState(ctx, s); modes != nil {
			result["modes"] = modes
		}
		return
	}
	if commands := a.availableCommands(s); len(commands) > 0 {
		result["availableCommands"] = commands
	}
}

// afterSetup wraps a setup response with what must follow it. v1 learns the
// command list only through available_commands_update (v2 already got it in
// the response); the notification is sent once the response is written, so
// it never arrives for a session id the client has not been told about.
func (a *Agent) afterSetup(s *acpSession, result obj) any {
	if a.protocolVersion() != ProtocolV1 {
		return result
	}
	return jsonrpc.Then{Result: result, After: func() {
		a.update(s.id, obj{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": a.availableCommands(s),
		})
	}}
}

// applyRoots installs the session's tool environment: its additional
// workspace roots, which widen the file tools' sandbox so the effective root
// set is [cwd, ...additionalDirectories] (protocol/v1/session-setup "Working
// Directory"), and — for a v1 client that offered them — the client's file
// system and terminal. Each call replaces the previous environment: the
// client sends the full root list on every load/resume, and an omitted list
// restores nothing.
func (a *Agent) applyRoots(_ context.Context, s *acpSession) error {
	s.mu.Lock()
	dirs := make([]string, 0, len(s.additionalDirs))
	for _, dir := range s.additionalDirs {
		dirs = append(dirs, filepath.Clean(dir))
	}
	s.mu.Unlock()
	env := &tool.SessionEnv{Roots: dirs}
	caps := a.clientCapabilities()
	if a.protocolVersion() == ProtocolV1 {
		if caps.readTextFile || caps.writeTextFile {
			env.FS = &clientFS{agent: a, sessionID: s.id, read: caps.readTextFile, write: caps.writeTextFile}
		}
		if caps.terminal {
			env.Terminal = &clientTerminal{agent: a, session: s}
		}
	}
	if len(env.Roots) == 0 && env.FS == nil && env.Terminal == nil {
		tool.SetSessionEnv(s.id, nil)
		return nil
	}
	tool.SetSessionEnv(s.id, env)
	return nil
}

func (a *Agent) listSessions(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		Cwd    string `json:"cwd"`
		Cursor string `json:"cursor"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	if in.Cwd != "" && !filepath.IsAbs(in.Cwd) {
		return nil, invalidParams("cwd must be an absolute path: %q", in.Cwd)
	}
	offset := 0
	if in.Cursor != "" {
		var err error
		if offset, err = decodeCursor(in.Cursor); err != nil {
			return nil, invalidParams("invalid cursor")
		}
	}
	all, err := a.host.Store.List(ctx)
	if err != nil {
		return nil, internalError("listing sessions: %v", err)
	}
	// Subagent sessions are internal to the turn that spawned them; the
	// list is the user's conversations. Most recently active first.
	var matching []session.Info
	for _, info := range all {
		if info.ParentID != "" {
			continue
		}
		if in.Cwd != "" && filepath.Clean(info.Directory) != filepath.Clean(in.Cwd) {
			continue
		}
		matching = append(matching, info)
	}
	sort.SliceStable(matching, func(i, j int) bool { return matching[i].TimeUpdated > matching[j].TimeUpdated })

	if offset > len(matching) {
		return nil, invalidParams("invalid cursor")
	}
	end := offset + sessionPageSize
	if end > len(matching) {
		end = len(matching)
	}
	sessions := make([]obj, 0, end-offset)
	for _, info := range matching[offset:end] {
		entry := obj{
			"sessionId": info.ID,
			"cwd":       info.Directory,
			"updatedAt": time.UnixMilli(info.TimeUpdated).UTC().Format(time.RFC3339),
		}
		if info.Title != "" {
			entry["title"] = info.Title
		}
		a.mu.Lock()
		if live := a.sessions[info.ID]; live != nil && len(live.additionalDirs) > 0 {
			entry["additionalDirectories"] = live.additionalDirs
		}
		a.mu.Unlock()
		sessions = append(sessions, entry)
	}
	result := obj{"sessions": sessions}
	if end < len(matching) {
		result["nextCursor"] = encodeCursor(end)
	}
	return result, nil
}

// sessionPageSize bounds one session/list page ("Agents SHOULD enforce
// reasonable page sizes internally").
const sessionPageSize = 50

// Cursors are opaque to the client; this one is a versioned offset.
func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("v1:" + strconv.Itoa(offset)))
}

func decodeCursor(cursor string) (int, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	value, ok := strings.CutPrefix(string(raw), "v1:")
	if !ok {
		return 0, errors.New("bad cursor version")
	}
	offset, err := strconv.Atoi(value)
	if err != nil || offset < 0 {
		return 0, errors.New("bad cursor offset")
	}
	return offset, nil
}

func (a *Agent) closeSession(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	s, err := a.session(in.SessionID)
	if err != nil {
		return nil, err
	}
	// Close cancels as session/cancel would, then frees the session
	// (protocol/v1/session-setup "Closing a Session").
	a.cancel(in.SessionID)
	return jsonrpc.Deferred(func() (any, error) {
		s.waitIdle(ctx)
		a.release(s)
		return obj{}, nil
	}), nil
}

// release forgets a session and disconnects the MCP servers it brought.
func (a *Agent) release(s *acpSession) {
	a.mu.Lock()
	if a.sessions[s.id] == s {
		delete(a.sessions, s.id)
	}
	a.mu.Unlock()
	for _, name := range s.mcpNames {
		s.runtime.MCP.Disconnect(name)
	}
	tool.SetSessionEnv(s.id, nil)
	s.mu.Lock()
	queue := s.permissionQueue
	s.permissionQueue = nil
	s.mu.Unlock()
	if queue != nil {
		close(queue)
	}
}

func (a *Agent) deleteSession(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	if in.SessionID == "" {
		return nil, invalidParams("sessionId is required")
	}
	live, _ := a.session(in.SessionID)
	if live != nil {
		a.cancel(in.SessionID)
	}
	return jsonrpc.Deferred(func() (any, error) {
		if live != nil {
			live.waitIdle(ctx)
			a.release(live)
		}
		// Deleting a session that does not exist succeeds silently
		// (protocol/v1/session-delete "Semantics").
		if err := a.host.Store.Delete(ctx, in.SessionID); err != nil && !errors.Is(err, session.ErrSessionNotFound) {
			return nil, internalError("deleting session: %v", err)
		}
		return obj{}, nil
	}), nil
}

// forkSession is the _gocode/session/fork extension: copy a session (up to a
// message, optionally) into a new one and open it on this connection.
func (a *Agent) forkSession(ctx context.Context, params json.RawMessage) (any, error) {
	var in struct {
		SessionID string `json:"sessionId"`
		MessageID string `json:"messageId"`
	}
	if err := decode(params, &in); err != nil {
		return nil, err
	}
	info, err := a.host.Store.Get(ctx, in.SessionID)
	if err != nil || info == nil {
		return nil, sessionNotFound(in.SessionID)
	}
	rt, err := a.runtime(ctx, info.Directory)
	if err != nil {
		return nil, err
	}
	forked, err := rt.Sessions.Fork(ctx, in.SessionID, in.MessageID)
	if err != nil {
		return nil, internalError("forking session: %v", err)
	}
	s := a.open(forked.ID, forked.Directory, nil, rt)
	s.title = forked.Title
	result := obj{"sessionId": forked.ID}
	a.setupState(ctx, s, result)
	return a.afterSetup(s, result), nil
}

// parseMCPServers validates the client's MCP server list against the
// negotiated version: v1 stdio configs carry no type, v2 requires one, and
// SSE only exists in v1 (protocol/v2/migration "MCP server configuration").
func (a *Agent) parseMCPServers(raw []json.RawMessage) ([]namedServer, error) {
	version := a.protocolVersion()
	var out []namedServer
	for _, entry := range raw {
		var server struct {
			Type    string   `json:"type"`
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
			Env     []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"env"`
			URL     string `json:"url"`
			Headers []struct {
				Name  string `json:"name"`
				Value string `json:"value"`
			} `json:"headers"`
		}
		if err := json.Unmarshal(entry, &server); err != nil {
			return nil, invalidParams("invalid MCP server: %v", err)
		}
		if server.Name == "" {
			return nil, invalidParams("MCP server name is required")
		}
		kind := server.Type
		if kind == "" {
			if version == ProtocolV2 {
				return nil, invalidParams("MCP server %q: type is required", server.Name)
			}
			kind = "stdio"
		}
		cfg := mcp.ServerConfig{}
		switch kind {
		case "stdio":
			if server.Command == "" {
				return nil, invalidParams("MCP server %q: command is required", server.Name)
			}
			cfg.Type = "local"
			cfg.Command = append([]string{server.Command}, server.Args...)
			if len(server.Env) > 0 {
				cfg.Environment = map[string]string{}
				for _, env := range server.Env {
					cfg.Environment[env.Name] = env.Value
				}
			}
		case "http", "sse":
			if kind == "sse" && version == ProtocolV2 {
				return nil, invalidParams("MCP server %q: the sse transport was removed in protocol v2", server.Name)
			}
			if server.URL == "" {
				return nil, invalidParams("MCP server %q: url is required", server.Name)
			}
			cfg.Type = "remote"
			cfg.URL = server.URL
			if len(server.Headers) > 0 {
				cfg.Headers = map[string]string{}
				for _, header := range server.Headers {
					cfg.Headers[header.Name] = header.Value
				}
			}
		default:
			// Custom transports are extension variants; ones we do not know
			// are skipped rather than failing the session.
			a.host.Log("acp: skipping MCP server %q with unsupported transport %q", server.Name, kind)
			continue
		}
		out = append(out, namedServer{name: server.Name, config: cfg})
	}
	return out, nil
}

type namedServer struct {
	name   string
	config mcp.ServerConfig
}

// connectMCP connects the client's MCP servers for a session. Failures are
// logged, not returned: the agent SHOULD connect to every server, but one
// unreachable server must not cost the user the session.
func (a *Agent) connectMCP(ctx context.Context, s *acpSession, servers []namedServer) {
	for _, server := range servers {
		name := "acp-" + server.name
		status := s.runtime.MCP.Connect(context.WithoutCancel(ctx), name, server.config)
		switch {
		case status.Status == "needs_auth":
			// An OAuth-protected server: authorize it through the client's
			// browser when it offers URL elicitation, without holding up
			// the session setup response.
			go a.authorizeMCP(context.WithoutCancel(ctx), s, name, server.config)
		case status.Error != "":
			a.host.Log("acp: MCP server %s: %s", server.name, status.Error)
		}
		s.mu.Lock()
		s.mcpNames = append(s.mcpNames, name)
		s.mu.Unlock()
	}
}

// waitIdle blocks until the session's current turn, if any, has ended.
func (s *acpSession) waitIdle(ctx context.Context) {
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t == nil {
		return
	}
	select {
	case <-t.done:
	case <-ctx.Done():
	}
}

// sessionFor finds the connection session an event belongs to: its own, or
// — for a subagent's session — the root session that spawned it, whose client
// is the one that has to answer the subagent's permission asks.
func (a *Agent) sessionFor(rt *Runtime, sessionID string) (*acpSession, bool) {
	a.mu.Lock()
	s := a.sessions[sessionID]
	a.mu.Unlock()
	if s != nil {
		return s, false
	}
	parents, err := rt.Sessions.Parents(context.Background(), sessionID)
	if err != nil {
		return nil, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, parent := range parents {
		if s := a.sessions[parent]; s != nil {
			return s, true
		}
	}
	return nil, false
}

func (s *acpSession) String() string { return fmt.Sprintf("session %s (%s)", s.id, s.cwd) }
