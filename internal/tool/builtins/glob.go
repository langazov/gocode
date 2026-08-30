package builtins

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const defaultGlobLimit = 10000

type GlobTool struct {
	resolver Resolver
}

func NewGlobTool(resolver Resolver) *GlobTool {
	return &GlobTool{resolver: resolver}
}

func (t *GlobTool) Name() string { return "glob" }

func (t *GlobTool) Description() string {
	return "Find files by glob pattern within the working directory. Returns relative file paths."
}

func (t *GlobTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Glob pattern to match files against (supports **)",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to search. Defaults to the working directory.",
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum results to return",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GlobTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	pattern := stringArg(input, "pattern")
	if pattern == "" {
		return "", fmt.Errorf("glob: pattern is required")
	}
	limit := intArg(input, "limit", defaultGlobLimit)
	base := t.resolver.Root
	if sub := stringArg(input, "path"); sub != "" {
		resolved, err := t.resolver.Resolve(sub)
		if err != nil {
			return "", err
		}
		base = resolved
	}
	matches, err := globFiles(base, pattern, limit)
	if err != nil {
		return "", fmt.Errorf("Unable to find files matching %s", pattern)
	}
	if len(matches) == 0 {
		return "No files found", nil
	}
	return strings.Join(matches, "\n"), nil
}

func globFiles(base, pattern string, limit int) ([]string, error) {
	var matches []string
	root := filepath.Clean(base)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if len(matches) >= limit {
			return fs.SkipAll
		}
		if entry.IsDir() {
			if entry.Name() == ".git" && path != root {
				return fs.SkipDir
			}
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if matched, matchErr := doublestar.Match(filepath.ToSlash(pattern), relative); matchErr == nil && matched {
			matches = append(matches, relative)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(matches)
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}
