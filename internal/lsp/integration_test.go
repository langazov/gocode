package lsp

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestAgainstRealGopls exercises the whole stack against an actual language
// server: process spawn, the initialize handshake, didOpen, and a real
// publishDiagnostics payload.
//
// The fake-server tests cover the protocol deterministically; this one catches
// what a fake cannot — a real server's timing, its capability response, and
// the requests it makes back to the client during startup. It skips when gopls
// is not installed rather than failing, since that is an environment fact
// rather than a defect.
func TestAgainstRealGopls(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real language server")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls is not installed")
	}

	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module example.com/x\n\ngo 1.21\n")
	file := writeFile(t, dir, "main.go", "package main\n\nfunc main() {\n\tundefinedFunction()\n}\n")

	service := New(dir, nil)
	t.Cleanup(service.Shutdown)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	service.Touch(ctx, file, true)

	status := service.Status()
	if len(status) != 1 || status[0].ID != "gopls" || status[0].Status != "connected" {
		t.Fatalf("status = %+v, want one connected gopls", status)
	}

	diagnostics := service.DiagnosticsFor(file)
	if len(diagnostics) == 0 {
		t.Fatal("gopls reported nothing for a call to an undefined function")
	}
	found := false
	for _, item := range diagnostics {
		if item.Severity == SeverityError && strings.Contains(item.Message, "undefinedFunction") {
			found = true
			// The call is on source line 4, which is index 3.
			if item.Range.Start.Line != 3 {
				t.Errorf("diagnostic is on line index %d, want 3", item.Range.Start.Line)
			}
		}
	}
	if !found {
		t.Errorf("expected an error naming undefinedFunction, got %+v", diagnostics)
	}

	// And the rendered block the tools append.
	report := Report(file, diagnostics)
	if !strings.Contains(report, "ERROR [4:") {
		t.Errorf("report should carry a 1-based line 4:\n%s", report)
	}

	// Fixing the file must clear the diagnostics.
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	service.Touch(ctx, file, true)
	if got := service.DiagnosticsFor(file); len(got) != 0 {
		t.Errorf("after the fix gopls still reports %+v", got)
	}
}
