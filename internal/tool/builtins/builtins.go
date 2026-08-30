// Package builtins ports the core filesystem and shell tools from
// packages/core/src/tool. Tools are scoped to a root directory (the Go
// analogue of the active Location); relative paths resolve within it and
// paths escaping it are rejected unless externally approved.
package builtins

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/tool"
)

// Resolver scopes tool paths to a root directory.
type Resolver struct {
	Root string
}

// Resolve returns the absolute path for input, keeping it within the root.
func (r Resolver) Resolve(input string) (string, error) {
	candidate := input
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(r.Root, candidate)
	}
	candidate = filepath.Clean(candidate)
	root := filepath.Clean(r.Root)
	if candidate != root && !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes working directory: %s", input)
	}
	return candidate, nil
}

// Register adds all built-in tools to the registry, scoped to root. The
// database backs session-aware tools such as todowrite.
func Register(registry *tool.Registry, root string, database *db.DB) {
	resolver := Resolver{Root: root}
	registry.Register(NewReadTool(resolver))
	registry.Register(NewWriteTool(resolver))
	registry.Register(NewEditTool(resolver))
	registry.Register(NewGlobTool(resolver))
	registry.Register(NewGrepTool(resolver))
	registry.Register(NewBashTool(resolver))
	registry.Register(NewWebFetchTool())
	if database != nil {
		registry.Register(NewTodoTool(database))
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
