package lsp

import (
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// Position is a zero-based line/character pair.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
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

// publishDiagnosticsParams is textDocument/publishDiagnostics.
type publishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
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
//
// Not a simple concatenation: each segment has to be escaped, and Windows
// paths need the drive letter after a leading slash with backslashes flipped.
func uriFromPath(path string) string {
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

// pathFromURI is the inverse, returning ok=false for a non-file URI.
func pathFromURI(uri string) (string, bool) {
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

// maxDiagnosticsPerFile matches MAX_PER_FILE in diagnostic.ts.
const maxDiagnosticsPerFile = 20

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
	if len(errs) > maxDiagnosticsPerFile {
		suffix = fmt.Sprintf("\n... and %d more", len(errs)-maxDiagnosticsPerFile)
		errs = errs[:maxDiagnosticsPerFile]
	}
	lines := make([]string, 0, len(errs))
	for _, issue := range errs {
		lines = append(lines, issue.Pretty())
	}
	return fmt.Sprintf("<diagnostics file=\"%s\">\n%s%s\n</diagnostics>", file, strings.Join(lines, "\n"), suffix)
}
