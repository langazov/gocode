package mdlsp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/lspprotocol"
	"github.com/langazov/gocode-go/internal/mddoc"
)

// mddocDoc is the parsed document model this package works against.
type mddocDoc = mddoc.Doc

// requestPositional is the textDocument-only request shape.
type requestPositional struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
}

// positionParams carries URI + position requests (definition, completion…).
type positionParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position lspprotocol.Position `json:"position"`
}

// symbolNode pairs a symbol with its heading level while nesting.
type symbolNode struct {
	sym   *lspprotocol.DocumentSymbol
	level int
}

// documentSymbol answers textDocument/documentSymbol with the heading tree
// nested by level.
func (s *Server) documentSymbol(ctx context.Context, raw json.RawMessage) (any, error) {
	var p requestPositional
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		return headingSymbols(doc.doc)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// headingSymbols builds the hierarchical symbol list for one parsed document.
func headingSymbols(doc *mddocDoc) []lspprotocol.DocumentSymbol {
	var roots []lspprotocol.DocumentSymbol
	var stack []symbolNode

	for _, h := range doc.Headings {
		start := toPosition(doc, h.StartByte)
		end := toPosition(doc, h.EndByte)
		sym := &lspprotocol.DocumentSymbol{
			Name:           plainHeadingName(h.Text),
			Kind:           lspprotocol.SymbolKindField,
			Range:          lspprotocol.Range{Start: start, End: end},
			SelectionRange: lspprotocol.Range{Start: start, End: end},
		}
		for len(stack) > 0 && stack[len(stack)-1].level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		if len(stack) == 0 {
			roots = append(roots, *sym)
			stack = append(stack, symbolNode{sym: &roots[len(roots)-1], level: h.Level})
		} else {
			parent := stack[len(stack)-1].sym
			parent.Children = append(parent.Children, *sym)
			stack = append(stack, symbolNode{sym: &parent.Children[len(parent.Children)-1], level: h.Level})
		}
	}
	if roots == nil {
		roots = []lspprotocol.DocumentSymbol{}
	}
	return roots
}

// foldingRange answers textDocument/foldingRange: one range per heading
// section, per code block, and for the frontmatter block.
func (s *Server) foldingRange(ctx context.Context, raw json.RawMessage) (any, error) {
	var p requestPositional
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		return foldingRanges(doc.doc)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// foldingRanges lists the foldable spans of one parsed document. A heading's
// section ends where the next heading of equal or higher level begins.
func foldingRanges(doc *mddocDoc) []lspprotocol.FoldingRange {
	var out []lspprotocol.FoldingRange
	lastLine := doc.LineCount() - 1

	for i, h := range doc.Headings {
		endLine := lastLine
		for j := i + 1; j < len(doc.Headings); j++ {
			if doc.Headings[j].Level <= h.Level {
				endLine = doc.LineOfByte(doc.Headings[j].StartByte) - 1
				break
			}
		}
		startLine := doc.LineOfByte(h.StartByte)
		if endLine > startLine {
			out = append(out, lspprotocol.FoldingRange{StartLine: startLine, EndLine: endLine})
		}
	}
	for _, span := range doc.CodeSpans {
		if span.End-1 > span.Start {
			kind := lspprotocol.FoldingRangeRegion
			out = append(out, lspprotocol.FoldingRange{
				StartLine: span.Start,
				EndLine:   span.End - 1,
				Kind:      kind,
			})
		}
	}
	if fm := doc.Frontmatter; fm.Has {
		out = append(out, lspprotocol.FoldingRange{
			StartLine:     fm.StartLine,
			EndLine:       fm.EndLine,
			Kind:          lspprotocol.FoldingRangeComment,
			CollapsedText: "...",
		})
	}
	if out == nil {
		out = []lspprotocol.FoldingRange{}
	}
	// The spec does not require ordering, but a stable document-ordered list
	// is what every consumer wants.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartLine != out[j].StartLine {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].EndLine < out[j].EndLine
	})
	return out
}

// toPosition converts a byte offset through the document's line index.
func toPosition(doc *mddocDoc, offset int) lspprotocol.Position {
	line, char := doc.OffsetPosition(offset)
	return lspprotocol.Position{Line: line, Character: char}
}

// plainHeadingName strips inline markup so the outline reads like rendered
// text: "Getting **Started**" lists as "Getting Started".
func plainHeadingName(s string) string {
	if !strings.ContainsAny(s, "*_`~[]!") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '*', '_', '`', '~', '[', ']', '!':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
