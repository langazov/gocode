package builtins

import (
	"context"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/tool"
)

// readText reads a text file for a tool call: through the client when the
// session's environment offers a readable file system (so an editor's unsaved
// buffer is what the model sees), from disk otherwise.
func readText(ctx context.Context, path string) ([]byte, error) {
	if env := tool.EnvFor(ctx); env != nil && env.FS != nil && env.FS.CanRead() {
		content, err := env.FS.ReadTextFile(ctx, path)
		if err != nil {
			return nil, err
		}
		return []byte(content), nil
	}
	return os.ReadFile(path)
}

// writeText writes a text file for a tool call, creating parent directories:
// through the client when it offers a writable file system (the editor then
// tracks the change as its own), to disk otherwise. The directories are
// created locally either way — the client protocol has no mkdir, and a client
// write into a missing directory would otherwise fail.
func writeText(ctx context.Context, path string, content []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if env := tool.EnvFor(ctx); env != nil && env.FS != nil && env.FS.CanWrite() {
		return env.FS.WriteTextFile(ctx, path, string(content))
	}
	return os.WriteFile(path, content, 0o644)
}
