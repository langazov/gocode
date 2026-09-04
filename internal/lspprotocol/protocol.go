// Package lspprotocol holds the Language Server Protocol wire types shared by
// the language-server client in internal/lsp and the markdown language server
// in internal/mdlsp. It carries only data and small pure helpers — no I/O, no
// state — so both sides can depend on it without a cycle.
package lspprotocol

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// TextDocumentSyncKind values from the LSP spec. Full means the client sends
// the whole document text on every change.
const (
	SyncKindNone        = 0
	SyncKindFull        = 1
	SyncKindIncremental = 2
)

// Position is a zero-based line/character pair. Character counts UTF-16 code
// units, per the spec.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Location is a range inside a document, identified by URI.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// DocumentSymbol is one entry of a textDocument/documentSymbol response,
// normalized to the hierarchical shape the spec defines. A server that
// answers with the older, flat SymbolInformation shape instead (name, kind,
// location) is converted to this on decode — see decodeDocumentSymbols in
// internal/lsp/client.go — so callers only ever see one shape.
type DocumentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// SymbolKind values from the LSP spec (3.17), the full enum. DocumentSymbol.Kind
// holds one of these.
const (
	SymbolKindFile          = 1
	SymbolKindModule        = 2
	SymbolKindNamespace     = 3
	SymbolKindPackage       = 4
	SymbolKindClass         = 5
	SymbolKindMethod        = 6
	SymbolKindProperty      = 7
	SymbolKindField         = 8
	SymbolKindConstructor   = 9
	SymbolKindEnum          = 10
	SymbolKindInterface     = 11
	SymbolKindFunction      = 12
	SymbolKindVariable      = 13
	SymbolKindConstant      = 14
	SymbolKindString        = 15
	SymbolKindNumber        = 16
	SymbolKindBoolean       = 17
	SymbolKindArray         = 18
	SymbolKindObject        = 19
	SymbolKindKey           = 20
	SymbolKindNull          = 21
	SymbolKindEnumMember    = 22
	SymbolKindStruct        = 23
	SymbolKindEvent         = 24
	SymbolKindOperator      = 25
	SymbolKindTypeParameter = 26
)

// CompletionItemKind values from the LSP spec (3.17), the subset a markdown
// server needs.
const (
	CompletionKindText   = 1
	CompletionKindMethod = 2
	CompletionKindFile   = 17
	CompletionKindRef    = 18
	CompletionKindFolder = 19
	CompletionKindStruct = 22
	CompletionKindKey    = 24
)

// InsertTextFormat values from the LSP spec.
const (
	InsertTextFormatPlainText = 1
	InsertTextFormatSnippet   = 2
)

// CompletionItem is one entry of a textDocument/completion response.
type CompletionItem struct {
	Label            string `json:"label"`
	Kind             int    `json:"kind,omitempty"`
	Detail           string `json:"detail,omitempty"`
	Documentation    string `json:"documentation,omitempty"`
	InsertText       string `json:"insertText,omitempty"`
	InsertTextFormat int    `json:"insertTextFormat,omitempty"`
	SortText         string `json:"sortText,omitempty"`
	FilterText       string `json:"filterText,omitempty"`
}

// FoldingRange is one entry of a textDocument/foldingRange response. Lines
// are zero-based and the end line is inclusive, per the spec.
type FoldingRange struct {
	StartLine     int    `json:"startLine"`
	EndLine       int    `json:"endLine"`
	Kind          string `json:"kind,omitempty"`
	CollapsedText string `json:"collapsedText,omitempty"`
}

// FoldingRangeKind values from the LSP spec.
const (
	FoldingRangeComment = "comment"
	FoldingRangeImports = "imports"
	FoldingRangeRegion  = "region"
)

// TextEdit replaces Range with NewText. A zero Range inserts.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit carries the changes a rename or applyEdit asks the client to
// make, keyed by document URI.
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes,omitempty"`
}

// DocumentLink makes a span of text clickable, pointing at Target.
type DocumentLink struct {
	Range  Range  `json:"range"`
	Target string `json:"target,omitempty"`
}

// MarkupKind values from the LSP spec.
const (
	MarkupPlainText = "plaintext"
	MarkupMarkdown  = "markdown"
)

// MarkupContent is a completion or hover payload with a declared format.
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Severity values from the LSP spec.
const (
	SeverityError   = 1
	SeverityWarning = 2
	SeverityInfo    = 3
	SeverityHint    = 4
)

// Diagnostic is a single problem reported by a server.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// TextDocumentIdentifier identifies an open document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

// URIFromPath converts an absolute path to a file:// URI.
//
// Not a simple concatenation: each segment has to be escaped, and Windows
// paths need the drive letter after a leading slash with backslashes flipped.
func URIFromPath(path string) string {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" && len(path) > 1 && path[1] == ':' {
		path = "/" + path
	}
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "file://" + strings.Join(segments, "/")
}

// PathFromURI is the inverse, returning ok=false for a non-file URI.
func PathFromURI(uri string) (string, bool) {
	if !strings.HasPrefix(uri, "file://") {
		return "", false
	}
	parsed, err := url.Parse(uri)
	if err != nil {
		return "", false
	}
	path := parsed.Path
	if runtime.GOOS == "windows" && len(path) > 2 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	return filepath.FromSlash(path), true
}

// MaxDiagnosticsPerFile matches MAX_PER_FILE in diagnostic.ts.
const MaxDiagnosticsPerFile = 20

// Pretty renders one diagnostic the way diagnostic.ts does.
func (d Diagnostic) Pretty() string {
	severity := "ERROR"
	switch d.Severity {
	case SeverityWarning:
		severity = "WARN"
	case SeverityInfo:
		severity = "INFO"
	case SeverityHint:
		severity = "HINT"
	}
	return fmt.Sprintf("%s [%d:%d] %s", severity, d.Range.Start.Line+1, d.Range.Start.Character+1, d.Message)
}

// Report renders the <diagnostics> block the edit and write tools append to
// their output, porting report() in diagnostic.ts. Only errors are reported;
// warnings would flood the model with noise it cannot act on.
func Report(file string, issues []Diagnostic) string {
	var errs []Diagnostic
	for _, issue := range issues {
		if issue.Severity == SeverityError {
			errs = append(errs, issue)
		}
	}
	if len(errs) == 0 {
		return ""
	}
	suffix := ""
	if len(errs) > MaxDiagnosticsPerFile {
		suffix = fmt.Sprintf("\n... and %d more", len(errs)-MaxDiagnosticsPerFile)
		errs = errs[:MaxDiagnosticsPerFile]
	}
	lines := make([]string, 0, len(errs))
	for _, issue := range errs {
		lines = append(lines, issue.Pretty())
	}
	return fmt.Sprintf("<diagnostics file=\"%s\">\n%s%s\n</diagnostics>", file, strings.Join(lines, "\n"), suffix)
}
