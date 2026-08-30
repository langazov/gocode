package builtins

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

const defaultGrepLimit = 1000

type GrepTool struct {
	resolver Resolver
}

func NewGrepTool(resolver Resolver) *GrepTool {
	return &GrepTool{resolver: resolver}
}

func (t *GrepTool) Name() string { return "grep" }

func (t *GrepTool) Description() string {
	return "Search file contents by regular expression within the working directory."
}

func (t *GrepTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regex pattern to search for in file contents",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Directory to search. Defaults to the working directory.",
			},
			"include": map[string]any{
				"type":        "string",
				"description": `File glob to include in the search (for example, "*.js" or "*.{ts,tsx}")`,
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum matches to return",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *GrepTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	pattern := stringArg(input, "pattern")
	if pattern == "" {
		return "", fmt.Errorf("grep: pattern is required")
	}
	expr, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("grep: invalid pattern: %w", err)
	}
	include := stringArg(input, "include")
	limit := intArg(input, "limit", defaultGrepLimit)
	base := t.resolver.Root
	if sub := stringArg(input, "path"); sub != "" {
		resolved, resolveErr := t.resolver.Resolve(sub)
		if resolveErr != nil {
			return "", resolveErr
		}
		base = resolved
	}
	matches, err := grepFiles(base, expr, include, limit)
	if err != nil {
		return "", fmt.Errorf("Unable to search for %s", pattern)
	}
	if len(matches) == 0 {
		return "No files found", nil
	}
	return formatGrepOutput(matches), nil
}

type grepMatch struct {
	path string
	line int
	text string
}

func grepFiles(base string, expr *regexp.Regexp, include string, limit int) ([]grepMatch, error) {
	root := filepath.Clean(base)
	var matches []grepMatch
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
		if include != "" {
			name := filepath.ToSlash(entry.Name())
			nameMatch, nameErr := doublestar.Match(include, name)
			pathMatch, pathErr := doublestar.Match(include, relative)
			if (nameErr != nil || !nameMatch) && (pathErr != nil || !pathMatch) {
				return nil
			}
		}
		fileMatches, grepErr := grepFile(path, relative, expr, limit-len(matches))
		if grepErr != nil {
			return nil
		}
		matches = append(matches, fileMatches...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func grepFile(path, relative string, expr *regexp.Regexp, limit int) ([]grepMatch, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var matches []grepMatch
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if strings.ContainsRune(text, 0) {
			return nil, nil
		}
		if expr.MatchString(text) {
			preview := text
			if len(preview) > 240 {
				preview = preview[:240] + "..."
			}
			matches = append(matches, grepMatch{path: relative, line: line, text: preview})
			if len(matches) >= limit {
				break
			}
		}
	}
	return matches, scanner.Err()
}

func formatGrepOutput(matches []grepMatch) string {
	lines := []string{fmt.Sprintf("Found %d matches", len(matches))}
	current := ""
	for _, match := range matches {
		if current != match.path {
			if current != "" {
				lines = append(lines, "")
			}
			current = match.path
			lines = append(lines, fmt.Sprintf("%s:", match.path))
		}
		lines = append(lines, fmt.Sprintf("  Line %d: %s", match.line, match.text))
	}
	return strings.Join(lines, "\n")
}
