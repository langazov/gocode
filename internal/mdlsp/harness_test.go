package mdlsp

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/jsonrpc"
)

// testPair wires a server and a test client over in-memory pipes, exercising
// the real framing in both directions without spawning a process.
type testPair struct {
	client *jsonrpc.Conn
	server *Server
	done   chan error
}

func newTestPair(t *testing.T) *testPair {
	t.Helper()
	cRead, sWrite := io.Pipe()
	sRead, cWrite := io.Pipe()
	pair := &testPair{
		client: jsonrpc.NewConn(cWrite, cRead),
		server: New(sRead, sWrite),
		done:   make(chan error, 1),
	}
	go func() { pair.done <- pair.server.Serve(context.Background()) }()
	go pair.client.Listen()
	t.Cleanup(func() {
		pair.client.Shutdown(io.ErrClosedPipe)
		<-pair.done
	})
	return pair
}

// initialize performs the handshake against a workspace root.
func (p *testPair) initialize(t *testing.T, root string) map[string]any {
	t.Helper()
	var result struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	err := p.client.Call(context.Background(), "initialize", map[string]any{
		"rootUri": fileURIRoot(root),
	}, &result)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	_ = p.client.Notify("initialized", map[string]any{})
	return result.Capabilities
}

func fileURIRoot(path string) string {
	return "file://" + path
}

// open sends didOpen with text and waits for the diagnostics notification.
func (p *testPair) open(t *testing.T, uri, text string) []publishDiagnosticsParams {
	t.Helper()
	return p.openVersion(t, uri, text, 1)
}

func (p *testPair) openVersion(t *testing.T, uri, text string, version int) []publishDiagnosticsParams {
	t.Helper()
	diags := make(chan publishDiagnosticsParams, 8)
	p.client.OnNotify("textDocument/publishDiagnostics", func(params json.RawMessage) {
		var p publishDiagnosticsParams
		json.Unmarshal(params, &p)
		diags <- p
	})
	_ = p.client.Notify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": uri, "languageId": "markdown", "version": version, "text": text,
		},
	})
	var out []publishDiagnosticsParams
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case d := <-diags:
			out = append(out, d)
			continue
		case <-timeout:
		}
		break
	}
	return out
}
