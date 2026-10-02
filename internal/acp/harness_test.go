package acp

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/command"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/jsonrpc"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/mcp"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/tool"
	"github.com/langazov/gocode-go/internal/tool/builtins"
)

// scriptedProvider plays one scripted model turn per request. A turn whose
// events are nil blocks until the request is cancelled, which is how the
// cancellation tests catch a turn mid-flight.
type scriptedProvider struct {
	mu       sync.Mutex
	turns    [][]llm.StreamEvent
	requests []llm.Request
	started  chan struct{}
}

func (p *scriptedProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	index := len(p.requests) - 1
	var turn []llm.StreamEvent
	if index < len(p.turns) {
		turn = p.turns[index]
	}
	started := p.started
	p.mu.Unlock()
	if turn == nil {
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}
	for _, streamEvent := range turn {
		emit(streamEvent)
	}
	return nil
}

func textTurn(text string) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.EventTextDelta, Text: text},
		{Type: llm.EventFinish, Finish: "stop", Usage: llm.Usage{Input: 100, Output: 20}},
	}
}

func toolTurn(callID, name string, input map[string]any) []llm.StreamEvent {
	return []llm.StreamEvent{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: callID, Name: name, Input: input}},
		{Type: llm.EventFinish, Finish: "tool-calls", Usage: llm.Usage{Input: 50, Output: 10}},
	}
}

// fixture is one agent under test with its runtime and a connected client.
type fixture struct {
	t        *testing.T
	dir      string
	database *db.DB
	provider *scriptedProvider
	runtime  *Runtime
	agent    *Agent
	client   *testClient
}

type fixtureOptions struct {
	rules permission.Ruleset
	// clientCaps is sent in initialize under the name the version uses.
	clientCaps map[string]any
}

func newFixture(t *testing.T, version int, provider *scriptedProvider, opts fixtureOptions) *fixture {
	t.Helper()
	dir := t.TempDir()
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	f := &fixture{t: t, dir: dir, database: database, provider: provider}
	f.runtime = buildRuntime(t, dir, database, provider, opts.rules)

	agentR, agentW := io.Pipe()
	clientR, clientW := io.Pipe()
	f.agent = New(Host{
		Boot: func(ctx context.Context, directory string) (*Runtime, error) {
			return f.runtime, nil
		},
		Store:            session.NewService(database, nil),
		DefaultDirectory: dir,
		Version:          "test",
		Log:              t.Logf,
	}, agentR, clientW)
	go func() {
		f.agent.Conn().Listen()
		f.agent.shutdown()
	}()
	f.client = newTestClient(t, clientR, agentW)
	t.Cleanup(func() {
		agentW.Close()
		f.client.conn.Shutdown(io.EOF)
	})

	params := map[string]any{"protocolVersion": version}
	caps := opts.clientCaps
	if caps == nil {
		caps = map[string]any{}
	}
	if version == ProtocolV1 {
		params["clientCapabilities"] = caps
		params["clientInfo"] = map[string]any{"name": "test-client", "version": "1"}
	} else {
		params["capabilities"] = caps
		params["info"] = map[string]any{"name": "test-client", "version": "1"}
	}
	var result map[string]any
	f.client.call("initialize", params, &result)
	if got := result["protocolVersion"]; got != float64(version) {
		t.Fatalf("negotiated version %v, want %d", got, version)
	}
	return f
}

func buildRuntime(t *testing.T, dir string, database *db.DB, provider llm.StreamClient, rules permission.Ruleset) *Runtime {
	t.Helper()
	bus := event.NewBus(database)
	session.RegisterProjectors(bus)
	session.RegisterRunnerProjectors(bus)

	questions := question.NewService(question.Hooks{
		OnAsked: func(request question.Request) {
			bus.Publish(context.Background(), session.QuestionAsked, map[string]any{"sessionID": request.SessionID, "requestID": request.ID}, event.PublishOptions{})
		},
	}, nil)
	tools := tool.NewRegistry()
	builtins.RegisterWith(tools, dir, builtins.Options{Database: database, Asker: questions})

	agents := agent.NewRegistry()
	buildRules := permission.Merge(permission.Defaults(), permission.Ruleset{
		{Action: "question", Resource: "*", Effect: permission.Allow},
	}, rules)
	agents.Update(agent.Info{ID: "build", Mode: "primary", Description: "Implements changes", Permissions: buildRules})
	agents.Update(agent.Info{ID: "plan", Mode: "primary", Description: "Plans without editing", Permissions: buildRules})
	agents.Update(agent.Info{ID: "explore", Mode: "subagent", Permissions: buildRules})
	agents.SetDefault("build")

	agentRules := &session.AgentRulesProvider{Agents: agents}
	engine := permission.NewEngine(agentRules, nil, permission.Hooks{
		OnAsked: func(request permission.Request) {
			bus.Publish(context.Background(), session.PermissionAsked, map[string]any{"sessionID": request.SessionID, "requestID": request.ID}, event.PublishOptions{})
		},
	}, nil)
	model := session.ModelRef{ProviderID: "faketest", ID: "fake-model"}
	runner := &session.Runner{
		DB:          database,
		Bus:         bus,
		Messages:    session.NewMessageStore(database),
		Provider:    provider,
		Tools:       tools,
		Agents:      agents,
		Agent:       "build",
		Model:       model,
		Permissions: &session.EnginePermissionGate{Engine: engine},
	}
	execution := session.NewExecution(&session.DBSessionLookup{DB: database}, runner)
	execution.OnStatus = session.PublishRunStatus(context.Background(), bus)
	service := session.NewService(database, bus)
	service.Execution = execution
	service.DefaultModel = model
	agentRules.Sessions = service

	return &Runtime{
		Directory:   dir,
		Sessions:    service,
		Bus:         bus,
		Runner:      runner,
		Permissions: engine,
		Questions:   questions,
		Agents:      agents,
		Commands:    command.Load(&config.Config{}, dir, nil, nil),
		MCP:         mcp.NewService(dir),
		Models: func(context.Context) []Model {
			return []Model{
				{ProviderID: "faketest", ProviderName: "Fake", ID: "fake-model", Name: "Fake Model", ContextLimit: 128000, Variants: []string{"low", "high"}},
				{ProviderID: "faketest", ProviderName: "Fake", ID: "other-model", Name: "Other Model", ContextLimit: 64000},
			}
		},
	}
}

// testClient is the editor side of the connection: it records every
// session/update and answers the agent's requests with scripted handlers.
type testClient struct {
	t    *testing.T
	conn *jsonrpc.Conn

	mu      sync.Mutex
	updates []map[string]any
	changed chan struct{}
	// permission answers session/request_permission; nil selects the first
	// option.
	permission func(params map[string]any) map[string]any
	requests   []recordedRequest
}

type recordedRequest struct {
	method string
	params map[string]any
}

func newTestClient(t *testing.T, r io.Reader, w io.WriteCloser) *testClient {
	c := &testClient{t: t, conn: jsonrpc.NewLineConn(w, r), changed: make(chan struct{}, 1)}
	c.conn.SetCancelRequest("$/cancel_request", "requestId")
	// Updates are recorded in arrival order, as a real client applies them;
	// per-message goroutines would reorder them and the ordering assertions
	// would test the harness instead of the agent.
	c.conn.SetOrderedDispatch(true)
	c.conn.OnNotify("session/update", func(params json.RawMessage) {
		var decoded map[string]any
		json.Unmarshal(params, &decoded)
		update, _ := decoded["update"].(map[string]any)
		c.mu.Lock()
		c.updates = append(c.updates, update)
		c.mu.Unlock()
		c.signal()
	})
	c.conn.OnNotify("elicitation/complete", func(json.RawMessage) {})
	c.conn.Handle("session/request_permission", func(params json.RawMessage) (any, error) {
		var decoded map[string]any
		json.Unmarshal(params, &decoded)
		c.record("session/request_permission", decoded)
		c.mu.Lock()
		answer := c.permission
		c.mu.Unlock()
		if answer != nil {
			return answer(decoded), nil
		}
		options, _ := decoded["options"].([]any)
		first, _ := options[0].(map[string]any)
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": first["optionId"]}}, nil
	})
	go c.conn.Listen()
	return c
}

func (c *testClient) record(method string, params map[string]any) {
	c.mu.Lock()
	c.requests = append(c.requests, recordedRequest{method: method, params: params})
	c.mu.Unlock()
	c.signal()
}

func (c *testClient) signal() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// handle registers a handler for an agent→client request, recording calls.
func (c *testClient) handle(method string, fn func(params map[string]any) (any, error)) {
	c.conn.Handle(method, func(params json.RawMessage) (any, error) {
		var decoded map[string]any
		json.Unmarshal(params, &decoded)
		c.record(method, decoded)
		return fn(decoded)
	})
}

// call sends a request, failing the test on an error response.
func (c *testClient) call(method string, params any, out any) {
	c.t.Helper()
	if err := c.try(method, params, out); err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
}

func (c *testClient) try(method string, params any, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.conn.Call(ctx, method, params, out)
}

// waitFor blocks until an update satisfying match has arrived, returning it.
func (c *testClient) waitFor(what string, match func(map[string]any) bool) map[string]any {
	c.t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		c.mu.Lock()
		for _, update := range c.updates {
			if match(update) {
				c.mu.Unlock()
				return update
			}
		}
		c.mu.Unlock()
		select {
		case <-c.changed:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			c.t.Fatalf("timed out waiting for %s; updates: %s", what, c.dump())
		}
	}
}

func (c *testClient) waitRequest(method string) map[string]any {
	c.t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		c.mu.Lock()
		for _, request := range c.requests {
			if request.method == method {
				c.mu.Unlock()
				return request.params
			}
		}
		c.mu.Unlock()
		select {
		case <-c.changed:
		case <-time.After(50 * time.Millisecond):
		case <-deadline:
			c.t.Fatalf("timed out waiting for a %s request", method)
		}
	}
}

func (c *testClient) snapshot() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]map[string]any(nil), c.updates...)
}

func (c *testClient) reset() {
	c.mu.Lock()
	c.updates = nil
	c.requests = nil
	c.mu.Unlock()
}

func (c *testClient) dump() string {
	encoded, _ := json.MarshalIndent(c.snapshot(), "", "  ")
	return string(encoded)
}

// kinds lists the sessionUpdate discriminators received, in order.
func kinds(updates []map[string]any) []string {
	out := make([]string, 0, len(updates))
	for _, update := range updates {
		kind, _ := update["sessionUpdate"].(string)
		out = append(out, kind)
	}
	return out
}

func isUpdate(kind string) func(map[string]any) bool {
	return func(update map[string]any) bool { return update["sessionUpdate"] == kind }
}

// newSession opens a session in the fixture's directory.
func (f *fixture) newSession() string {
	f.t.Helper()
	var result map[string]any
	f.client.call("session/new", map[string]any{"cwd": f.dir, "mcpServers": []any{}}, &result)
	id, _ := result["sessionId"].(string)
	if id == "" {
		f.t.Fatalf("session/new returned no sessionId: %v", result)
	}
	return id
}

func textPrompt(sessionID, text string) map[string]any {
	return map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": text}}}
}
