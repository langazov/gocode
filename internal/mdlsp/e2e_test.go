package mdlsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// e2eEnv re-executes this test binary as a real mdlsp server over stdio, so
// the test exercises OS pipes and process lifetime the way an editor does.
// This is the helper-process pattern used by internal/lsp/fakeserver_test.go.
const e2eEnv = "GOCODE_MDLSP_E2E"

func TestMain(m *testing.M) {
	if os.Getenv(e2eEnv) != "" {
		runE2EServer()
		return
	}
	os.Exit(m.Run())
}

func runE2EServer() {
	// The e2e server lives exactly as long as its stdio pipes; no signal
	// handling beyond what the test does.
	server := New(os.Stdin, os.Stdout)
	_ = server.Serve(context.Background())
}

func TestEndToEndSubprocess(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), e2eEnv+"=1")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.(io.Closer).Close()
		_ = cmd.Wait()
	})

	// A message that never comes must fail this test, not hang until the
	// package-wide timeout takes the whole suite down with it.
	if f, ok := stdout.(*os.File); ok {
		if err := f.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
			t.Logf("stdout read deadline unavailable: %v", err)
		}
	}

	// A minimal but real client: Content-Length framing by hand.
	writeMsg := func(v any) {
		payload, _ := json.Marshal(v)
		fmt.Fprintf(stdin, "Content-Length: %d\r\n\r\n", len(payload))
		stdin.Write(payload)
	}
	// One reader for the whole stream, not one per message: a buffered reader
	// reads ahead, so a per-call reader that happened to pull two frames into
	// its buffer would return the first and discard the second with itself,
	// leaving the next call blocked on bytes that already arrived.
	r := bufio.NewReader(stdout)
	readMsg := func() map[string]any {
		t.Helper()
		length := -1
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("reading headers: %v", err)
			}
			if strings.TrimSpace(line) == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				fmt.Sscanf(strings.TrimSpace(line), "Content-Length: %d", &length)
			}
		}
		if length < 0 {
			t.Fatal("no Content-Length")
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			t.Fatal(err)
		}
		var msg map[string]any
		json.Unmarshal(body, &msg)
		return msg
	}

	call := func(method string, params any, deadline time.Time) map[string]any {
		t.Helper()
		writeMsg(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		for {
			if time.Now().After(deadline) {
				t.Fatalf("%s: no response", method)
			}
			msg := readMsg()
			if _, ok := msg["id"]; ok {
				return msg
			}
			// A notification (publishDiagnostics); keep reading.
		}
	}

	deadline := time.Now().Add(10 * time.Second)

	// Handshake, open a document, get an outline — all through the real pipe.
	resp := call("initialize", map[string]any{
		"rootUri": fileURIRoot(root),
	}, deadline)
	caps, _ := resp["result"].(map[string]any)
	if caps != nil {
		caps, _ = caps["capabilities"].(map[string]any)
	}
	if caps == nil {
		t.Fatalf("initialize result = %v", resp)
	}
	if _, ok := caps["documentSymbolProvider"]; !ok {
		t.Fatalf("initialize missing documentSymbolProvider: %v", caps)
	}
	writeMsg(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}})

	uri := uriFor(root, "doc.md")
	text := "# Hello\n\nsee [x](#hello)\n"
	writeMsg(map[string]any{"jsonrpc": "2.0", "method": "textDocument/didOpen", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri, "languageId": "markdown", "version": 1, "text": text},
	}})

	// Consume the diagnostics notification, then the symbols response.
	writeMsg(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "textDocument/documentSymbol", "params": map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}})
	sawDiags := false
	var symbols []map[string]any
	for {
		if time.Now().After(deadline) {
			t.Fatal("never received symbols")
		}
		msg := readMsg()
		if msg["method"] == "textDocument/publishDiagnostics" {
			sawDiags = true
			continue
		}
		if msg["id"] == float64(2) {
			raw, _ := json.Marshal(msg["result"])
			json.Unmarshal(raw, &symbols)
			break
		}
	}
	if !sawDiags {
		t.Error("expected a diagnostics notification before the symbols reply")
	}
	if len(symbols) != 1 || symbols[0]["name"] != "Hello" {
		t.Fatalf("symbols = %v", symbols)
	}

	// Shutdown handshake.
	if resp := call("shutdown", nil, deadline); resp["result"] != nil {
		t.Fatalf("shutdown result = %v", resp["result"])
	}
	writeMsg(map[string]any{"jsonrpc": "2.0", "method": "exit", "params": nil})
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit after exit notification")
	}
}
