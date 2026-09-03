package builtins

import (
	"context"

	"github.com/langazov/gocode-go/internal/lsp"
)

// Diagnoser is the slice of the LSP service the tools need. It is an interface
// so the tool package does not depend on a running language server to be
// testable, and so a nil value disables the integration cleanly.
type Diagnoser interface {
	// Touch reloads a file in every server that handles it, optionally waiting
	// for fresh diagnostics.
	Touch(ctx context.Context, file string, wait bool)
	// DiagnosticsFor returns the current diagnostics for one file.
	DiagnosticsFor(file string) []lsp.Diagnostic
}

// diagnosticsFooter runs a file past the language servers and renders the
// block the edit, write and patch tools append to their output.
//
// This ports the tail of edit.ts: touch the file, read back its diagnostics,
// and if any are errors, tell the model to fix them. Reporting them in the
// same turn is the point — otherwise the model only learns it broke the build
// on some later read, if at all.
func diagnosticsFooter(ctx context.Context, diagnoser Diagnoser, file string) string {
	if diagnoser == nil {
		return ""
	}
	diagnoser.Touch(ctx, file, true)
	block := lsp.Report(file, diagnoser.DiagnosticsFor(file))
	if block == "" {
		return ""
	}
	return "\n\nLSP errors detected in this file, please fix:\n" + block
}

// warmDiagnostics reloads a file without waiting, for the read tool. Warming
// the server on a read means the diagnostics are already there by the time an
// edit lands, rather than the edit paying for startup.
func warmDiagnostics(ctx context.Context, diagnoser Diagnoser, file string) {
	if diagnoser == nil {
		return
	}
	// Detached: a read must not block on a language server starting up, which
	// can take seconds for a large project. Matches the forkIn(scope) call in
	// read.ts.
	go diagnoser.Touch(context.WithoutCancel(ctx), file, false)
}
