package eval

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/langazov/gocode-go/internal/lsp"
	"github.com/langazov/gocode-go/internal/rag/chunk"
)

// symbolFetchConcurrency bounds how many ground-truth DocumentSymbols calls
// run at once, mirroring chunk.walkConcurrency's reasoning: this loop calls
// the resolver once per file regardless of whether chunk.Walk's own pass
// already did (it must, to score the plain sliding window, which never
// touches the resolver at all), so a large project pays this serial,
// per-file LSP round trip twice over — once per BoundaryReport requested.
const symbolFetchConcurrency = 8

// BoundaryReport summarizes how well one chunking pass respects real
// function/class/method boundaries, judged against an LSP resolver's symbol
// outline for each file — the same ground truth chunk.Walk's own
// syntax-aware mode (chunk/syntax.go) is built on.
type BoundaryReport struct {
	Files   int
	Symbols int
	// FullyContained is how many symbols land entirely inside one chunk.
	FullyContained int
	// ContainmentRatios holds, per symbol, the fraction of its lines that
	// fall inside whichever single chunk covers the most of it — 1.0 means
	// fully contained. Sorted ascending so Percentile can binary-search it.
	ContainmentRatios []float64
}

// IntegrityRate is the fraction of symbols fully contained in a single
// chunk — the headline number: 1.0 means chunking never cuts a
// function/class/method in half.
func (r BoundaryReport) IntegrityRate() float64 {
	if r.Symbols == 0 {
		return 0
	}
	return float64(r.FullyContained) / float64(r.Symbols)
}

// Percentile returns the p-th percentile (p in [0,1]) containment ratio,
// e.g. Percentile(0.5) for the median symbol, Percentile(0.1) for how bad
// the worst-affected tenth of symbols get split.
func (r BoundaryReport) Percentile(p float64) float64 {
	return percentile(r.ContainmentRatios, p)
}

// EvaluateChunking walks root exactly as chunk.Walk would with opts, then
// scores the resulting chunks against every file's real symbol boundaries
// as resolver reports them.
//
// resolver supplies ground truth independently of opts.LSP, which is what
// makes an apples-to-apples comparison possible: call this once with
// opts.LSP nil to measure the plain sliding window's boundary-violation
// rate, and again with opts.LSP set to resolver to measure syntax-aware
// splitting — both scored against the same symbols. (The syntax-aware pass
// should come back near 1.0 by construction; running it anyway is a sanity
// check on that construction, not redundant.)
func EvaluateChunking(ctx context.Context, root string, resolver chunk.SymbolResolver, opts chunk.Options) (BoundaryReport, error) {
	if resolver == nil {
		return BoundaryReport{}, fmt.Errorf("eval: EvaluateChunking needs a non-nil resolver for ground truth")
	}

	chunks, err := chunk.Walk(ctx, root, opts)
	if err != nil {
		return BoundaryReport{}, fmt.Errorf("eval: walk %s: %w", root, err)
	}
	byPath := make(map[string][]chunk.Chunk, len(chunks))
	for _, c := range chunks {
		byPath[c.Path] = append(byPath[c.Path], c)
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}

	// One DocumentSymbols round trip per file, fanned out the same way
	// chunk.Walk fans out its own resolver calls: independent per file, no
	// ordering dependency (results are only ever aggregated by path below).
	type groundTruth struct {
		symbols []lsp.DocumentSymbol
		ok      bool
	}
	results := make([]groundTruth, len(paths))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(symbolFetchConcurrency, len(paths)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				abs := filepath.Join(root, filepath.FromSlash(paths[i]))
				if symbols, err := resolver.DocumentSymbols(ctx, abs); err == nil && len(symbols) > 0 {
					results[i] = groundTruth{symbols: symbols, ok: true}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i := range paths {
			jobs <- i
		}
	}()
	wg.Wait()

	var report BoundaryReport
	for i, path := range paths {
		res := results[i]
		if !res.ok {
			continue // no ground truth for this file (language unsupported, no symbols): not scoreable, not a failure
		}
		report.Files++
		fileChunks := byPath[path]
		for _, sym := range res.symbols {
			if !chunk.BoundaryKinds[sym.Kind] {
				continue
			}
			// LSP ranges are 0-based; chunk.Chunk.StartLine/EndLine are
			// 1-based (see chunk.go's Chunk doc).
			start, end := sym.Range.Start.Line+1, sym.Range.End.Line+1
			if end < start {
				continue
			}
			report.Symbols++
			ratio := bestContainment(fileChunks, start, end)
			report.ContainmentRatios = append(report.ContainmentRatios, ratio)
			if ratio >= 1.0 {
				report.FullyContained++
			}
		}
	}
	sort.Float64s(report.ContainmentRatios)
	return report, nil
}

// bestContainment returns, among chunks, the largest fraction of the
// 1-based inclusive range [start,end] that a single chunk covers.
func bestContainment(chunks []chunk.Chunk, start, end int) float64 {
	total := end - start + 1
	if total <= 0 {
		return 1
	}
	best := 0
	for _, c := range chunks {
		ovStart, ovEnd := max(start, c.StartLine), min(end, c.EndLine)
		if ovEnd < ovStart {
			continue
		}
		if n := ovEnd - ovStart + 1; n > best {
			best = n
		}
	}
	return float64(best) / float64(total)
}
