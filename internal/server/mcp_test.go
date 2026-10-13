package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/langazov/gocode-go/internal/mcp"
)

// newMCPTestServer builds a Server with a live MCP service connected to a
// real test MCP server, an isolated global config carrying one enabled and
// one disabled entry, and VCSWorkdir pointed at a temp project dir.
func newMCPTestServer(t *testing.T) (*Server, string, string) {
	t.Helper()
	mcpUpstream := startTestUpstream(t)

	globalRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalRoot)
	// The MCP service persists context-cost measurements to the data dir
	// (mcp-usage.json) on every successful connect; keep those writes out
	// of the developer's real store.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	globalConfig := filepath.Join(globalRoot, "gocode", "gocode.json")
	if err := os.MkdirAll(filepath.Dir(globalConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	configJSON := `{
  "mcp": {
    "live": {"type": "remote", "url": "` + mcpUpstream.URL + `"},
    "off":  {"type": "remote", "url": "` + mcpUpstream.URL + `", "enabled": false}
  }
}`
	if err := os.WriteFile(globalConfig, []byte(configJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	servers, errs := mcp.ParseServers(parseRawMCP(t, configJSON))
	if len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	service := mcp.NewService(t.TempDir())
	t.Cleanup(service.Close)
	service.Load(context.Background(), servers)

	s := &Server{MCP: service, VCSWorkdir: t.TempDir()}
	return s, mcpUpstream.URL, globalConfig
}

func startTestUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	// The same shape internal/mcp's tests use: a streamable HTTP MCP server
	// with one echo tool, built inline because that package's helper is a
	// test-file symbol not exported here.
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-upstream", Version: "v0.0.1"}, nil)
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

func parseRawMCP(t *testing.T, configJSON string) map[string]any {
	t.Helper()
	root := map[string]any{}
	if err := json.Unmarshal([]byte(configJSON), &root); err != nil {
		t.Fatal(err)
	}
	raw, _ := root["mcp"].(map[string]any)
	return raw
}

// GET /api/mcp/servers lists connected, disabled and failed servers with
// their enable state and endpoint.
func TestListMCPServersIncludesDisabled(t *testing.T) {
	s, upstream, _ := newMCPTestServer(t)
	rec := httptest.NewRecorder()
	s.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"name":"live"`, `"enabled":true`, `"name":"off"`, `"enabled":false`, upstream} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %s:\n%s", want, body)
		}
	}
}

// A server disabled in a previous run — one that boots disabled carrying a
// measurement restored from the usage store — reports its figures through
// GET /api/mcp/servers, which is what the /mcp dialog's cost column reads:
// the row shows what enabling it would cost back instead of "—" forever.
func TestListMCPServersReportsDisabledServerUsage(t *testing.T) {
	mcpUpstream := startTestUpstream(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg := mcp.ServerConfig{Type: "remote", URL: mcpUpstream.URL, Enabled: &disabled}
	// What a previous run persisted when the server was last connected.
	if err := mcp.UsageSet("off", mcp.UsageEntry{Type: cfg.Type, URL: cfg.URL, ToolCount: 2, EstTokens: 250}); err != nil {
		t.Fatal(err)
	}

	service := mcp.NewService(t.TempDir())
	t.Cleanup(service.Close)
	service.Load(context.Background(), map[string]mcp.ServerConfig{"off": cfg})

	s := &Server{MCP: service, VCSWorkdir: t.TempDir()}
	rec := httptest.NewRecorder()
	s.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mcp/servers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var servers []mcp.ServerInfo
	if err := json.NewDecoder(rec.Body).Decode(&servers); err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %+v, want the one disabled entry", servers)
	}
	got := servers[0]
	if got.ToolCount != 2 || got.EstTokens != 250 {
		t.Errorf("figures = %d tools / %d tokens, want the stored 2 / 250", got.ToolCount, got.EstTokens)
	}
	if got.CostLive {
		t.Error("CostLive = true for a disabled server, want false — the tools are not in the context")
	}
}

var disabled = false

// POST disable persists enabled:false, disconnects and unregisters the
// tools; POST enable reverses both.
func TestEnableDisableRoundTrip(t *testing.T) {
	s, _, globalConfig := newMCPTestServer(t)
	mux := s.Mux()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/live/disable", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable = %d: %s", rec.Code, rec.Body.String())
	}
	saved, _ := os.ReadFile(globalConfig)
	if !strings.Contains(string(saved), `"live"`) || !strings.Contains(string(saved), `"enabled": false`) {
		t.Errorf("global config after disable:\n%s", saved)
	}
	if status := s.MCP.Statuses()["live"]; status.Status != "disabled" {
		t.Errorf("live status after disable = %+v", status)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/mcp/live/enable", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("enable = %d: %s", rec.Code, rec.Body.String())
	}
	saved, _ = os.ReadFile(globalConfig)
	var cfg struct {
		MCP map[string]struct {
			Enabled *bool `json:"enabled"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(saved, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.MCP["live"].Enabled != nil {
		t.Errorf("live entry still carries enabled=%v after re-enable:\n%s", *cfg.MCP["live"].Enabled, saved)
	}
	if status := s.MCP.Statuses()["live"]; status.Status != "connected" {
		t.Errorf("live status after enable = %+v", status)
	}
}

// DELETE removes the entry from the config and the connection from the
// live list; a disabled-only server (off) is untouched.
func TestDeleteMCPServer(t *testing.T) {
	s, _, globalConfig := newMCPTestServer(t)
	mux := s.Mux()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/mcp/live", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	saved, _ := os.ReadFile(globalConfig)
	if strings.Contains(string(saved), `"live"`) {
		t.Errorf("deleted server survived in config:\n%s", saved)
	}
	if !strings.Contains(string(saved), `"off"`) {
		t.Errorf("unrelated server was removed:\n%s", saved)
	}
	if _, ok := s.MCP.Statuses()["live"]; ok {
		t.Error("deleted server still listed in statuses")
	}
}

// An unknown server is a 404, not a 500.
func TestUnknownMCPServerIs404(t *testing.T) {
	s, _, _ := newMCPTestServer(t)
	mux := s.Mux()
	for _, req := range []struct {
		method, path string
	}{
		{http.MethodPost, "/api/mcp/nope/enable"},
		{http.MethodPost, "/api/mcp/nope/disable"},
		{http.MethodDelete, "/api/mcp/nope"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", req.method, req.path, rec.Code)
		}
	}
}
