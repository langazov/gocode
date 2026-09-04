package chunk

import (
	"sort"

	"github.com/langazov/gocode-go/internal/lsp"
)

// maxDeclLinesMultiplier bounds a single symbol-derived chunk's size as a
// multiple of opts.Lines, so one huge generated function (or a symbol whose
// range the server reported too broadly) cannot produce an unembeddable
// chunk. A chunk that exceeds this is sub-split with the same sliding window
// the language-agnostic fallback uses.
const maxDeclLinesMultiplier = 4

// boundaryKinds are the LSP SymbolKind values worth splitting a file at on
// their own: container-like declarations, one retrieval unit each.
//
// Everything else — Variable, Constant, Field, Property, EnumMember, ... —
// is excluded on purpose. gopls (confirmed directly, not assumed) reports
// each const inside a `const (...)` block as its own top-level symbol, one
// per line. Treating those as boundaries too fragments a block into a
// stream of one-line, context-free chunks (a lone "ReplyReject Reply =
// \"reject\"" with no idea what block or type it belongs to). Leaving them
// out of this set means such a symbol is simply absorbed into whichever
// chunk surrounds it, the same as any other non-symbol content.
var boundaryKinds = map[int]bool{
	lsp.SymbolKindClass:       true,
	lsp.SymbolKindMethod:      true,
	lsp.SymbolKindConstructor: true,
	lsp.SymbolKindEnum:        true,
	lsp.SymbolKindInterface:   true,
	lsp.SymbolKindFunction:    true,
	lsp.SymbolKindStruct:      true,
}

// chunksFromSymbols splits a file at its top-level, container-kind symbol
// boundaries (see boundaryKinds), filling every gap so the whole file is
// covered exactly once:
//
//   - the first chunk starts at line 0, not the first symbol's start, so
//     package/import lines and any header comment stay attached to whatever
//     follows rather than being dropped;
//   - each subsequent chunk starts immediately after the previous one ends —
//     using the running end position, never a symbol's own declared start —
//     so blank lines, comments, and any code the server didn't report as a
//     top-level symbol land in the following chunk rather than nowhere, and
//     the result is gap-free and non-overlapping even if a server's line
//     numbers are slightly inconsistent (only the *end* of each symbol drives
//     a boundary; the start is only used to sort them);
//   - a final chunk covers anything after the last symbol.
//
// Only top-level symbols (symbols itself, not their Children) are used:
// nested members already sit inside their parent's range, so descending into
// them would either duplicate content or require a careful non-overlapping
// merge for no real benefit — a whole function or type is already the right
// retrieval granularity for a coding agent.
func chunksFromSymbols(relPath string, lines []string, symbols []lsp.DocumentSymbol, opts Options) []Chunk {
	type boundary struct{ start, end int } // 0-indexed inclusive

	bounds := make([]boundary, 0, len(symbols))
	for _, sym := range symbols {
		if !boundaryKinds[sym.Kind] {
			continue
		}
		start := clampLine(sym.Range.Start.Line, len(lines))
		end := clampLine(sym.Range.End.Line, len(lines))
		if end < start {
			end = start
		}
		bounds = append(bounds, boundary{start: start, end: end})
	}
	sort.Slice(bounds, func(i, j int) bool { return bounds[i].start < bounds[j].start })

	var out []Chunk
	prevEnd := -1 // 0-indexed inclusive end of the previous chunk; -1 = nothing emitted yet
	for _, b := range bounds {
		start := prevEnd + 1
		if b.end < start {
			// This symbol's reported end falls entirely inside a range
			// already emitted (overlapping/out-of-order symbols, which a
			// well-behaved server shouldn't send but this must not corrupt
			// coverage over): its content is already covered, skip it.
			continue
		}
		out = append(out, splitIfOversized(relPath, lines, start, b.end+1, opts)...)
		prevEnd = b.end
	}
	if prevEnd+1 < len(lines) {
		out = append(out, splitIfOversized(relPath, lines, prevEnd+1, len(lines), opts)...)
	}
	return out
}

func clampLine(line, total int) int {
	if total == 0 || line < 0 {
		return 0
	}
	if line >= total {
		return total - 1
	}
	return line
}

// splitIfOversized returns a single chunk for [start, end), or several from
// the sliding window if that range is too large to embed as one chunk.
func splitIfOversized(relPath string, lines []string, start, end int, opts Options) []Chunk {
	if end-start <= opts.Lines*maxDeclLinesMultiplier {
		return []Chunk{buildChunk(relPath, lines, start, end)}
	}
	return slidingWindowRange(relPath, lines, start, end, opts)
}

// slidingWindowRange is slidingWindow restricted to [rangeStart, rangeEnd),
// for sub-splitting one oversized symbol-derived chunk.
func slidingWindowRange(relPath string, lines []string, rangeStart, rangeEnd int, opts Options) []Chunk {
	step := opts.Lines - opts.Overlap
	var out []Chunk
	for start := rangeStart; start < rangeEnd; start += step {
		end := min(start+opts.Lines, rangeEnd)
		out = append(out, buildChunk(relPath, lines, start, end))
		if end == rangeEnd {
			break
		}
	}
	return out
}
