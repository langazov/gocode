package acp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
)

func TestNESPositionsAreUTF16(t *testing.T) {
	text := "a😀b\nxyz\n"
	// 😀 is two UTF-16 units: "b" is character 3.
	if off := offsetOf(text, position{0, 3}); text[off:off+1] != "b" {
		t.Fatalf("offset of (0,3) = %d", off)
	}
	if p := positionOf(text, strings.Index(text, "b")); p != (position{0, 3}) {
		t.Fatalf("position of b = %+v", p)
	}
	// Past the end of a line clamps to the line end; past the text to its end.
	if off := offsetOf(text, position{1, 99}); off != strings.Index(text, "xyz")+3 {
		t.Fatalf("clamp = %d", off)
	}
	if off := offsetOf(text, position{9, 0}); off != len(text) {
		t.Fatalf("past end = %d", off)
	}
}

const nesFile = "package main\n\nfunc add(a, b int) int {\n\treturn a + b\n}\n\nfunc main() {\n\tx := add(1, 2)\n\tprintln(x)\n}\n"

func TestNESRewriteToCompletionAtCursor(t *testing.T) {
	text := "package main\n\nfunc main() {\n\tfmt.Pri\n}\n"
	pos := position{Line: 3, Character: 8} // after "fmt.Pri"
	req := buildNESRequest("file:///w/main.go", "go", text, pos, nil, nil)
	if !strings.Contains(req.prompt, "\tfmt.Pri"+cursorMarker+"\n") {
		t.Fatalf("cursor marker missing:\n%s", req.prompt)
	}
	region := text[req.start:req.end]
	rewritten := strings.Replace(region, "fmt.Pri", `fmt.Println("hi")`, 1)
	edit, ok := req.edit("```go\n" + rewritten + "\n```")
	if !ok {
		t.Fatal("no edit")
	}
	if len(edit.Edits) != 1 || edit.Edits[0].Range.Start != pos || edit.Edits[0].Range.End != pos || edit.Edits[0].NewText != `ntln("hi")` {
		t.Fatalf("completion edit = %+v", edit)
	}
	if edit.Cursor != (position{3, 18}) {
		t.Fatalf("cursor after = %+v", edit.Cursor)
	}
}

func TestNESRewriteToNextEdit(t *testing.T) {
	// The user renamed the function; the prediction updates the call site.
	text := strings.Replace(nesFile, "func add(", "func sum(", 1)
	pos := position{Line: 2, Character: 8}
	req := buildNESRequest("file:///w/main.go", "go", text, pos, []string{"@@ -3 +3 @@\n-func add(a, b int) int {\n+func sum(a, b int) int {\n"}, nil)
	if !strings.Contains(req.prompt, "Recent edits") || !strings.Contains(req.prompt, "+func sum(") {
		t.Fatalf("history missing:\n%s", req.prompt)
	}
	region := text[req.start:req.end]
	edit, ok := req.edit(strings.Replace(region, "add(1, 2)", "sum(1, 2)", 1))
	if !ok {
		t.Fatal("no edit")
	}
	if len(edit.Edits) != 1 || edit.Edits[0].Range.Start.Line != 7 || edit.Edits[0].NewText != "sum" {
		t.Fatalf("next edit = %+v", edit)
	}
	// An unchanged rewrite is no suggestion.
	if _, ok := req.edit(region); ok {
		t.Fatal("unchanged region produced an edit")
	}
	// Nor is an empty reply (never "delete the region").
	if _, ok := req.edit("  \n"); ok {
		t.Fatal("empty reply produced an edit")
	}
	// Nor an implausible one.
	if _, ok := req.edit("x"); ok {
		t.Fatal("collapsed region produced an edit")
	}
}

func TestNESSeparateHunksAreSeparateEdits(t *testing.T) {
	text := "func f() {\n\ta := add(1)\n\tb := add(2)\n}\n"
	req := buildNESRequest("file:///w/a.go", "go", text, position{0, 0}, nil, nil)
	edit, ok := req.edit(strings.ReplaceAll(text, "add(", "sum("))
	if !ok || len(edit.Edits) != 2 {
		t.Fatalf("edits = %+v", edit)
	}
	for k, e := range edit.Edits {
		if e.NewText != "sum" || e.Range.Start != (position{k + 1, 6}) || e.Range.End != (position{k + 1, 9}) {
			t.Fatalf("edit %d = %+v", k, e)
		}
	}
	if edit.Cursor != (position{1, 9}) {
		t.Fatalf("cursor = %+v", edit.Cursor)
	}
}

func TestNESInsertionSlidesToCursor(t *testing.T) {
	// "foo(" with cursor after "(", model writes "foo(x)" where ")" already
	// follows: the common-prefix diff would anchor after the ")".
	text := "foo()\n"
	req := buildNESRequest("file:///w/a.go", "go", text, position{0, 4}, nil, nil)
	edit, ok := req.edit("foo(x)()\n")
	if !ok || edit.Edits[0].Range.Start != (position{0, 4}) {
		t.Fatalf("edit = %+v %v", edit, ok)
	}
}

func TestNESProtocol(t *testing.T) {
	for _, version := range []int{ProtocolV1, ProtocolV2} {
		t.Run(versionName(version), func(t *testing.T) {
			provider := &scriptedProvider{}
			f := newFixture(t, version, provider, fixtureOptions{})

			// Advertised under _meta.gocode.nes in both versions.
			var init map[string]any
			params := map[string]any{"protocolVersion": version}
			f.client.call("initialize", params, &init)
			capsKey := "agentCapabilities"
			if version == ProtocolV2 {
				capsKey = "capabilities"
			}
			caps, _ := init[capsKey].(map[string]any)
			meta, _ := caps["_meta"].(map[string]any)
			gocode, _ := meta["gocode"].(map[string]any)
			if nes, _ := gocode["nes"].(map[string]any); nes["positionEncoding"] != "utf-16" {
				t.Fatalf("nes capability missing: %v", caps)
			}

			var started struct {
				SessionID string `json:"sessionId"`
			}
			f.client.call("_gocode/nes/start", map[string]any{"workspaceUri": "file://" + f.dir}, &started)
			if started.SessionID == "" {
				t.Fatal("no NES session")
			}
			uri := "file://" + f.dir + "/main.go"
			notify := func(method string, p map[string]any) {
				p["sessionId"] = started.SessionID
				if err := f.client.conn.Notify("_gocode/"+method, p); err != nil {
					t.Fatal(err)
				}
			}
			notify("document/didOpen", map[string]any{"uri": uri, "languageId": "go", "version": 1, "text": nesFile})
			// The user renames add -> sum (incremental change).
			notify("document/didChange", map[string]any{"uri": uri, "version": 2, "contentChanges": []any{
				map[string]any{"range": map[string]any{"start": map[string]any{"line": 2, "character": 5}, "end": map[string]any{"line": 2, "character": 8}}, "text": "sum"},
			}})
			time.Sleep(50 * time.Millisecond) // notifications are processed in order before the call

			// The model rewrites the region with the call site renamed.
			renamed := strings.Replace(nesFile, "func add(", "func sum(", 1)
			provider.mu.Lock()
			provider.turns = [][]llm.StreamEvent{textTurn(strings.Replace(renamed, "add(1, 2)", "sum(1, 2)", 1))}
			provider.mu.Unlock()

			var result struct {
				Suggestions []struct {
					ID    string `json:"id"`
					Kind  string `json:"kind"`
					URI   string `json:"uri"`
					Edits []struct {
						Range   textRange `json:"range"`
						NewText string    `json:"newText"`
					} `json:"edits"`
					CursorPosition position `json:"cursorPosition"`
				} `json:"suggestions"`
			}
			f.client.call("_gocode/nes/suggest", map[string]any{
				"sessionId": started.SessionID, "uri": uri, "version": 2,
				"position": map[string]any{"line": 2, "character": 8}, "triggerKind": "automatic",
				"context": map[string]any{"diagnostics": []any{map[string]any{"uri": uri,
					"range":   map[string]any{"start": map[string]any{"line": 7, "character": 6}, "end": map[string]any{"line": 7, "character": 9}},
					"message": "undefined: add", "severity": "error"}}},
			}, &result)
			if len(result.Suggestions) != 1 {
				t.Fatalf("suggestions: %+v", result)
			}
			s := result.Suggestions[0]
			if s.Kind != "edit" || s.URI != uri || len(s.Edits) != 1 || s.Edits[0].NewText != "sum" || s.Edits[0].Range.Start != (position{7, 6}) {
				t.Fatalf("suggestion: %+v", s)
			}

			// The prompt saw the synced document, the edit history and the
			// diagnostic, and went to the provider with the default model.
			provider.mu.Lock()
			req := provider.requests[len(provider.requests)-1]
			provider.mu.Unlock()
			prompt := req.Messages[0].Content[0].Text
			for _, want := range []string{"func sum(a, b int)", "-func add(a, b int) int {", "undefined: add", cursorMarker} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("prompt lacks %q:\n%s", want, prompt)
				}
			}
			if req.ModelID != "fake-model" {
				t.Fatalf("model = %s", req.ModelID)
			}

			notify("nes/accept", map[string]any{"id": s.ID})
			var closed map[string]any
			f.client.call("_gocode/nes/close", map[string]any{"sessionId": started.SessionID}, &closed)
			if err := f.client.try("_gocode/nes/suggest", map[string]any{"sessionId": started.SessionID, "uri": uri, "position": map[string]any{"line": 0, "character": 0}}, nil); err == nil {
				t.Fatal("suggest after close should fail")
			}
		})
	}
}

func versionName(v int) string {
	b, _ := json.Marshal(v)
	return "v" + string(b)
}
