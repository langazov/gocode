package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/session"
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

// Regression: real model replies that would have wiped or duplicated code.
func TestNESRejectsDestructiveRewrites(t *testing.T) {
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("\tstep%d := compute(%d)", i, i))
	}
	text := "func f() {\n" + strings.Join(lines, "\n") + "\n}\n"
	req := buildNESRequest("file:///w/a.go", "go", text, position{15, 0}, nil, nil)
	region := text[req.start:req.end]
	regionLines := strings.SplitAfter(region, "\n")

	var reasons []string
	nesReject = func(r string) { reasons = append(reasons, r) }
	defer func() { nesReject = func(string) {} }()
	reject := func(name, output string) {
		t.Helper()
		reasons = nil
		if e, ok := req.edit(output); ok {
			t.Fatalf("%s: accepted %+v", name, e)
		}
		t.Logf("%s: %v", name, reasons)
	}
	// Only the lines around the cursor came back, unchanged: placed, so
	// nothing else is touched — and with no change, no suggestion.
	reject("fragment", strings.Join(regionLines[6:11], ""))
	// A fragment with a change is placed and edits only its own lines.
	frag := strings.Replace(strings.Join(regionLines[6:11], ""), "compute(15)", "compute(15, true)", 1)
	if e, ok := req.edit(frag); !ok || len(e.Edits) != 1 || e.Edits[0].NewText != ", true" || e.Edits[0].Range.Start.Line != 16 {
		t.Fatalf("fragment edit = %+v %v", e, ok)
	}
	// A cursor-line-only reply is a completion at the cursor.
	reqC := buildNESRequest("file:///w/a.go", "go", "func f() {\n\tfmt.Pri\n\tx := 1\n\ty := 2\n}\n", position{1, 8}, nil, nil)
	if e, ok := reqC.edit("\tfmt.Println(x)"); !ok || e.Edits[0].NewText != "ntln(x)" || e.Edits[0].Range.Start != (position{1, 8}) {
		t.Fatalf("cursor-line reply = %+v %v", e, ok)
	}
	// A chunk starting before the region, with the first line under-
	// indented, is placed and keeps the document's indentation.
	chunk := "\t\tstep1 := compute(1)\n" + strings.Join(lines[2:12], "\n") + "\n"
	chunk = strings.Replace(chunk, "compute(10)", "compute(10, true)", 1)
	if e, ok := req.edit(chunk); !ok || len(e.Edits) != 1 || e.Edits[0].NewText != ", true" || e.Edits[0].Range.Start.Line != 11 {
		t.Fatalf("context chunk = %+v %v", e, ok)
	}
	// An unindented cursor-line reply keeps the line's indentation.
	if e, ok := reqC.edit("fmt.Println(y)"); !ok || len(e.Edits) != 1 || e.Edits[0].NewText != "ntln(y)" || e.Edits[0].Range.Start != (position{1, 8}) {
		t.Fatalf("unindented cursor-line reply = %+v %v", e, ok)
	}
	// A fragment that can't be placed is dropped.
	reject("unplaceable", "\tnothing like this := here()\n\tor this()\n")
	// The region with a copy of nearby lines pasted in.
	reject("duplicate", strings.Join(regionLines[:9], "")+strings.Join(regionLines[3:7], "")+strings.Join(regionLines[9:], ""))
	// Nearby lines pasted in a different order (the line-45 case).
	reject("reordered duplicate", strings.Join(regionLines[:9], "")+regionLines[5]+regionLines[3]+regionLines[4]+strings.Join(regionLines[9:], ""))
	// A mangled marker in the reply.
	reject("marker", strings.Replace(region, "step15", "|editable_region_start|>step15", 1))
	// Dropping three lines in the middle.
	reject("deletion", strings.Join(regionLines[:5], "")+strings.Join(regionLines[8:], ""))

	// A real multi-line completion is still fine.
	text2 := "func g(xs []int) int {\n\ttotal := 0\n\tfor _, x := range xs {\n\t\t\n\t}\n\treturn total\n}\n"
	req2 := buildNESRequest("file:///w/a.go", "go", text2, position{3, 2}, nil, nil)
	out := strings.Replace(text2[req2.start:req2.end], "\t\t\n", "\t\tif x > 0 {\n\t\t\ttotal += x\n\t\t}\n", 1)
	if e, ok := req2.edit(out); !ok || len(e.Edits) != 1 || !strings.Contains(e.Edits[0].NewText, "total += x") {
		t.Fatalf("multi-line completion rejected: %+v %v", e, ok)
	}
}

func TestNESFillInTheMiddle(t *testing.T) {
	text := "func f() {\n\tfmt.Println(\n}\n"
	req := buildNESRequest("file:///w/a.go", "go", text, position{1, 13}, nil, nil)
	// The model closes the call; the line already... has nothing after the
	// cursor, so the whole completion stays.
	if e, ok := req.fimEdit("\"hi\")\n\n\tmore()\n"); !ok || e.Edits[0].NewText != `"hi")` || e.Cursor != (position{1, 18}) {
		t.Fatalf("fim = %+v %v", e, ok)
	}
	// Overlap with the text after the cursor is dropped.
	text2 := "x := foo()\n"
	req2 := buildNESRequest("file:///w/a.go", "go", text2, position{0, 9}, nil, nil)
	if e, ok := req2.fimEdit("a, b)"); !ok || e.Edits[0].NewText != "a, b" {
		t.Fatalf("overlap = %+v %v", e, ok)
	}
	// Mid-line, a multi-line insertion is refused (the rewrite path takes
	// over): "func sum|(a, b int) int {" must not get a new body pushed in.
	text3 := "func sum(a, b int) int {\n\treturn a + b\n}\n"
	req3 := buildNESRequest("file:///w/a.go", "go", text3, position{0, 8}, nil, nil)
	if _, ok := req3.fimEdit("(xs []int) int {\n\ttotal := 0\n}"); ok {
		t.Fatal("multi-line mid-line insertion accepted")
	}
	// Nothing to insert: no completion (the rewrite path takes over).
	if _, ok := req2.fimEdit(")\n"); ok {
		t.Fatal("pure overlap produced an edit")
	}
}

func TestNESPrefersFIMAndFallsBackToRewrite(t *testing.T) {
	provider := &scriptedProvider{}
	f := newFixture(t, ProtocolV2, provider, fixtureOptions{})
	var fimCalls int
	fimReply := "ntln(x)"
	f.runtime.FIM = func(ctx context.Context, model session.ModelRef, prefix, suffix string, maxTokens int) (string, bool, error) {
		fimCalls++
		if !strings.HasSuffix(prefix, "fmt.Pri") || !strings.HasPrefix(suffix, "\n}") {
			t.Errorf("fim context: prefix %q suffix %q", prefix, suffix)
		}
		return fimReply, true, nil
	}
	var started struct {
		SessionID string `json:"sessionId"`
	}
	f.client.call("_gocode/nes/start", map[string]any{"workspaceUri": "file://" + f.dir}, &started)
	uri := "file://" + f.dir + "/a.go"
	text := "package a\n\nfunc f(x int) {\n\tfmt.Pri\n}\n"
	f.client.conn.Notify("_gocode/document/didOpen", map[string]any{"sessionId": started.SessionID, "uri": uri, "languageId": "go", "version": 1, "text": text})
	time.Sleep(50 * time.Millisecond)
	suggest := func() []any {
		var res map[string]any
		f.client.call("_gocode/nes/suggest", map[string]any{"sessionId": started.SessionID, "uri": uri, "version": 1,
			"position": map[string]any{"line": 3, "character": 8}}, &res)
		list, _ := res["suggestions"].([]any)
		return list
	}
	got := suggest()
	if len(got) != 1 || !strings.Contains(toJSONString(got), `"newText":"ntln(x)"`) || len(provider.requests) != 0 {
		t.Fatalf("fim suggestion: %v (chat calls %d)", got, len(provider.requests))
	}
	// FIM has nothing to add: the chat rewrite runs instead.
	fimReply = ""
	provider.mu.Lock()
	provider.turns = [][]llm.StreamEvent{textTurn(strings.Replace(text, "fmt.Pri", "fmt.Print(x)", 1))}
	provider.mu.Unlock()
	got = suggest()
	if fimCalls != 2 || len(provider.requests) != 1 || !strings.Contains(toJSONString(got), `"newText":"nt(x)"`) {
		t.Fatalf("fallback: %v (fim %d, chat %d)", got, fimCalls, len(provider.requests))
	}
}

func toJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
