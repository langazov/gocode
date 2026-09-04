package chunk

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/lsp"
)

// fakeResolver is a hand-written SymbolResolver: no spawned process, no real
// language server, just canned symbols keyed by file path. This is exactly
// why SymbolResolver is a narrow interface rather than a *lsp.Service
// dependency — this package's own tests never need to spawn anything.
type fakeResolver struct {
	symbols map[string][]lsp.DocumentSymbol
	err     error
}

func (f *fakeResolver) DocumentSymbols(ctx context.Context, file string) ([]lsp.DocumentSymbol, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.symbols[file], nil
}

// sym builds a Function-kind symbol, the common case in these tests. Use
// symKind directly for a test that cares about a specific SymbolKind.
func sym(name string, startLine, endLine int) lsp.DocumentSymbol {
	return symKind(name, lsp.SymbolKindFunction, startLine, endLine)
}

func symKind(name string, kind, startLine, endLine int) lsp.DocumentSymbol {
	return lsp.DocumentSymbol{
		Name:  name,
		Kind:  kind,
		Range: lsp.Range{Start: lsp.Position{Line: startLine}, End: lsp.Position{Line: endLine}},
	}
}

func TestWalkUsesSymbolBoundariesWhenAvailable(t *testing.T) {
	root := t.TempDir()
	content := strings.Join([]string{
		"package main",       // 0
		"",                   // 1
		"func One() {",       // 2
		"    doStuff()",      // 3
		"}",                  // 4
		"",                   // 5
		"func Two() {",       // 6
		"    doOtherStuff()", // 7
		"}",                  // 8
	}, "\n")
	writeFile(t, root, "a.go", content)
	path := filepath.Join(root, "a.go")

	resolver := &fakeResolver{symbols: map[string][]lsp.DocumentSymbol{
		path: {sym("One", 2, 4), sym("Two", 6, 8)},
	}}

	chunks, err := Walk(context.Background(), root, Options{LSP: resolver})
	if err != nil {
		t.Fatal(err)
	}
	// Gap-filling: chunk 1 absorbs the header (lines 0-4, "package main"
	// through One's closing brace); chunk 2 covers the blank line + Two
	// (lines 5-8).
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2: %+v", len(chunks), chunks)
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 5 {
		t.Errorf("chunk 0: got [%d,%d], want [1,5]", chunks[0].StartLine, chunks[0].EndLine)
	}
	if !strings.Contains(chunks[0].Content, "package main") || !strings.Contains(chunks[0].Content, "func One") {
		t.Errorf("chunk 0 should contain the header and One(): %q", chunks[0].Content)
	}
	if chunks[1].StartLine != 6 || chunks[1].EndLine != 9 {
		t.Errorf("chunk 1: got [%d,%d], want [6,9]", chunks[1].StartLine, chunks[1].EndLine)
	}
	if !strings.Contains(chunks[1].Content, "func Two") {
		t.Errorf("chunk 1 should contain Two(): %q", chunks[1].Content)
	}
}

func TestWalkFallsBackToSlidingWindowWhenNoSymbols(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", strings.Repeat("line\n", 5))

	resolver := &fakeResolver{symbols: map[string][]lsp.DocumentSymbol{}} // no entry for this file -> nil

	chunks, err := Walk(context.Background(), root, Options{LSP: resolver, Lines: 3, Overlap: 1})
	if err != nil {
		t.Fatal(err)
	}
	// step=2 over 5 lines: [1,3] [3,5]
	if len(chunks) != 2 || chunks[0].StartLine != 1 || chunks[1].StartLine != 3 {
		t.Fatalf("expected the plain sliding window, got %+v", chunks)
	}
}

func TestWalkFallsBackToSlidingWindowOnResolverError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", strings.Repeat("line\n", 5))

	resolver := &fakeResolver{err: context.DeadlineExceeded}

	chunks, err := Walk(context.Background(), root, Options{LSP: resolver, Lines: 3, Overlap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected the sliding-window fallback despite the resolver error, got %+v", chunks)
	}
}

func TestWalkWithNilLSPUnchanged(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", strings.Repeat("line\n", 5))

	chunks, err := Walk(context.Background(), root, Options{Lines: 3, Overlap: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %+v", chunks)
	}
}

func TestChunksFromSymbolsSplitsOversizedSymbol(t *testing.T) {
	// A single symbol spanning way more than opts.Lines*maxDeclLinesMultiplier
	// must still be sub-split, not embedded as one giant chunk.
	total := 50
	lines := make([]string, total)
	for i := range lines {
		lines[i] = "line"
	}
	symbols := []lsp.DocumentSymbol{sym("Huge", 0, total-1)}
	opts := Options{Lines: 5, Overlap: 1}.withDefaults()

	chunks := chunksFromSymbols("big.go", lines, symbols, opts)
	if len(chunks) < 2 {
		t.Fatalf("expected the oversized symbol to be sub-split, got %d chunk(s)", len(chunks))
	}
	// Coverage must still be exact: first chunk starts at line 1, last ends at total.
	if chunks[0].StartLine != 1 {
		t.Errorf("first chunk starts at %d, want 1", chunks[0].StartLine)
	}
	if chunks[len(chunks)-1].EndLine != total {
		t.Errorf("last chunk ends at %d, want %d", chunks[len(chunks)-1].EndLine, total)
	}
}

func TestChunksFromSymbolsCoversWholeFileWithNoGapsOrOverlaps(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "line"
	}
	// Deliberately out of order and with a gap between symbols, to exercise
	// sorting and gap-filling together.
	symbols := []lsp.DocumentSymbol{sym("B", 10, 14), sym("A", 2, 5)}
	opts := Options{}.withDefaults()

	chunks := chunksFromSymbols("f.go", lines, symbols, opts)
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	if chunks[0].StartLine != 1 {
		t.Errorf("first chunk should start at line 1 (absorbing the header), got %d", chunks[0].StartLine)
	}
	// Verify no gaps/overlaps: each chunk's start is exactly the previous
	// chunk's end + 1.
	for i := 1; i < len(chunks); i++ {
		if chunks[i].StartLine != chunks[i-1].EndLine+1 {
			t.Errorf("gap or overlap between chunk %d (end %d) and chunk %d (start %d)",
				i-1, chunks[i-1].EndLine, i, chunks[i].StartLine)
		}
	}
	if chunks[len(chunks)-1].EndLine != len(lines) {
		t.Errorf("last chunk should reach EOF (%d), got %d", len(lines), chunks[len(chunks)-1].EndLine)
	}
}

// TestChunksFromSymbolsDoesNotFragmentConstBlock reproduces a real gopls
// behavior found by indexing this project's own internal/permission/
// permission.go with a live gopls: it reports every individual const inside
// a `const (...)` block as its own top-level symbol (one per line), not the
// block as a whole. Before boundaryKinds filtered by SymbolKind, that turned
// each middle-of-block const into an isolated, context-free one-line chunk —
// e.g. a lone `ReplyReject Reply = "reject"` with no idea what block or type
// it belongs to. Constants must stay absorbed into the surrounding chunk.
func TestChunksFromSymbolsDoesNotFragmentConstBlock(t *testing.T) {
	lines := []string{
		"type Reply string",         // 0
		"",                          // 1
		"const (",                   // 2
		`    ReplyOnce = "a"`,       // 3
		`    ReplyMore = "b"`,       // 4
		`    ReplyLast = "c"`,       // 5
		")",                         // 6
		"",                          // 7
		"type AssertInput struct {", // 8
		"    ID string",             // 9
		"}",                         // 10
	}
	symbols := []lsp.DocumentSymbol{
		symKind("Reply", lsp.SymbolKindClass, 0, 0),
		symKind("ReplyOnce", lsp.SymbolKindConstant, 3, 3),
		symKind("ReplyMore", lsp.SymbolKindConstant, 4, 4),
		symKind("ReplyLast", lsp.SymbolKindConstant, 5, 5),
		symKind("AssertInput", lsp.SymbolKindStruct, 8, 10),
	}
	opts := Options{}.withDefaults()

	chunks := chunksFromSymbols("f.go", lines, symbols, opts)
	for _, c := range chunks {
		if c.StartLine == c.EndLine && strings.Contains(c.Content, "Reply") && !strings.Contains(c.Content, "type") {
			t.Errorf("got an isolated const-only chunk, want the const block absorbed into a neighbor: %+v", c)
		}
	}
	// AssertInput must still be its own clean chunk, unaffected by the fix.
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Content, "type AssertInput struct") && strings.Contains(c.Content, "ID string") {
			found = true
		}
	}
	if !found {
		t.Errorf("AssertInput should still be a complete chunk, got %+v", chunks)
	}
}
