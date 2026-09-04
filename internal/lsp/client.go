package lsp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/global"
)

// Timeouts ported from client.ts.
const (
	initializeTimeout       = 45 * time.Second
	diagnosticsDebounce     = 150 * time.Millisecond
	diagnosticsDocumentWait = 5 * time.Second
	shutdownTimeout         = 2 * time.Second
	syncKindIncremental     = 2
)

// Client is one running language server.
type Client struct {
	ServerID string
	Root     string

	cmd  *exec.Cmd
	conn *conn

	// syncKind is the server's declared textDocumentSync; full sync is used
	// unless the server asked for incremental.
	syncKind int

	mu sync.Mutex
	// files tracks open documents so didChange carries the right version.
	files map[string]*openFile
	// diagnostics holds the latest push for each file, keyed by absolute path.
	diagnostics map[string][]Diagnostic
	// publishSeq counts publishes per file. A waiter records the count before
	// asking the server to reload and waits for it to move, so a publish that
	// lands before the waiter registers is not missed — servers routinely
	// answer faster than the caller gets to the next line.
	publishSeq map[string]uint64
	// waiters are notified when a publish for their file arrives.
	waiters map[string][]chan struct{}
	closed  bool
}

type openFile struct {
	version int
	text    string
}

// Spawn starts a language server and completes the initialize handshake.
func Spawn(ctx context.Context, serverID, root string, command []string, env map[string]string, initialization map[string]any) (*Client, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// A server that writes to stderr must not block on a full pipe, and its
	// output must never reach the terminal — the interface owns that surface.
	// See internal/global/diag.go for the same rule applied to MCP.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go drainStderr(serverID, stderr)

	client := &Client{
		ServerID:    serverID,
		Root:        root,
		cmd:         cmd,
		conn:        newConn(stdin, stdout),
		files:       map[string]*openFile{},
		diagnostics: map[string][]Diagnostic{},
		publishSeq:  map[string]uint64{},
		waiters:     map[string][]chan struct{}{},
	}
	client.register()
	go client.conn.listen()

	if err := client.initialize(ctx, initialization); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func drainStderr(serverID string, stderr interface{ Read([]byte) (int, error) }) {
	buffer := make([]byte, 4096)
	for {
		n, err := stderr.Read(buffer)
		if n > 0 {
			global.LogBackground("lsp[%s]: %s", serverID, string(buffer[:n]))
		}
		if err != nil {
			return
		}
	}
}

// register wires the handlers a server may call before initialize returns, so
// none of them race the handshake.
func (c *Client) register() {
	c.conn.onNotify("textDocument/publishDiagnostics", c.onPublishDiagnostics)

	// Servers commonly ask for configuration and for dynamic capability
	// registration during startup. Answering (even emptily) keeps them moving;
	// leaving these unanswered makes several servers stall.
	c.conn.handle("workspace/configuration", func(params json.RawMessage) (any, error) {
		var request struct {
			Items []struct {
				Section string `json:"section"`
			} `json:"items"`
		}
		json.Unmarshal(params, &request)
		out := make([]any, len(request.Items))
		return out, nil
	})
	c.conn.handle("client/registerCapability", func(json.RawMessage) (any, error) { return nil, nil })
	c.conn.handle("client/unregisterCapability", func(json.RawMessage) (any, error) { return nil, nil })
	c.conn.handle("window/workDoneProgress/create", func(json.RawMessage) (any, error) { return nil, nil })
	c.conn.handle("workspace/diagnostic/refresh", func(json.RawMessage) (any, error) { return nil, nil })
	c.conn.handle("workspace/applyEdit", func(json.RawMessage) (any, error) {
		// This client never applies server-driven edits; the agent owns edits.
		return map[string]any{"applied": false}, nil
	})
}

func (c *Client) onPublishDiagnostics(params json.RawMessage) {
	var payload publishDiagnosticsParams
	if err := json.Unmarshal(params, &payload); err != nil {
		return
	}
	path, ok := pathFromURI(payload.URI)
	if !ok {
		return
	}
	path = normalizePath(path)

	c.mu.Lock()
	c.diagnostics[path] = payload.Diagnostics
	c.publishSeq[path]++
	waiters := c.waiters[path]
	delete(c.waiters, path)
	c.mu.Unlock()

	for _, waiter := range waiters {
		close(waiter)
	}
}

// initialize performs the handshake, porting the capabilities block in
// client.ts.
func (c *Client) initialize(ctx context.Context, initialization map[string]any) error {
	ctx, cancel := context.WithTimeout(ctx, initializeTimeout)
	defer cancel()

	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   uriFromPath(c.Root),
		"workspaceFolders": []map[string]any{
			{"name": "workspace", "uri": uriFromPath(c.Root)},
		},
		"initializationOptions": initialization,
		"capabilities": map[string]any{
			"window": map[string]any{"workDoneProgress": true},
			"workspace": map[string]any{
				"configuration":         true,
				"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true},
				"diagnostics":           map[string]any{"refreshSupport": false},
			},
			"textDocument": map[string]any{
				"synchronization": map[string]any{"didOpen": true, "didChange": true},
				"diagnostic": map[string]any{
					"dynamicRegistration":    true,
					"relatedDocumentSupport": true,
				},
				"publishDiagnostics": map[string]any{"versionSupport": false},
			},
		},
	}

	var result initializeResult
	if err := c.conn.call(ctx, "initialize", params, &result); err != nil {
		return err
	}
	c.syncKind = result.Capabilities.syncKind()

	if err := c.conn.send("initialized", map[string]any{}); err != nil {
		return err
	}
	if len(initialization) > 0 {
		c.conn.send("workspace/didChangeConfiguration", map[string]any{"settings": initialization})
	}
	return nil
}

// Open notifies the server about a file's current contents, sending didOpen
// the first time and didChange afterwards.
//
// changed reports whether the server was actually told anything. It is false
// when the file is already open at these exact contents, and the caller must
// then not wait for diagnostics: nothing will be published, so waiting only
// burns the timeout. That path is common — reading a file twice in a turn.
func (c *Client) Open(path string) (changed bool, err error) {
	path = normalizePath(path)
	text, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	c.mu.Lock()
	existing, seen := c.files[path]
	if seen && existing.text == string(text) {
		c.mu.Unlock()
		return false, nil
	}
	if !seen {
		existing = &openFile{}
		c.files[path] = existing
	}
	existing.version++
	existing.text = string(text)
	version := existing.version
	c.mu.Unlock()

	uri := uriFromPath(path)
	if !seen {
		return true, c.conn.send("textDocument/didOpen", map[string]any{
			"textDocument": textDocumentItem{
				URI:        uri,
				LanguageID: languageID(path),
				Version:    version,
				Text:       string(text),
			},
		})
	}

	// A server that asked for incremental sync still accepts a single
	// full-document change, which is what this sends: the agent replaces whole
	// files, so there is no incremental edit to describe.
	change := map[string]any{"text": string(text)}
	return true, c.conn.send("textDocument/didChange", map[string]any{
		"textDocument":   versionedTextDocumentIdentifier{URI: uri, Version: version},
		"contentChanges": []map[string]any{change},
	})
}

// PublishSeq returns how many times this file has been published. Capture it
// before asking the server to reload, then pass it to WaitForDiagnostics.
func (c *Client) PublishSeq(path string) uint64 {
	path = normalizePath(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.publishSeq[path]
}

// WaitForDiagnostics blocks until the server publishes for this file beyond
// the sequence the caller saw, or the timeout lapses. A timeout is not an
// error: many servers publish nothing for a file they consider clean.
func (c *Client) WaitForDiagnostics(ctx context.Context, path string, since uint64) {
	path = normalizePath(path)
	waiter := make(chan struct{})

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.publishSeq[path] > since {
		// Already published between the caller's snapshot and now.
		c.mu.Unlock()
		return
	}
	c.waiters[path] = append(c.waiters[path], waiter)
	c.mu.Unlock()

	timer := time.NewTimer(diagnosticsDocumentWait)
	defer timer.Stop()
	select {
	case <-waiter:
		// Servers often publish in bursts as analysis proceeds. A short settle
		// lets the follow-up land instead of reporting the first partial pass.
		time.Sleep(diagnosticsDebounce)
	case <-timer.C:
	case <-ctx.Done():
	}
}

// Diagnostics returns a copy of everything published so far.
func (c *Client) Diagnostics() map[string][]Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string][]Diagnostic, len(c.diagnostics))
	for path, items := range c.diagnostics {
		out[path] = append([]Diagnostic(nil), items...)
	}
	return out
}

// DiagnosticsFor returns the diagnostics for one file.
func (c *Client) DiagnosticsFor(path string) []Diagnostic {
	path = normalizePath(path)
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Diagnostic(nil), c.diagnostics[path]...)
}

// DocumentSymbols requests textDocument/documentSymbol for path, which the
// caller must already have opened (Open) so the server has something to
// answer about. Used by the RAG plugin's syntax-aware chunker to split code
// files at real function/class/method boundaries instead of arbitrary line
// windows — see internal/rag/chunk/syntax.go.
func (c *Client) DocumentSymbols(ctx context.Context, path string) ([]DocumentSymbol, error) {
	path = normalizePath(path)
	var raw json.RawMessage
	err := c.conn.call(ctx, "textDocument/documentSymbol", map[string]any{
		"textDocument": textDocumentIdentifier{URI: uriFromPath(path)},
	}, &raw)
	if err != nil {
		return nil, err
	}
	return decodeDocumentSymbols(raw)
}

// decodeDocumentSymbols accepts either response shape the spec allows:
// hierarchical DocumentSymbol[] (has "range" directly on each entry) or flat
// SymbolInformation[] (has "location": {"range": ...} instead). Presence of
// a "location" key on the first entry distinguishes them; a mixed array
// never happens in practice, since it comes from one server's one
// capability choice.
func decodeDocumentSymbols(raw json.RawMessage) ([]DocumentSymbol, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var probe []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	if len(probe) == 0 {
		return nil, nil
	}
	if _, flat := probe[0]["location"]; flat {
		var symbols []struct {
			Name     string `json:"name"`
			Kind     int    `json:"kind"`
			Location struct {
				Range Range `json:"range"`
			} `json:"location"`
		}
		if err := json.Unmarshal(raw, &symbols); err != nil {
			return nil, err
		}
		out := make([]DocumentSymbol, len(symbols))
		for i, s := range symbols {
			out[i] = DocumentSymbol{Name: s.Name, Kind: s.Kind, Range: s.Location.Range, SelectionRange: s.Location.Range}
		}
		return out, nil
	}

	var hierarchical []DocumentSymbol
	if err := json.Unmarshal(raw, &hierarchical); err != nil {
		return nil, err
	}
	return hierarchical, nil
}

// Close shuts the server down, politely first and then forcibly.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	// Best effort: a server that is wedged should not hold up the caller.
	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		c.conn.call(ctx, "shutdown", nil, nil)
		c.conn.send("exit", nil)
	}()
	select {
	case <-done:
	case <-time.After(shutdownTimeout):
	}

	c.conn.shutdown(errConnClosed)
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	go c.cmd.Wait() // reap, without blocking shutdown
	return nil
}

// normalizePath resolves a path so the same file is always keyed identically,
// whichever way the caller or the server spelled it (symlinked temp dirs on
// macOS being the usual culprit).
func normalizePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
