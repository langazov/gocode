package main

import "github.com/anomalyco/opencode-go/internal/clix"

// generateCommand mirrors GenerateCommand in cli/cmd/generate.ts: emits the
// server's OpenAPI spec with SDK code samples injected. The Go server has no
// OpenAPI export yet.
func generateCommand() *clix.Command {
	return &clix.Command{
		Name: "generate",
		Run:  func(a *clix.Args) error { return notImplemented("opencode generate") },
	}
}
