package mdlsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/lspprotocol"
)

// writeTree lays out files under a temp workspace root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// uriFor builds the URI an editor would send for a workspace file. It must go
// through URIFromPath rather than concatenating: a Windows path needs its
// backslashes flipped and its drive letter moved behind a slash, and
// "file://C:\..." parses as a host with an invalid port, which PathFromURI
// rejects — leaving the server with no document and every assertion empty.
func uriFor(root, rel string) string {
	return lspprotocol.URIFromPath(filepath.Join(root, rel))
}

func TestInitializeDeclaresCapabilities(t *testing.T) {
	pair := newTestPair(t)
	caps := pair.initialize(t, t.TempDir())
	for _, key := range []string{
		"documentSymbolProvider", "definitionProvider", "referencesProvider",
		"renameProvider", "workspaceSymbolProvider", "documentFormattingProvider",
		"foldingRangeProvider", "documentLinkProvider", "completionProvider",
	} {
		if _, ok := caps[key]; !ok {
			t.Errorf("capabilities missing %q: %v", key, caps)
		}
	}
}

func TestDocumentSymbolHeadingTree(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "doc.md")
	pair.open(t, uri, "# Top\n\n## Sub A\n\ntext\n\n### Deep\n\n## Sub B\n")

	var symbols []lspprotocol.DocumentSymbol
	err := pair.client.Call(context.Background(), "textDocument/documentSymbol",
		map[string]any{"textDocument": map[string]any{"uri": uri}}, &symbols)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 1 || symbols[0].Name != "Top" {
		t.Fatalf("roots = %+v, want one Top", symbols)
	}
	children := symbols[0].Children
	if len(children) != 2 || children[0].Name != "Sub A" || children[1].Name != "Sub B" {
		t.Fatalf("children = %+v", children)
	}
	if len(children[0].Children) != 1 || children[0].Children[0].Name != "Deep" {
		t.Fatalf("deep = %+v", children[0].Children)
	}
	if children[0].Range.Start.Line != 2 {
		t.Fatalf("Sub A range = %+v", children[0].Range)
	}
}

func TestFoldingRange(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "doc.md")
	pair.open(t, uri, "---\ntitle: x\n---\n\n# Section\n\nbody\n\n```go\ncode\n```\n")

	var ranges []lspprotocol.FoldingRange
	err := pair.client.Call(context.Background(), "textDocument/foldingRange",
		map[string]any{"textDocument": map[string]any{"uri": uri}}, &ranges)
	if err != nil {
		t.Fatal(err)
	}
	if len(ranges) != 3 {
		t.Fatalf("ranges = %+v, want 3 (frontmatter, section, code)", ranges)
	}
	if !reflect.DeepEqual(ranges[0], lspprotocol.FoldingRange{StartLine: 0, EndLine: 2, Kind: "comment", CollapsedText: "..."}) {
		t.Errorf("frontmatter range = %+v", ranges[0])
	}
	// The "# Section" run extends to end of document (line 11).
	if ranges[1].StartLine != 4 || ranges[1].EndLine != 11 {
		t.Errorf("section range = %+v", ranges[1])
	}
	if ranges[2].StartLine != 8 || ranges[2].EndLine != 10 {
		t.Errorf("code range = %+v", ranges[2])
	}
}

func TestDefinitionAnchor(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "doc.md")
	text := "# Alpha\n\nsee [link](#beta).\n\n## Beta\n\ncontent\n"
	pair.open(t, uri, text)

	// Position inside "beta" on line 2: "see [link](#beta)." — character 14.
	var loc []lspprotocol.Location
	err := pair.client.Call(context.Background(), "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": 2, "character": 14},
	}, &loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(loc) != 1 {
		t.Fatalf("locations = %+v, want one", loc)
	}
	if loc[0].Range.Start.Line != 4 {
		t.Fatalf("definition at %+v, want line 4 (## Beta)", loc[0].Range)
	}
}

func TestDefinitionAcrossFiles(t *testing.T) {
	root := writeTree(t, map[string]string{
		"notes/guide.md": "# Guide\n\n## Setup\n\nsteps\n",
		"index.md":       "start: [guide](notes/guide.md#setup)\n",
	})
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "index.md")
	pair.open(t, uri, "start: [guide](notes/guide.md#setup)\n")

	var loc []lspprotocol.Location
	err := pair.client.Call(context.Background(), "textDocument/definition", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": 0, "character": 20},
	}, &loc)
	if err != nil {
		t.Fatal(err)
	}
	if len(loc) != 1 {
		t.Fatalf("locations = %+v", loc)
	}
	if loc[0].URI != uriFor(root, "notes/guide.md") {
		t.Fatalf("target = %s", loc[0].URI)
	}
	if loc[0].Range.Start.Line != 2 {
		t.Fatalf("range = %+v, want line 2 (## Setup)", loc[0].Range)
	}
}

func TestReferencesFindsInboundLinks(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	main := uriFor(root, "main.md")
	other := uriFor(root, "other.md")
	pair.open(t, main, "# Setup\n\nbody\n")
	pair.open(t, other, "see [back](main.md#setup) and [top](#nothing)\n")

	var refs []lspprotocol.Location
	err := pair.client.Call(context.Background(), "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": main},
		"position":     map[string]any{"line": 0, "character": 3},
		"context":      map[string]any{"includeDeclaration": false},
	}, &refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].URI != other {
		t.Fatalf("refs = %+v, want one hit in other.md", refs)
	}
	if refs[0].Range.Start.Line != 0 {
		t.Fatalf("ref range = %+v", refs[0].Range)
	}
}

func TestRenameHeadingUpdatesInboundLinks(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	main := uriFor(root, "main.md")
	other := uriFor(root, "other.md")
	pair.open(t, main, "# Old Name\n\nbody\n")
	pair.open(t, other, "[a](main.md#old-name)\n[[main|Old Name]] — wait this is a wiki link to the file, not the heading\n")

	var edit lspprotocol.WorkspaceEdit
	err := pair.client.Call(context.Background(), "textDocument/rename", map[string]any{
		"textDocument": map[string]any{"uri": main},
		"position":     map[string]any{"line": 0, "character": 4},
		"newName":      "New Name",
	}, &edit)
	if err != nil {
		t.Fatal(err)
	}
	mainEdits := edit.Changes[main]
	if len(mainEdits) != 1 || mainEdits[0].NewText != "New Name" {
		t.Fatalf("main edits = %+v", mainEdits)
	}
	otherEdits := edit.Changes[other]
	if len(otherEdits) != 1 {
		t.Fatalf("other edits = %+v, want one anchor rewrite", otherEdits)
	}
	if otherEdits[0].NewText != "main.md#new-name" {
		t.Fatalf("anchor rewrite = %q", otherEdits[0].NewText)
	}
}

func TestPrepareRenameOnlyOnHeadings(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "doc.md")
	pair.open(t, uri, "# Title\n\nplain words\n")

	var rng *lspprotocol.Range
	_ = pair.client.Call(context.Background(), "textDocument/prepareRename", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": 0, "character": 3},
	}, &rng)
	if rng == nil {
		t.Fatal("prepareRename on heading returned nil")
	}
	_ = pair.client.Call(context.Background(), "textDocument/prepareRename", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": 2, "character": 2},
	}, &rng)
	if rng != nil {
		t.Fatalf("prepareRename on body text must be nil, got %+v", rng)
	}
}

func TestDocumentLink(t *testing.T) {
	root := writeTree(t, map[string]string{"guide.md": ""})
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "index.md")
	pair.open(t, uri, "[g](guide.md) [s](#top) [x](https://x.io)\n\n# top\n")

	var links []lspprotocol.DocumentLink
	err := pair.client.Call(context.Background(), "textDocument/documentLink",
		map[string]any{"textDocument": map[string]any{"uri": uri}}, &links)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 {
		t.Fatalf("links = %+v", links)
	}
	if links[0].Target != uriFor(root, "guide.md") {
		t.Errorf("file target = %s", links[0].Target)
	}
	if links[1].Target != "#top" {
		t.Errorf("anchor target = %s", links[1].Target)
	}
	if links[2].Target != "https://x.io" {
		t.Errorf("external target = %s", links[2].Target)
	}
}

func TestCompletionAnchors(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "doc.md")
	pair.open(t, uri, "# Alpha Section\n\n[](#al)\n")

	// Line 2 is "[](#al)" — the cursor right after "al" is character 6.
	var items []lspprotocol.CompletionItem
	err := pair.client.Call(context.Background(), "textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": 2, "character": 6},
	}, &items)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].InsertText != "alpha-section" {
		t.Fatalf("items = %+v", items)
	}
}

func TestCompletionWikiNames(t *testing.T) {
	root := writeTree(t, map[string]string{"notes/getting-started.md": "# Start\n"})
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "index.md")
	pair.open(t, uri, "[[get\n")

	var items []lspprotocol.CompletionItem
	err := pair.client.Call(context.Background(), "textDocument/completion", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": 0, "character": 5},
	}, &items)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Label != "getting-started" {
		t.Fatalf("items = %+v", items)
	}
}

func TestDiagnosticsBrokenAnchor(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "doc.md")
	notifs := pair.open(t, uri, "# Real\n\n[bad](#missing) [good](#real)\n")

	var last *publishDiagnosticsParams
	for i := range notifs {
		last = &notifs[i]
	}
	if last == nil {
		t.Fatal("no diagnostics published")
	}
	if len(last.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one", last.Diagnostics)
	}
	d := last.Diagnostics[0]
	if d.Range.Start.Line != 2 || d.Severity != lspprotocol.SeverityWarning {
		t.Fatalf("diag = %+v", d)
	}
	if d.Message != `no heading named "missing" in this document` {
		t.Fatalf("message = %q", d.Message)
	}
}

func TestDiagnosticsMissingFileAndFix(t *testing.T) {
	root := writeTree(t, map[string]string{"guide.md": "# Guide\n"})
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "index.md")

	// First open: one broken link to a file that does not exist.
	notifs := pair.open(t, uri, "[x](nope.md)\n")
	if len(notifs) == 0 || len(notifs[len(notifs)-1].Diagnostics) != 1 {
		t.Fatalf("expected one diagnostic, got %+v", notifs)
	}

	// Rewriting the text to a valid link clears the diagnostic. didChange is
	// a notification handled on its own goroutine, so it is not ordered
	// against the request below; poll until the actor has applied it.
	pair.client.OnNotify("textDocument/publishDiagnostics", func(json.RawMessage) {})
	_ = pair.client.Notify("textDocument/didChange", map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": 2},
		"contentChanges": []map[string]any{
			{"text": "[x](guide.md)\n"},
		},
	})
	var links []lspprotocol.DocumentLink
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := pair.client.Call(context.Background(), "textDocument/documentLink",
			map[string]any{"textDocument": map[string]any{"uri": uri}}, &links)
		if err != nil {
			t.Fatal(err)
		}
		if len(links) == 1 && links[0].Target == uriFor(root, "guide.md") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("links after change = %+v, want the rewritten guide.md target", links)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFormattingTrimsAndCollapses(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	uri := uriFor(root, "doc.md")
	pair.open(t, uri, "text \n\n\n\n\nmore\t\n")

	var edits []lspprotocol.TextEdit
	err := pair.client.Call(context.Background(), "textDocument/formatting", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"options":      map[string]any{"tabSize": 2, "insertSpaces": true},
	}, &edits)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 3 { // trim line 0, trim line 5, collapse blanks 2..4
		t.Fatalf("edits = %+v, want 3", edits)
	}
}

func TestWorkspaceSymbol(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a/one.md": "# Install\n",
		"b/two.md": "# Setup\n\n## Setup Steps\n",
	})
	pair := newTestPair(t)
	pair.initialize(t, root)

	var symbols []symbolInformation
	err := pair.client.Call(context.Background(), "workspace/symbol",
		map[string]any{"query": "setup"}, &symbols)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 2 {
		t.Fatalf("symbols = %+v, want 2", symbols)
	}
	if symbols[0].Location.URI != uriFor(root, "b/two.md") {
		t.Fatalf("first = %s", symbols[0].Location.URI)
	}
}

func TestUnknownMethodAnswersNull(t *testing.T) {
	root := t.TempDir()
	pair := newTestPair(t)
	pair.initialize(t, root)
	var out any
	if err := pair.client.Call(context.Background(), "textDocument/hover",
		map[string]any{"textDocument": map[string]any{"uri": "file:///x.md"}}, &out); err != nil {
		t.Fatalf("unknown method must not error: %v", err)
	}
}

func TestShutdownAndExit(t *testing.T) {
	pair := newTestPair(t)
	pair.initialize(t, t.TempDir())
	if err := pair.client.Call(context.Background(), "shutdown", nil, nil); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_ = pair.client.Notify("exit", nil)
	// exit is the lifecycle terminator: the server must stop on its own,
	// without the client closing the pipe. Waiting for that is the whole
	// point of the test — a non-blocking poll here passed whether or not
	// Serve ever returned.
	if !pair.waitForExit(10 * time.Second) {
		t.Fatal("Serve did not return after the exit notification")
	}
	if pair.serveErr != nil {
		t.Fatalf("Serve returned %v", pair.serveErr)
	}
}
