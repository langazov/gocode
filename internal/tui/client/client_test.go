package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/mcp"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/server"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/tool"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestRoundTripAgainstServer exercises the TUI client against the real
// server stack, proving the wire contract end to end.
func TestRoundTripAgainstServer(t *testing.T) {
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	bus := event.NewBus(database)
	session.RegisterProjectors(bus)
	session.RegisterRunnerProjectors(bus)

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "hello from the model"},
		{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 3, Output: 5}},
	}}}
	runner := &session.Runner{
		DB:       database,
		Bus:      bus,
		Messages: session.NewMessageStore(database),
		Provider: provider,
		Tools:    tool.NewRegistry(),
		Agent:    "build",
		System:   "You are gocode.",
		Model:    session.ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
	}
	execution := session.NewExecution(&session.DBSessionLookup{DB: database}, runner)
	service := session.NewService(database, bus)
	service.Execution = execution
	engine := permission.NewEngine(
		permission.StaticRules{Rules: permission.Ruleset{{Action: "bash", Resource: "*", Effect: permission.Ask}}},
		nil, permission.Hooks{}, nil)

	api := httptest.NewServer((&server.Server{Session: service, Bus: bus, Permissions: engine}).Mux())
	defer api.Close()

	c := New(api.URL)
	ctx := context.Background()

	created, err := c.CreateSession(ctx, CreateInput{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("expected session id")
	}

	sessions, err := c.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("expected created session listed, got %v", sessions)
	}

	// Subscribe before prompting: the stream is live-only, no replay.
	eventCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	events, err := c.Events(eventCtx, created.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Prompt(ctx, created.ID, "say hi"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var messages []Message
	var data AssistantData
	for time.Now().Before(deadline) {
		messages, err = c.Messages(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) >= 2 {
			candidate, decodeErr := DecodeAssistant(messages[len(messages)-1].Data)
			if decodeErr == nil {
				for _, part := range candidate.Content {
					if part.Type == "text" && part.Text != "" {
						data = candidate
					}
				}
			}
		}
		if data.Finish != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(messages) < 2 {
		t.Fatalf("expected user+assistant messages, got %d", len(messages))
	}
	if len(data.Content) == 0 || data.Content[0].Text != "hello from the model" {
		t.Fatalf("unexpected assistant content: %+v", data.Content)
	}
	if data.Finish != "end_turn" {
		t.Fatalf("unexpected finish: %q", data.Finish)
	}

	// The stream must carry the committed events.
	seen := map[string]bool{}
	for event := range events {
		seen[event.Type] = true
		if event.Type == "session.next.step.ended" {
			break
		}
	}
	if !seen["session.next.step.started"] {
		t.Fatalf("expected step events on the stream, saw %v", seen)
	}
}

type scriptedProvider struct{ turns [][]llm.StreamEvent }

func (p *scriptedProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	turn := p.turns[0]
	if len(p.turns) > 1 {
		p.turns = p.turns[1:]
	}
	for _, streamEvent := range turn {
		emit(streamEvent)
	}
	return nil
}

// TestVcsRoundTrip proves the diff viewer's two endpoints through the real
// server: the info shape the "Main branch" source gates on, and the per-file
// diff list the viewer renders.
func TestVcsRoundTrip(t *testing.T) {
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	bus := event.NewBus(database)
	api := httptest.NewServer((&server.Server{Bus: bus, VCSWorkdir: t.TempDir()}).Mux())
	defer api.Close()

	c := New(api.URL)
	ctx := context.Background()

	info, err := c.Vcs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// t.TempDir() is not a repository: the payload is an empty object, and
	// that is a successful answer the viewer renders as "working tree only".
	if info.Branch != "" || info.DefaultBranch != "" {
		t.Fatalf("info outside a repo = %+v", info)
	}

	files, err := c.VcsDiff(ctx, "git", 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("diff outside a repo = %+v", files)
	}

	// The query the viewer actually sends round-trips without error.
	if _, err := c.VcsDiff(ctx, "branch", 12); err != nil {
		t.Fatalf("branch mode: %v", err)
	}
}

// TestMCPServersInfoRoundTrip proves the management list through the real
// server: mcp.ServerInfo nests its Status object on the wire, so the client
// must flatten it — a decode mismatch here once left the /mcp dialog
// permanently on its empty state while the endpoint itself worked.
func TestMCPServersInfoRoundTrip(t *testing.T) {
	// The service reads/writes the context-cost measurement store in the
	// data dir on disabled/live connects; keep the test off the
	// developer's real store.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	mcpService := mcp.NewService(t.TempDir())
	defer mcpService.Close()
	// A disabled entry needs no upstream: the shape under test is the
	// listing, and disabled servers are exactly the ones only this richer
	// view surfaces.
	mcpService.Load(context.Background(), map[string]mcp.ServerConfig{
		"off": {Type: "remote", URL: "https://mcp.example.invalid", Enabled: boolPtrFalse()},
		// A connected server is what carries the token-cost fields; the
		// upstream needs to actually expose one tool.
		"live": {Type: "remote", URL: upstreamMCP(t).URL},
	})

	api := httptest.NewServer((&server.Server{MCP: mcpService, VCSWorkdir: t.TempDir()}).Mux())
	defer api.Close()

	c := New(api.URL)
	servers, err := c.MCPServersInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %+v, want one entry", servers)
	}
	var got, live MCPServerInfo
	for _, server := range servers {
		switch server.Name {
		case "off":
			got = server
		case "live":
			live = server
		}
	}
	if got.Name == "" || live.Name == "" {
		t.Fatalf("servers = %+v, want an off and a live entry", servers)
	}
	if got.Name != "off" || got.Type != "remote" || got.URL != "https://mcp.example.invalid" {
		t.Errorf("entry = %+v", got)
	}
	if got.Enabled {
		t.Error("Enabled = true, want false")
	}
	if got.Status != "disabled" {
		t.Errorf("Status = %q, want disabled — the nested wire status was not flattened", got.Status)
	}
	if got.ToolCount != 0 || got.EstTokens != 0 {
		t.Errorf("disabled server reports cost %d tools / %d tokens, want zeros", got.ToolCount, got.EstTokens)
	}
	if live.Name != "live" || live.ToolCount != 1 {
		t.Errorf("live entry = %+v, want one tool", live)
	}
	if live.EstTokens <= 0 {
		t.Errorf("live EstTokens = %d, want a positive estimate", live.EstTokens)
	}
}

func boolPtrFalse() *bool {
	value := false
	return &value
}

// upstreamMCP starts a one-tool streamable MCP server, the same shape
// internal/mcp's tests use.
func upstreamMCP(t *testing.T) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "upstream", Version: "v0"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name:        "echo",
		Description: "echoes its input back",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
	}, func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo"}}}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}
