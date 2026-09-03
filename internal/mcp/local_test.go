package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/langazov/gocode-go/internal/tool"
)

// This file tests the local (stdio) connect path against a *real*
// subprocess: the test binary re-execs itself with an env var that makes it
// act as a tiny MCP stdio server instead of running tests, mirroring how
// TS's tests would spawn a real local MCP server rather than mocking the
// transport.

const stdioServerEnv = "GOCODE_MCP_TEST_STDIO_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(stdioServerEnv) == "1" {
		runTestStdioServer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runTestStdioServer() {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-stdio-server", Version: "v0.0.1"}, nil)
	server.AddTool(&sdkmcp.Tool{
		Name:        "add",
		Description: "adds two numbers",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}`),
	}, func(_ context.Context, req *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
		var args struct{ A, B float64 }
		_ = json.Unmarshal(req.Params.Arguments, &args)
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: strconv.FormatInt(int64(args.A+args.B), 10)}}}, nil
	})
	_ = server.Run(context.Background(), &sdkmcp.StdioTransport{})
}

func TestServiceConnectLocalStdioSubprocess(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	svc := NewService(t.TempDir())
	defer svc.Close()

	status := svc.Connect(context.Background(), "local", ServerConfig{
		Type:        "local",
		Command:     []string{self},
		Environment: map[string]string{stdioServerEnv: "1"},
	})
	if status.Status != "connected" {
		t.Fatalf("expected connected, got %+v", status)
	}

	registry := tool.NewRegistry()
	svc.RegisterTools(registry)
	name := ToolName("local", "add")
	got, ok := registry.Get(name)
	if !ok {
		t.Fatalf("expected tool %q registered, got %v", name, registry.Names())
	}
	out, err := got.Execute(context.Background(), map[string]any{"a": 2, "b": 3})
	if err != nil {
		t.Fatal(err)
	}
	if out != "5" {
		t.Fatalf("Execute() = %q, want %q", out, "5")
	}
}
