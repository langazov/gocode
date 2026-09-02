package builtins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

type WriteTool struct {
	resolver Resolver
	// diagnoser, when set, appends the language servers' verdict on the
	// written file to the tool output.
	diagnoser Diagnoser
}

func NewWriteTool(resolver Resolver) *WriteTool {
	return &WriteTool{resolver: resolver}
}

// NewWriteToolWith adds LSP diagnostics reporting to the write tool.
func NewWriteToolWith(resolver Resolver, diagnoser Diagnoser) *WriteTool {
	return &WriteTool{resolver: resolver, diagnoser: diagnoser}
}

func (t *WriteTool) Name() string { return "write" }

func (t *WriteTool) Description() string {
	return "Write content to one file, creating parent directories. Relative paths resolve from the working directory."
}

func (t *WriteTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write to the file",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (t *WriteTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	path := stringArg(input, "path")
	content := stringArg(input, "content")
	if path == "" {
		return "", fmt.Errorf("write: path is required")
	}
	target, err := t.resolver.Resolve(path)
	if err != nil {
		return "", err
	}
	_, statErr := os.Stat(target)
	existed := statErr == nil
	if dir := filepath.Dir(target); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("Unable to write %s", path)
		}
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("Unable to write %s", path)
	}
	verb := "Created"
	if existed {
		verb = "Wrote"
	}
	return fmt.Sprintf("%s file successfully: %s", verb, target) + diagnosticsFooter(ctx, t.diagnoser, target), nil
}
