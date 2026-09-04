package mdlsp

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/langazov/gocode-go/internal/lspprotocol"
)

// formattingParams is textDocument/formatting's shape.
type formattingParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Options struct {
		TabSize      int  `json:"tabSize"`
		InsertSpaces bool `json:"insertSpaces"`
	} `json:"options"`
}

// formatting answers textDocument/formatting with whitespace-normalizing
// edits: trailing whitespace trimmed, runs of 3+ blank lines collapsed to
// two, exactly one trailing newline at end of file. Nothing else is touched —
// markdown formatting opinions are a trap.
func (s *Server) formatting(ctx context.Context, raw json.RawMessage) (any, error) {
	var p formattingParams
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		return formatEdits(doc.doc)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// formatEdits diffs the formatted text against the original, producing whole-
// line edits. Lines are 1:1 between old and new, so each changed line gets a
// minimal single-line edit and untouched lines stay untouched.
func formatEdits(doc *mddocDoc) []lspprotocol.TextEdit {
	text := doc.Text
	lines := strings.Split(text, "\n")

	var out []lspprotocol.TextEdit
	for i, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed != line {
			out = append(out, lspprotocol.TextEdit{
				Range: lspprotocol.Range{
					Start: lspprotocol.Position{Line: i, Character: len([]rune(trimmed))},
					End:   lspprotocol.Position{Line: i, Character: lineUnits(line)},
				},
				NewText: "",
			})
		}
	}

	// Blank-line runs and the trailing newline are structural: collapse a
	// run of 3+ blanks by deleting the extra lines.
	blanks := 0
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			if blanks == 0 {
				start = i
			}
			blanks++
			continue
		}
		out = append(out, collapseEdits(start, blanks)...)
		blanks, start = 0, -1
	}
	out = append(out, collapseEdits(start, blanks)...)

	// Exactly one final newline: if the last line is non-empty, append.
	last := len(lines) - 1
	if last >= 0 && lines[last] != "" {
		out = append(out, lspprotocol.TextEdit{
			Range: lspprotocol.Range{
				Start: lspprotocol.Position{Line: last, Character: lineUnits(lines[last])},
				End:   lspprotocol.Position{Line: last, Character: lineUnits(lines[last])},
			},
			NewText: "\n",
		})
	}
	if out == nil {
		return []lspprotocol.TextEdit{}
	}
	return out
}

// collapseEdits produces deletions for blank-line runs longer than two.
func collapseEdits(start, count int) []lspprotocol.TextEdit {
	if count <= 2 {
		return nil
	}
	return []lspprotocol.TextEdit{{
		Range: lspprotocol.Range{
			Start: lspprotocol.Position{Line: start + 2, Character: 0},
			End:   lspprotocol.Position{Line: start + count, Character: 0},
		},
		NewText: "",
	}}
}

// lineUnits measures a line's length in UTF-16 code units.
func lineUnits(line string) int {
	units := 0
	for _, r := range line {
		if r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// workspaceSymbolParams is workspace/symbol's shape.
type workspaceSymbolParams struct {
	Query string `json:"query"`
}

// workspaceSymbol answers workspace/symbol with headings across the
// workspace whose text contains the query, case-insensitively. The result is
// the flat SymbolInformation shape, which the spec allows for workspace-wide
// queries.
func (s *Server) workspaceSymbol(ctx context.Context, raw json.RawMessage) (any, error) {
	var p workspaceSymbolParams
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		return st.workspaceSymbols(p.Query)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (st *state) workspaceSymbols(query string) []symbolInformation {
	st.ensureIndex()
	if st.index == nil {
		return []symbolInformation{}
	}
	needle := strings.ToLower(query)
	limit := 200 // keep payloads sane on big workspaces
	out := []symbolInformation{}
	for rel := range st.index {
		parsed := st.parseWorkspace(rel)
		if parsed == nil {
			continue
		}
		for _, h := range parsed.Headings {
			name := plainHeadingName(h.Text)
			if needle != "" && !strings.Contains(strings.ToLower(name), needle) {
				continue
			}
			line, char := parsed.OffsetPosition(h.StartByte)
			out = append(out, symbolInformation{
				Name: name,
				Kind: lspprotocol.SymbolKindField,
				Location: lspprotocol.Location{
					URI:   st.docURI(rel),
					Range: lspprotocol.Range{Start: lspprotocol.Position{Line: line, Character: char}},
				},
				ContainerName: "",
			})
			if len(out) >= limit {
				return out
			}
		}
	}
	return out
}

// symbolInformation is the flat, workspace-wide symbol shape.
type symbolInformation struct {
	Name          string               `json:"name"`
	Kind          int                  `json:"kind"`
	Location      lspprotocol.Location `json:"location"`
	ContainerName string               `json:"containerName,omitempty"`
}
