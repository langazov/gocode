package eval

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/lsp"
	"github.com/langazov/gocode-go/internal/rag/chunk"
)

// fakeResolver is a hand-written chunk.SymbolResolver, mirroring the one
// chunk/syntax_test.go uses: canned symbols keyed by absolute file path, no
// spawned language server.
type fakeResolver struct {
	symbols map[string][]lsp.DocumentSymbol
}

func (f fakeResolver) DocumentSymbols(_ context.Context, file string) ([]lsp.DocumentSymbol, error) {
	return f.symbols[file], nil
}

func sym(startLine, endLine int) lsp.DocumentSymbol {
	return lsp.DocumentSymbol{
		Kind:  lsp.SymbolKindFunction,
		Range: lsp.Range{Start: lsp.Position{Line: startLine}, End: lsp.Position{Line: endLine}},
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEvaluateChunkingSlidingWindowSplitsASymbol(t *testing.T) {
	root := t.TempDir()
	// 0-based lines: One spans 2-6 (5 lines), a 4-line sliding window with no
	// overlap will not fit it in a single chunk.
	content := strings.Join([]string{
		"package main", // 0
		"",             // 1
		"func One() {", // 2
		"    a()",      // 3
		"    b()",      // 4
		"    c()",      // 5
		"}",            // 6
	}, "\n")
	path := writeFile(t, root, "a.go", content)
	resolver := fakeResolver{symbols: map[string][]lsp.DocumentSymbol{path: {sym(2, 6)}}}

	report, err := EvaluateChunking(context.Background(), root, resolver, chunk.Options{Lines: 4, Overlap: 0})
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != 1 || report.Symbols != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.FullyContained != 0 {
		t.Fatalf("a 5-line symbol in 4-line non-overlapping chunks must be split, got FullyContained=%d", report.FullyContained)
	}
	if got := report.IntegrityRate(); got != 0 {
		t.Fatalf("integrity rate = %v, want 0", got)
	}
	if len(report.ContainmentRatios) != 1 || report.ContainmentRatios[0] >= 1.0 {
		t.Fatalf("containment ratio should be < 1.0, got %v", report.ContainmentRatios)
	}
}

func TestEvaluateChunkingSyntaxAwareContainsSymbol(t *testing.T) {
	root := t.TempDir()
	content := strings.Join([]string{
		"package main", // 0
		"",             // 1
		"func One() {", // 2
		"    a()",      // 3
		"    b()",      // 4
		"    c()",      // 5
		"}",            // 6
	}, "\n")
	path := writeFile(t, root, "a.go", content)
	resolver := fakeResolver{symbols: map[string][]lsp.DocumentSymbol{path: {sym(2, 6)}}}

	report, err := EvaluateChunking(context.Background(), root, resolver, chunk.Options{Lines: 4, Overlap: 0, LSP: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if report.IntegrityRate() != 1.0 {
		t.Fatalf("syntax-aware splitting should fully contain every symbol by construction, got integrity=%v", report.IntegrityRate())
	}
}

func TestEvaluateChunkingRejectsNilResolver(t *testing.T) {
	if _, err := EvaluateChunking(context.Background(), t.TempDir(), nil, chunk.Options{}); err == nil {
		t.Fatal("expected an error for a nil resolver")
	}
}

func TestEvaluateChunkingSkipsFilesWithNoSymbols(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package main\n")
	report, err := EvaluateChunking(context.Background(), root, fakeResolver{}, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != 0 || report.Symbols != 0 {
		t.Fatalf("a file with no reported symbols should not count as scored, got %+v", report)
	}
}
