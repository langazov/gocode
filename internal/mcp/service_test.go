package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/langazov/gocode-go/internal/tool"
)

// newTestMCPServer starts a real MCP server (via the SDK's own streamable
// HTTP handler) exposing one "echo" tool, for end-to-end testing of this
// port's connect/tool-registration path without needing OAuth or a real
// subprocess.
func newTestMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server", Version: "v0.0.1"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name:        "echo",
		Description: "echoes its input back",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string"}}}`),
	}, func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var args struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo: " + args.Message}}}, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(func(r *http.Request) *sdkmcp.Server { return server }, nil)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}

func TestServiceConnectRemoteAndRegisterTools(t *testing.T) {
	httpServer := newTestMCPServer(t)

	svc := NewService(t.TempDir())
	defer svc.Close()

	status := svc.Connect(context.Background(), "myserver", ServerConfig{Type: "remote", URL: httpServer.URL})
	if status.Status != "connected" {
		t.Fatalf("expected connected, got %+v", status)
	}

	registry := tool.NewRegistry()
	svc.RegisterTools(registry)

	name := ToolName("myserver", "echo")
	got, ok := registry.Get(name)
	if !ok {
		t.Fatalf("expected tool %q registered, registry has: %v", name, registry.Names())
	}
	if got.Description() != "echoes its input back" {
		t.Fatalf("description = %q", got.Description())
	}

	out, err := got.Execute(context.Background(), map[string]any{"message": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "echo: hi" {
		t.Fatalf("Execute() = %q, want %q", out, "echo: hi")
	}
}

func TestServiceStatusesAndServerNames(t *testing.T) {
	httpServer := newTestMCPServer(t)
	svc := NewService(t.TempDir())
	defer svc.Close()

	svc.Load(context.Background(), map[string]ServerConfig{
		"good":     {Type: "remote", URL: httpServer.URL},
		"disabled": {Type: "remote", URL: httpServer.URL, Enabled: boolPtr(false)},
		"bad":      {Type: "remote", URL: "http://127.0.0.1:1"}, // nothing listens here
	})

	statuses := svc.Statuses()
	if statuses["good"].Status != "connected" {
		t.Fatalf("good = %+v", statuses["good"])
	}
	if statuses["disabled"].Status != "disabled" {
		t.Fatalf("disabled = %+v", statuses["disabled"])
	}
	if statuses["bad"].Status != "failed" {
		t.Fatalf("bad = %+v, want failed", statuses["bad"])
	}

	names := svc.ServerNames()
	if len(names) != 1 || names[0] != "good" {
		t.Fatalf("ServerNames() = %v, want [good]", names)
	}
}

// Disable removes a live connection's tools from the wired registry and
// leaves the server listed as disabled; Enable reconnects and re-registers.
func TestServiceDisableUnregistersAndEnableReconnects(t *testing.T) {
	httpServer := newTestMCPServer(t)

	svc := NewService(t.TempDir())
	defer svc.Close()

	cfg := ServerConfig{Type: "remote", URL: httpServer.URL}
	if status := svc.Connect(context.Background(), "myserver", cfg); status.Status != "connected" {
		t.Fatalf("connect = %+v", status)
	}

	registry := tool.NewRegistry()
	svc.SetRegistry(registry)
	svc.RegisterTools(registry)
	name := ToolName("myserver", "echo")
	if _, ok := registry.Get(name); !ok {
		t.Fatal("tool was never registered")
	}

	// Disable: tools gone from the registry, status disabled, still listed.
	svc.Disable("myserver")
	if _, ok := registry.Get(name); ok {
		t.Error("tool stayed registered after Disable")
	}
	if status := svc.Statuses()["myserver"]; status.Status != "disabled" {
		t.Errorf("status after Disable = %+v", status)
	}
	servers := svc.Servers()
	if len(servers) != 1 || servers[0].Name != "myserver" {
		t.Fatalf("Servers() after Disable = %+v, want the one disabled entry", servers)
	}
	if servers[0].Enabled {
		t.Error("Servers() reports enabled after Disable")
	}
	// The measurement survives the disable: the dialog shows what enabling
	// it back would cost, flagged as not-live via CostLive.
	if servers[0].ToolCount != 1 || servers[0].EstTokens <= 0 {
		t.Errorf("last-known cost lost on Disable: %+v", servers[0])
	}
	if servers[0].CostLive {
		t.Error("CostLive = true after Disable, want false (last-known)")
	}

	// Enable: reconnected, tools back.
	status := svc.Enable(context.Background(), "myserver", cfg)
	if status.Status != "connected" {
		t.Fatalf("Enable = %+v", status)
	}
	if _, ok := registry.Get(name); !ok {
		t.Error("tool not re-registered after Enable")
	}
	if servers := svc.Servers(); !servers[0].Enabled {
		t.Error("Servers() reports disabled after Enable")
	}
	if servers := svc.Servers(); !servers[0].CostLive {
		t.Error("CostLive = false after Enable, want true")
	}
}

// Servers() lists disabled and failed servers alongside connected ones —
// the management dialog's whole row set, sorted by name.
func TestServiceServersIncludesDisabledAndFailed(t *testing.T) {
	httpServer := newTestMCPServer(t)
	svc := NewService(t.TempDir())
	defer svc.Close()

	svc.Load(context.Background(), map[string]ServerConfig{
		"zeta":    {Type: "remote", URL: httpServer.URL},
		"alpha":   {Type: "remote", URL: httpServer.URL, Enabled: boolPtr(false)},
		"failure": {Type: "remote", URL: "http://127.0.0.1:1"},
	})

	servers := svc.Servers()
	if len(servers) != 3 {
		t.Fatalf("Servers() = %+v, want 3 entries", servers)
	}
	wantOrder := []string{"alpha", "failure", "zeta"}
	for i, want := range wantOrder {
		if servers[i].Name != want {
			t.Errorf("Servers()[%d] = %q, want %q (sorted)", i, servers[i].Name, want)
		}
	}
	if servers[0].Status.Status != "disabled" {
		t.Errorf("alpha status = %+v, want disabled", servers[0].Status)
	}
	if servers[2].Type != "remote" || servers[2].URL == "" {
		t.Errorf("zeta config view = %+v, want type and url", servers[2])
	}
}

// A connected server's ServerInfo quantifies its context cost: tool count
// and a positive chars/4 token estimate over names, descriptions and
// schemas. A disabled server with no measurement anywhere reports zeros —
// the cost column's whole point — which is why the probe is off here; the
// probe's own behavior is covered by the usage tests below.
func TestServiceServersReportTokenEstimate(t *testing.T) {
	httpServer := newTestMCPServer(t)
	svc := NewService(t.TempDir())
	defer svc.Close()
	svc.setUsageProbeEnabled(false)

	svc.Load(context.Background(), map[string]ServerConfig{
		"live": {Type: "remote", URL: httpServer.URL},
		"off":  {Type: "remote", URL: httpServer.URL, Enabled: boolPtr(false)},
	})

	byName := map[string]ServerInfo{}
	for _, info := range svc.Servers() {
		byName[info.Name] = info
	}
	live := byName["live"]
	if live.ToolCount != 1 {
		t.Errorf("live ToolCount = %d, want 1", live.ToolCount)
	}
	if live.EstTokens <= 0 {
		t.Errorf("live EstTokens = %d, want a positive estimate", live.EstTokens)
	}
	if live.CostLive != true {
		t.Errorf("live CostLive = %v, want true", live.CostLive)
	}
	if off := byName["off"]; off.ToolCount != 0 || off.EstTokens != 0 || off.CostLive {
		t.Errorf("off = %d tools / %d tokens / live=%v, want zeros and not live (never measured, probe off)", off.ToolCount, off.EstTokens, off.CostLive)
	}
}

func TestServiceDisabledServerNeverConnects(t *testing.T) {
	svc := NewService(t.TempDir())
	defer svc.Close()
	status := svc.Connect(context.Background(), "s", ServerConfig{Type: "remote", URL: "http://127.0.0.1:1", Enabled: boolPtr(false)})
	if status.Status != "disabled" {
		t.Fatalf("status = %+v, want disabled", status)
	}
}

// connectTimeoutFor returns the deadline connect() would apply for the given
// mode and config — the logic from service.go's connect, mirrored here so a
// regression in the interactive widening (the authFetcher's browser wait
// running inside a deadline too small to hold it) can't slip through tests
// unnoticed.
func connectTimeoutFor(mode oauthMode, cfg ServerConfig) time.Duration {
	timeout := time.Duration(cfg.TimeoutOr(int(defaultTimeout/time.Millisecond))) * time.Millisecond
	if mode == modeInteractive {
		timeout += interactiveAuthTimeout + time.Minute
	}
	return timeout
}

func TestConnectTimeoutInteractiveAuthWidened(t *testing.T) {
	passive := connectTimeoutFor(modePassive, ServerConfig{})
	interactive := connectTimeoutFor(modeInteractive, ServerConfig{})
	if interactive != passive+interactiveAuthTimeout+time.Minute {
		t.Fatalf("interactive = %v, want passive(%v) + %v + 1m", interactive, passive, interactiveAuthTimeout)
	}
	// The widened budget must comfortably contain the authFetcher's browser
	// wait, or that wait is dead code killed by the connect deadline first —
	// the exact failure mode `mcp auth` hit before (30s connect deadline,
	// 5min browser wait).
	if interactive < interactiveAuthTimeout+defaultTimeout {
		t.Fatalf("interactive connect budget %v does not contain browser wait %v", interactive, interactiveAuthTimeout)
	}
	if interactiveAuthTimeout != 300*time.Second {
		t.Fatalf("interactiveAuthTimeout = %v, want 5 minutes", interactiveAuthTimeout)
	}
}

func TestConnectTimeoutExplicitConfigStillWidened(t *testing.T) {
	// A user-configured per-server timeout is the base for interactive auth
	// too — the widening is additive, never a ceiling.
	cfg := ServerConfig{Timeout: intPtr(10_000)}
	if got := connectTimeoutFor(modeInteractive, cfg); got != 10*time.Second+interactiveAuthTimeout+time.Minute {
		t.Fatalf("interactive with configured timeout = %v", got)
	}
}

func boolPtr(b bool) *bool { return &b }

func intPtr(i int) *int { return &i }
