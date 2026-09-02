package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// A fake language server, run as a subprocess of the test binary.
//
// This is the standard Go helper-process pattern rather than a mock: the test
// exercises the real spawn, the real pipes and the real Content-Length
// framing, which is where a client like this actually goes wrong. It stands in
// for test/fixture/lsp/fake-lsp-server.js on the TypeScript side.
const fakeServerEnv = "OPENCODE_LSP_FAKE_SERVER"

// TestMain lets the test binary re-exec itself as the fake server.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeServerEnv); mode != "" {
		runFakeServer(mode)
		return
	}
	os.Exit(m.Run())
}

// fakeServerCommand returns the argv that runs this test binary as a server.
func fakeServerCommand() ([]string, map[string]string, string) {
	return []string{os.Args[0]}, map[string]string{fakeServerEnv: "normal"}, "normal"
}

func runFakeServer(mode string) {
	reader := bufio.NewReader(os.Stdin)
	writer := os.Stdout

	send := func(message any) {
		payload, _ := json.Marshal(message)
		fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n", len(payload))
		writer.Write(payload)
	}

	for {
		payload, err := readFrame(reader)
		if err != nil {
			return
		}
		var message struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
			Params json.RawMessage  `json:"params"`
		}
		if err := json.Unmarshal(payload, &message); err != nil {
			continue
		}

		switch message.Method {
		case "initialize":
			if mode == "no-initialize" {
				// Never answer, to exercise the initialize timeout path.
				continue
			}
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      message.ID,
				"result": map[string]any{
					"capabilities": map[string]any{
						"textDocumentSync":   map[string]any{"change": 2},
						"diagnosticProvider": true,
					},
				},
			})
		case "textDocument/didOpen", "textDocument/didChange":
			var params struct {
				TextDocument struct {
					URI  string `json:"uri"`
					Text string `json:"text"`
				} `json:"textDocument"`
				ContentChanges []struct {
					Text string `json:"text"`
				} `json:"contentChanges"`
			}
			json.Unmarshal(message.Params, &params)
			text := params.TextDocument.Text
			if len(params.ContentChanges) > 0 {
				text = params.ContentChanges[0].Text
			}
			// The fake's rule: every line holding BUG is an error, and every
			// line holding WARN is a warning.
			diagnostics := []map[string]any{}
			for i, line := range strings.Split(text, "\n") {
				severity := 0
				switch {
				case strings.Contains(line, "BUG"):
					severity = 1
				case strings.Contains(line, "WARN"):
					severity = 2
				}
				if severity == 0 {
					continue
				}
				diagnostics = append(diagnostics, map[string]any{
					"range": map[string]any{
						"start": map[string]any{"line": i, "character": 0},
						"end":   map[string]any{"line": i, "character": len(line)},
					},
					"severity": severity,
					"source":   "fake",
					"message":  strings.TrimSpace(line),
				})
			}
			send(map[string]any{
				"jsonrpc": "2.0",
				"method":  "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri":         params.TextDocument.URI,
					"diagnostics": diagnostics,
				},
			})
		case "shutdown":
			send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": nil})
		case "exit":
			return
		default:
			// Answer any other request so the client never hangs.
			if message.ID != nil {
				send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": nil})
			}
		}
	}
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "content-length") {
			length, _ = strconv.Atoi(strings.TrimSpace(value))
		}
	}
	if length < 0 {
		return nil, io.EOF
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}
