package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/langazov/gocode-go/internal/lsp"
)

// fakeDiagnoser stands in for a running language server: it records what was
// touched and returns canned diagnostics.
type fakeDiagnoser struct {
	mu       sync.Mutex
	touched  []string
	waited   []bool
	byFile   map[string][]lsp.Diagnostic
	blockFor chan struct{}
}

func (f *fakeDiagnoser) Touch(ctx context.Context, file string, wait bool) {
	if f.blockFor != nil {
		<-f.blockFor
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touched = append(f.touched, file)
	f.waited = append(f.waited, wait)
}

func (f *fakeDiagnoser) DiagnosticsFor(file string) []lsp.Diagnostic {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byFile[file]
}

func (f *fakeDiagnoser) touchedFiles() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.touched...)
}

// TestEditToolReportsDiagnostics: the value of the LSP integration is that a
// broken edit says so in the same turn, not on some later read.
func TestEditToolReportsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	if err := os.WriteFile(file, []byte("package main\nfunc old() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	diagnoser := &fakeDiagnoser{byFile: map[string][]lsp.Diagnostic{
		file: {{
			Severity: lsp.SeverityError,
			Message:  "undefined: helper",
			Range:    lsp.Range{Start: lsp.Position{Line: 1, Character: 5}},
		}},
	}}
	tool := NewEditToolWith(Resolver{Root: dir}, diagnoser)

	out, err := tool.Execute(context.Background(), map[string]any{
		"path":      "main.go",
		"oldString": "old",
		"newString": "helper",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "LSP errors detected in this file, please fix:") {
		t.Errorf("edit output is missing the diagnostics lead-in:\n%s", out)
	}
	if !strings.Contains(out, "undefined: helper") {
		t.Errorf("edit output is missing the diagnostic itself:\n%s", out)
	}
	if !strings.Contains(out, "ERROR [2:6]") {
		t.Errorf("expected 1-based positions:\n%s", out)
	}

	// The edited file must be the one that was touched, and it must wait.
	touched := diagnoser.touchedFiles()
	if len(touched) != 1 || touched[0] != file {
		t.Errorf("touched %v, want just %q", touched, file)
	}
	if !diagnoser.waited[0] {
		t.Error("an edit must wait for diagnostics, or they arrive too late to report")
	}
}

// TestEditToolSilentWhenClean: a clean edit must not gain a diagnostics
// section, which would be noise on every successful edit.
func TestEditToolSilentWhenClean(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	os.WriteFile(file, []byte("package main\nfunc old() {}\n"), 0o644)

	diagnoser := &fakeDiagnoser{byFile: map[string][]lsp.Diagnostic{}}
	tool := NewEditToolWith(Resolver{Root: dir}, diagnoser)

	out, err := tool.Execute(context.Background(), map[string]any{
		"path": "main.go", "oldString": "old", "newString": "renamed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "LSP errors") {
		t.Errorf("a clean edit must not mention LSP:\n%s", out)
	}
}

// TestEditToolIgnoresWarnings: only errors are reported, matching report() in
// diagnostic.ts. Warnings would crowd out what the model can act on.
func TestEditToolIgnoresWarnings(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	os.WriteFile(file, []byte("package main\nfunc old() {}\n"), 0o644)

	diagnoser := &fakeDiagnoser{byFile: map[string][]lsp.Diagnostic{
		file: {{Severity: lsp.SeverityWarning, Message: "unused variable"}},
	}}
	tool := NewEditToolWith(Resolver{Root: dir}, diagnoser)

	out, _ := tool.Execute(context.Background(), map[string]any{
		"path": "main.go", "oldString": "old", "newString": "renamed",
	})
	if strings.Contains(out, "unused variable") {
		t.Errorf("warnings must not be reported:\n%s", out)
	}
}

func TestWriteToolReportsDiagnostics(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "new.go")
	diagnoser := &fakeDiagnoser{byFile: map[string][]lsp.Diagnostic{
		file: {{Severity: lsp.SeverityError, Message: "syntax error"}},
	}}
	tool := NewWriteToolWith(Resolver{Root: dir}, diagnoser)

	out, err := tool.Execute(context.Background(), map[string]any{
		"path": "new.go", "content": "package main\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "syntax error") {
		t.Errorf("write output is missing the diagnostic:\n%s", out)
	}
}

// TestReadToolWarmsWithoutBlocking: a read must not wait on a language server
// starting up, which can take seconds on a large project.
func TestReadToolWarmsWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "main.go")
	os.WriteFile(file, []byte("package main\n"), 0o644)

	// A diagnoser that never returns: if the read waited on it, this test
	// would hang rather than fail.
	blocked := make(chan struct{})
	diagnoser := &fakeDiagnoser{byFile: map[string][]lsp.Diagnostic{}, blockFor: blocked}
	t.Cleanup(func() { close(blocked) })

	tool := NewReadToolWith(Resolver{Root: dir}, diagnoser)
	out, err := tool.Execute(context.Background(), map[string]any{"path": "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "package main") {
		t.Errorf("read returned %q", out)
	}
}

// TestToolsWithoutDiagnoserAreUnchanged: LSP is optional, and a nil service
// must leave tool output exactly as it was.
func TestToolsWithoutDiagnoserAreUnchanged(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc old() {}\n"), 0o644)
	resolver := Resolver{Root: dir}

	withNil := NewEditToolWith(resolver, nil)
	out, err := withNil.Execute(context.Background(), map[string]any{
		"path": "main.go", "oldString": "old", "newString": "renamed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "LSP") {
		t.Errorf("a nil diagnoser must add nothing:\n%s", out)
	}
}

// TestNilServiceIsSafe: a *lsp.Service that was never built must be callable,
// so boot paths without LSP need no branching.
func TestNilServiceIsSafe(t *testing.T) {
	var service *lsp.Service
	if service.Enabled() {
		t.Error("a nil service must report disabled")
	}
	service.Touch(context.Background(), "x.go", true)
	if got := service.DiagnosticsFor("x.go"); got != nil {
		t.Errorf("DiagnosticsFor on nil = %v, want nil", got)
	}
	if got := service.Status(); got != nil {
		t.Errorf("Status on nil = %v, want nil", got)
	}
	service.Shutdown()
}
