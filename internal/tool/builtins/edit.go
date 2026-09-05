package builtins

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/langazov/gocode-go/internal/diff"
	"github.com/langazov/gocode-go/internal/patch"
)

type EditTool struct {
	resolver Resolver
	// diagnoser, when set, appends the language servers' verdict on the edited
	// file to the tool output.
	diagnoser Diagnoser
}

func NewEditTool(resolver Resolver) *EditTool {
	return &EditTool{resolver: resolver}
}

// NewEditToolWith adds LSP diagnostics reporting to the edit tool.
func NewEditToolWith(resolver Resolver, diagnoser Diagnoser) *EditTool {
	return &EditTool{resolver: resolver, diagnoser: diagnoser}
}

func (t *EditTool) Name() string { return "edit" }

func (t *EditTool) Description() string {
	return "Replace exact text in one file. Relative paths resolve from the working directory."
}

func (t *EditTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path to edit",
			},
			"oldString": map[string]any{
				"type":        "string",
				"description": "Exact text to replace",
			},
			"newString": map[string]any{
				"type":        "string",
				"description": "Replacement text, which must differ from oldString",
			},
			"replaceAll": map[string]any{
				"type":        "boolean",
				"description": "Replace all exact occurrences of oldString (default false)",
			},
		},
		"required": []string{"path", "oldString", "newString"},
	}
}

func (t *EditTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	path := stringArg(input, "path")
	oldString := stringArg(input, "oldString")
	newString := stringArg(input, "newString")
	replaceAll := boolArg(input, "replaceAll")
	if path == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if oldString == newString {
		return "", fmt.Errorf("No changes to apply: oldString and newString are identical.")
	}
	if oldString == "" {
		return "", fmt.Errorf("oldString must not be empty. Use write to create or overwrite a file.")
	}
	target, err := t.resolver.Resolve(path)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("Unable to edit %s", path)
	}
	bom, text := splitBOM(string(raw))
	ending := detectLineEnding(text)
	oldString = convertToLineEnding(oldString, ending)
	newString = convertToLineEnding(newString, ending)
	replacements := countOccurrences(text, oldString)
	if replacements == 0 {
		return "", fmt.Errorf("Could not find oldString in the file. It must match exactly, including whitespace and indentation.")
	}
	if replacements > 1 && !replaceAll {
		return "", fmt.Errorf("Found multiple exact matches for oldString. Provide more surrounding context or set replaceAll to true.")
	}
	var replaced string
	if replaceAll {
		replaced = strings.ReplaceAll(text, oldString, newString)
	} else {
		replaced = strings.Replace(text, oldString, newString, 1)
	}
	output := joinBOM(replaced, bom)
	if err := os.WriteFile(target, []byte(output), 0o644); err != nil {
		return "", fmt.Errorf("Unable to edit %s", path)
	}
	return formatEditOutput(target, replacements, text, replaced) + diagnosticsFooter(ctx, t.diagnoser, target), nil
}

// splitBOM and joinBOM defer to internal/patch, which already had the correct
// pair. The copy that used to live here sliced `text[1:]` \u2014 one *byte* off a
// three-byte mark \u2014 so every edit of a BOM file left the two trailing bytes
// behind and prepended a fresh mark in front of them. Two bytes of rubbish
// were added to the file on each edit, and the tool reported success.
func splitBOM(text string) (bool, string) {
	body, bom := patch.SplitBOM(text)
	return bom, body
}

func joinBOM(text string, bom bool) string {
	return patch.JoinBOM(text, bom)
}

func detectLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func convertToLineEnding(text, ending string) string {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if ending == "\n" {
		return normalized
	}
	return strings.ReplaceAll(normalized, "\n", "\r\n")
}

func countOccurrences(content, search string) int {
	if search == "" {
		return len(content) + 1
	}
	count := 0
	offset := 0
	for {
		index := strings.Index(content[offset:], search)
		if index == -1 {
			break
		}
		count++
		offset += index + len(search)
	}
	return count
}

// formatEditOutput renders the result as a real unified diff of the whole
// file, replacing the previous approximation that just printed oldString
// prefixed with "-" and newString with "+" — that showed no surrounding
// context and no line numbers, and misrepresented a replaceAll edit as a
// single change.
func formatEditOutput(path string, replacements int, before, after string) string {
	unified := diff.Trim(diff.Unified(path, path, before, after))
	stat := diff.Count(before, after)

	var lines []string
	lines = append(lines, fmt.Sprintf("Edited file successfully: %s", path))
	lines = append(lines, fmt.Sprintf("Replacements: %d (+%d -%d)", replacements, stat.Additions, stat.Deletions))
	lines = append(lines, "```diff")
	lines = append(lines, truncateDiff(unified, maxDiffLines)...)
	lines = append(lines, "```")
	return strings.Join(lines, "\n")
}

// maxDiffLines bounds how much of a diff is echoed back to the model. A large
// refactor can produce thousands of lines, which is context the model rarely
// needs — the file itself is authoritative.
const maxDiffLines = 60

func truncateDiff(unified string, limit int) []string {
	lines := strings.Split(strings.TrimRight(unified, "\n"), "\n")
	truncated := false
	if len(lines) > limit {
		lines = lines[:limit]
		truncated = true
	}
	for i, line := range lines {
		if len(line) > 240 {
			lines[i] = line[:240] + "..."
		}
	}
	if truncated {
		lines = append(lines, "... diff truncated")
	}
	return lines
}
