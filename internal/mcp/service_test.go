package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anomalyco/opencode-go/internal/tool"
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

func TestServiceDisabledServerNeverConnects(t *testing.T) {
	svc := NewService(t.TempDir())
	defer svc.Close()
	status := svc.Connect(context.Background(), "s", ServerConfig{Type: "remote", URL: "http://127.0.0.1:1", Enabled: boolPtr(false)})
	if status.Status != "disabled" {
		t.Fatalf("status = %+v, want disabled", status)
	}
}

func boolPtr(b bool) *bool { return &b }
