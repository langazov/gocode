package builtins

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type EditTool struct {
	resolver Resolver
}

func NewEditTool(resolver Resolver) *EditTool {
	return &EditTool{resolver: resolver}
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
	return formatEditOutput(target, replacements, oldString, newString), nil
}

func splitBOM(text string) (bool, string) {
	if strings.HasPrefix(text, "\uFEFF") {
		return true, text[1:]
	}
	return false, text
}

func joinBOM(text string, bom bool) string {
	if bom {
		return "\uFEFF" + text
	}
	return text
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

func formatEditOutput(path string, replacements int, oldString, newString string) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Edited file successfully: %s", path))
	lines = append(lines, fmt.Sprintf("Replacements: %d", replacements))
	lines = append(lines, "```diff")
	lines = append(lines, previewLines(oldString, "-")...)
	lines = append(lines, previewLines(newString, "+")...)
	lines = append(lines, "```")
	return strings.Join(lines, "\n")
}

func previewLines(value, prefix string) []string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	var shown []string
	max := len(lines)
	if max > 6 {
		max = 6
	}
	for _, line := range lines[:max] {
		if len(line) > 240 {
			line = line[:240] + "..."
		}
		shown = append(shown, prefix+line)
	}
	if len(lines) > max {
		shown = append(shown, prefix+"...")
	}
	return shown
}
