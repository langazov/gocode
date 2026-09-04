package lsp

import (
	"github.com/langazov/gocode-go/internal/lspprotocol"
)

// The wire types and helpers in this file moved to internal/lspprotocol so
// the markdown language server in internal/mdlsp can share them. These
// aliases keep every existing call site — in this package and its consumers
// (internal/rag/chunk, internal/tool/builtins) — untouched.
type (
	Position       = lspprotocol.Position
	Range          = lspprotocol.Range
	DocumentSymbol = lspprotocol.DocumentSymbol
	Diagnostic     = lspprotocol.Diagnostic
)

const (
	SymbolKindFile          = lspprotocol.SymbolKindFile
	SymbolKindModule        = lspprotocol.SymbolKindModule
	SymbolKindNamespace     = lspprotocol.SymbolKindNamespace
	SymbolKindPackage       = lspprotocol.SymbolKindPackage
	SymbolKindClass         = lspprotocol.SymbolKindClass
	SymbolKindMethod        = lspprotocol.SymbolKindMethod
	SymbolKindProperty      = lspprotocol.SymbolKindProperty
	SymbolKindField         = lspprotocol.SymbolKindField
	SymbolKindConstructor   = lspprotocol.SymbolKindConstructor
	SymbolKindEnum          = lspprotocol.SymbolKindEnum
	SymbolKindInterface     = lspprotocol.SymbolKindInterface
	SymbolKindFunction      = lspprotocol.SymbolKindFunction
	SymbolKindVariable      = lspprotocol.SymbolKindVariable
	SymbolKindConstant      = lspprotocol.SymbolKindConstant
	SymbolKindString        = lspprotocol.SymbolKindString
	SymbolKindNumber        = lspprotocol.SymbolKindNumber
	SymbolKindBoolean       = lspprotocol.SymbolKindBoolean
	SymbolKindArray         = lspprotocol.SymbolKindArray
	SymbolKindObject        = lspprotocol.SymbolKindObject
	SymbolKindKey           = lspprotocol.SymbolKindKey
	SymbolKindNull          = lspprotocol.SymbolKindNull
	SymbolKindEnumMember    = lspprotocol.SymbolKindEnumMember
	SymbolKindStruct        = lspprotocol.SymbolKindStruct
	SymbolKindEvent         = lspprotocol.SymbolKindEvent
	SymbolKindOperator      = lspprotocol.SymbolKindOperator
	SymbolKindTypeParameter = lspprotocol.SymbolKindTypeParameter

	SeverityError   = lspprotocol.SeverityError
	SeverityWarning = lspprotocol.SeverityWarning
	SeverityInfo    = lspprotocol.SeverityInfo
	SeverityHint    = lspprotocol.SeverityHint
)

// publishDiagnosticsParams is textDocument/publishDiagnostics.
type publishDiagnosticsParams struct {
	URI         string                   `json:"uri"`
	Version     *int                     `json:"version,omitempty"`
	Diagnostics []lspprotocol.Diagnostic `json:"diagnostics"`
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

// serverCapabilities is the subset of the initialize result this client acts
// on. textDocumentSync is either a number or an object, so it is decoded
// loosely and normalized by syncKind.
type serverCapabilities struct {
	TextDocumentSync   any `json:"textDocumentSync,omitempty"`
	DiagnosticProvider any `json:"diagnosticProvider,omitempty"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

// syncKind normalizes textDocumentSync, which the spec allows to be either
// the number itself or an object carrying `change`.
func (c serverCapabilities) syncKind() int {
	switch value := c.TextDocumentSync.(type) {
	case float64:
		return int(value)
	case map[string]any:
		if change, ok := value["change"].(float64); ok {
			return int(change)
		}
	}
	return 0
}

// uriFromPath converts an absolute path to a file:// URI.
func uriFromPath(path string) string { return lspprotocol.URIFromPath(path) }

// pathFromURI is the inverse, returning ok=false for a non-file URI.
func pathFromURI(uri string) (string, bool) { return lspprotocol.PathFromURI(uri) }

// Report renders the <diagnostics> block the edit and write tools append to
// their output. Only errors are reported; warnings would flood the model with
// noise it cannot act on.
func Report(file string, issues []Diagnostic) string { return lspprotocol.Report(file, issues) }
