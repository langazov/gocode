package builtins

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
)

const (
	defaultReadLimit = 2000
	maxLineLength    = 2000
)

type ReadTool struct {
	resolver Resolver
}

func NewReadTool(resolver Resolver) *ReadTool {
	return &ReadTool{resolver: resolver}
}

func (t *ReadTool) Name() string { return "read" }

func (t *ReadTool) Description() string {
	return "Read a text file by line offset or list a directory page. Relative paths resolve from the working directory."
}

func (t *ReadTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "The absolute or relative path to the file or directory",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "The 1-based line or directory entry offset to start reading from",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "The maximum number of directory entries or text lines to read",
			},
		},
		"required": []string{"path"},
	}
}

func (t *ReadTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	path, _ := input["path"].(string)
	if path == "" {
		return "", fmt.Errorf("read: path is required")
	}
	offset := intArg(input, "offset", 1)
	limit := intArg(input, "limit", defaultReadLimit)
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = defaultReadLimit
	}
	target, err := t.resolver.Resolve(path)
	if err != nil {
		return "", err
	}
	if isDir(target) {
		return listDirectory(target, offset, limit)
	}
	return readFile(target, offset, limit)
}

func listDirectory(dir string, offset, limit int) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("Unable to read %s", dir)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	start := offset - 1
	if start > len(names) {
		return "", fmt.Errorf("Offset out of range for %s", dir)
	}
	end := start + limit
	if end > len(names) {
		end = len(names)
	}
	return strings.Join(names[start:end], "\n"), nil
}

func readFile(path string, offset, limit int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("Unable to read %s", path)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out strings.Builder
	line := 1
	for scanner.Scan() {
		if line >= offset && line < offset+limit {
			text := scanner.Text()
			if len(text) > maxLineLength {
				text = text[:maxLineLength] + "... (line truncated)"
			}
			fmt.Fprintf(&out, "%d: %s\n", line, text)
		}
		line++
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("Unable to read %s", path)
	}
	if out.Len() == 0 && offset > 1 && line <= offset {
		return "", fmt.Errorf("Offset out of range for %s", path)
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

func intArg(input map[string]any, key string, fallback int) int {
	switch value := input[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	}
	return fallback
}

func stringArg(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func boolArg(input map[string]any, key string) bool {
	value, _ := input[key].(bool)
	return value
}
