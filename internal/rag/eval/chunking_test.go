package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// TestBoundaryReportIntegrityRateZeroSymbols guards the division: a report
// with no scoreable symbols at all (every file skipped, or none walked)
// must read as 0, not NaN or a divide-by-zero panic.
func TestBoundaryReportIntegrityRateZeroSymbols(t *testing.T) {
	var r BoundaryReport
	if got := r.IntegrityRate(); got != 0 {
		t.Fatalf("IntegrityRate() on a zero-symbol report = %v, want 0", got)
	}
}

// TestBestContainmentPicksTheSingleBestChunkNotTheSum is bestContainment's
// own contract in isolation: when chunks overlap (the sliding window's
// Overlap option guarantees this happens in practice), the ratio is the
// largest single chunk's coverage, never the sum of several overlapping
// chunks double-counting the same lines.
func TestBestContainmentPicksTheSingleBestChunkNotTheSum(t *testing.T) {
	chunks := []chunk.Chunk{
		{Path: "a.go", StartLine: 1, EndLine: 6},  // covers 6 of 10 lines
		{Path: "a.go", StartLine: 4, EndLine: 10}, // covers 7 of 10 lines, overlapping the first
	}
	// Symbol spans 1-10. The best single chunk covers 7/10, not
	// (6+7)/10 = 1.3, which double-counts lines 4-6.
	if got := bestContainment(chunks, 1, 10); got != 0.7 {
		t.Fatalf("bestContainment = %v, want 0.7 (best single chunk, not the sum)", got)
	}
}

func TestBestContainmentNoChunkCoversTheSymbol(t *testing.T) {
	chunks := []chunk.Chunk{{Path: "a.go", StartLine: 100, EndLine: 200}}
	if got := bestContainment(chunks, 1, 10); got != 0 {
		t.Fatalf("bestContainment = %v, want 0 (no overlap at all)", got)
	}
}

func TestBestContainmentFullyContained(t *testing.T) {
	chunks := []chunk.Chunk{{Path: "a.go", StartLine: 1, EndLine: 60}}
	if got := bestContainment(chunks, 10, 20); got != 1.0 {
		t.Fatalf("bestContainment = %v, want 1.0 (symbol entirely inside one chunk)", got)
	}
}

func TestBestContainmentEmptyChunkList(t *testing.T) {
	if got := bestContainment(nil, 1, 10); got != 0 {
		t.Fatalf("bestContainment with no chunks at all = %v, want 0", got)
	}
}

// TestEvaluateChunkingIgnoresNonBoundaryKindSymbols pins the same rule
// chunk.BoundaryKinds documents for chunking itself: a Variable or Constant
// symbol (gopls reports each const in a `const (...)` block as its own
// top-level symbol) must never count toward Symbols/FullyContained, or a
// file full of one-line const declarations would swamp the score with
// trivially "fully contained" one-liners that say nothing about real
// function/class/method boundaries.
func TestEvaluateChunkingIgnoresNonBoundaryKindSymbols(t *testing.T) {
	root := t.TempDir()
	content := strings.Join([]string{
		"package main",  // 0
		"",              // 1
		"const X = 1",   // 2
		"",              // 3
		"func One() {",  // 4
		"    doStuff()", // 5
		"}",             // 6
	}, "\n")
	path := writeFile(t, root, "a.go", content)
	resolver := fakeResolver{symbols: map[string][]lsp.DocumentSymbol{
		path: {
			{Kind: lsp.SymbolKindConstant, Range: lsp.Range{Start: lsp.Position{Line: 2}, End: lsp.Position{Line: 2}}},
			{Kind: lsp.SymbolKindFunction, Range: lsp.Range{Start: lsp.Position{Line: 4}, End: lsp.Position{Line: 6}}},
		},
	}}

	report, err := EvaluateChunking(context.Background(), root, resolver, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Symbols != 1 {
		t.Fatalf("Symbols = %d, want 1 (only the function counts, not the const)", report.Symbols)
	}
}

// TestEvaluateChunkingSkipsSymbolWithReversedRange guards against a
// malformed (or off-by-one-bug) LSP range where End comes before Start: it
// must be skipped, not counted as a symbol with a nonsensical negative
// length.
func TestEvaluateChunkingSkipsSymbolWithReversedRange(t *testing.T) {
	root := t.TempDir()
	path := writeFile(t, root, "a.go", "package main\n\nfunc One() {}\n")
	resolver := fakeResolver{symbols: map[string][]lsp.DocumentSymbol{
		path: {
			{Kind: lsp.SymbolKindFunction, Range: lsp.Range{Start: lsp.Position{Line: 5}, End: lsp.Position{Line: 2}}},
		},
	}}

	report, err := EvaluateChunking(context.Background(), root, resolver, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Symbols != 0 {
		t.Fatalf("Symbols = %d, want 0 (a reversed range is not a real symbol)", report.Symbols)
	}
}

// TestEvaluateChunkingAggregatesMultipleFilesCorrectly guards the
// concurrent per-file fan-out (see EvaluateChunking's worker pool): each
// file's symbols must land against that same file's own chunks, not get
// crossed with another file's under concurrency.
func TestEvaluateChunkingAggregatesMultipleFilesCorrectly(t *testing.T) {
	root := t.TempDir()
	pathA := writeFile(t, root, "a.go", strings.Join([]string{
		"package main", "", "func One() {", "    a()", "}",
	}, "\n"))
	pathB := writeFile(t, root, "b.go", strings.Join([]string{
		"package main", "", "func Two() {", "    b()", "}", "", "func Three() {", "    c()", "}",
	}, "\n"))
	resolver := fakeResolver{symbols: map[string][]lsp.DocumentSymbol{
		pathA: {sym(2, 4)},
		pathB: {sym(2, 4), sym(6, 8)},
	}}

	report, err := EvaluateChunking(context.Background(), root, resolver, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != 2 {
		t.Fatalf("Files = %d, want 2", report.Files)
	}
	if report.Symbols != 3 {
		t.Fatalf("Symbols = %d, want 3 (1 from a.go, 2 from b.go)", report.Symbols)
	}
	// The default sliding window (60-line chunks) fits either whole file in
	// a single chunk, so every symbol should be fully contained regardless
	// of which file it came from — a mixup would still likely pass this,
	// but a wrong Files/Symbols count above already would have caught it.
	if report.FullyContained != 3 {
		t.Fatalf("FullyContained = %d, want 3", report.FullyContained)
	}
}

// erroringResolver fails for any file whose name is in errFiles, succeeding
// normally otherwise — for testing that EvaluateChunking's concurrent
// worker pool tolerates a per-file failure without losing the other files'
// results or hanging.
type erroringResolver struct {
	symbols  map[string][]lsp.DocumentSymbol
	errFiles map[string]bool
}

func (r erroringResolver) DocumentSymbols(_ context.Context, file string) ([]lsp.DocumentSymbol, error) {
	if r.errFiles[file] {
		return nil, errors.New("resolver: simulated failure")
	}
	return r.symbols[file], nil
}

func TestEvaluateChunkingContinuesWhenOneFileResolverErrors(t *testing.T) {
	root := t.TempDir()
	pathOK := writeFile(t, root, "ok.go", strings.Join([]string{
		"package main", "", "func One() {", "    a()", "}",
	}, "\n"))
	pathBad := writeFile(t, root, "bad.go", strings.Join([]string{
		"package main", "", "func Two() {", "    b()", "}",
	}, "\n"))
	resolver := erroringResolver{
		symbols:  map[string][]lsp.DocumentSymbol{pathOK: {sym(2, 4)}, pathBad: {sym(2, 4)}},
		errFiles: map[string]bool{pathBad: true},
	}

	report, err := EvaluateChunking(context.Background(), root, resolver, chunk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != 1 || report.Symbols != 1 {
		t.Fatalf("expected only ok.go to be scored, got %+v", report)
	}
}

// slowConcurrentResolver simulates a real LSP server's per-request latency,
// tracking how many DocumentSymbols calls overlap in time, to verify
// EvaluateChunking's own ground-truth lookups (separate from whatever
// chunk.Walk's pass did) run concurrently rather than one file at a time.
type slowConcurrentResolver struct {
	mu                sync.Mutex
	inFlight, maxSeen int
	delay             time.Duration
	symbol            lsp.DocumentSymbol
}

func (r *slowConcurrentResolver) DocumentSymbols(context.Context, string) ([]lsp.DocumentSymbol, error) {
	r.mu.Lock()
	r.inFlight++
	if r.inFlight > r.maxSeen {
		r.maxSeen = r.inFlight
	}
	r.mu.Unlock()

	time.Sleep(r.delay)

	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()

	return []lsp.DocumentSymbol{r.symbol}, nil
}

func TestEvaluateChunkingFetchesGroundTruthConcurrently(t *testing.T) {
	root := t.TempDir()
	const numFiles = 16
	for i := 0; i < numFiles; i++ {
		writeFile(t, root, fmt.Sprintf("f%d.go", i), "package main\n\nfunc Fn() {}\n")
	}
	resolver := &slowConcurrentResolver{delay: 20 * time.Millisecond, symbol: sym(2, 2)}

	start := time.Now()
	report, err := EvaluateChunking(context.Background(), root, resolver, chunk.Options{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if report.Files != numFiles {
		t.Fatalf("got %d files scored, want %d", report.Files, numFiles)
	}
	resolver.mu.Lock()
	maxSeen := resolver.maxSeen
	resolver.mu.Unlock()
	if maxSeen < 2 {
		t.Fatalf("DocumentSymbols calls never overlapped (max concurrent = %d): ground-truth lookup is not concurrent", maxSeen)
	}
	if elapsed >= numFiles*20*time.Millisecond {
		t.Fatalf("EvaluateChunking took %v, no faster than fetching ground truth for %d files one at a time", elapsed, numFiles)
	}
}
