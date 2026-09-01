package builtins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anomalyco/opencode-go/internal/patch"
)

// ApplyPatchTool applies a multi-file patch in opencode's own patch format.
//
// Every hunk is parsed and resolved against the filesystem before anything is
// written, so a patch that fails partway through leaves no half-applied state.
type ApplyPatchTool struct {
	resolver Resolver
}

func NewApplyPatchTool(resolver Resolver) *ApplyPatchTool {
	return &ApplyPatchTool{resolver: resolver}
}

func (t *ApplyPatchTool) Name() string { return "apply_patch" }

func (t *ApplyPatchTool) Description() string {
	return strings.Join([]string{
		"Use the `apply_patch` tool to edit files. Your patch language is a stripped-down, file-oriented diff format designed to be easy to parse and safe to apply. You can think of it as a high-level envelope:",
		"",
		"*** Begin Patch",
		"[ one or more file sections ]",
		"*** End Patch",
		"",
		"Within that envelope, you get a sequence of file operations.",
		"You MUST include a header to specify the action you are taking.",
		"Each operation starts with one of three headers:",
		"",
		"*** Add File: <path> - create a new file. Every following line is a + line (the initial contents).",
		"*** Delete File: <path> - remove an existing file. Nothing follows.",
		"*** Update File: <path> - patch an existing file in place (optionally with a rename).",
		"",
		"Example patch:",
		"",
		"```",
		"*** Begin Patch",
		"*** Add File: hello.txt",
		"+Hello world",
		"*** Update File: src/app.py",
		"*** Move to: src/main.py",
		"@@ def greet():",
		"-print(\"Hi\")",
		"+print(\"Hello, world!\")",
		"*** Delete File: obsolete.txt",
		"*** End Patch",
		"```",
		"",
		"It is important to remember:",
		"",
		"- You must include a header with your intended action (Add/Delete/Update)",
		"- You must prefix new lines with `+` even when creating a new file",
	}, "\n")
}

func (t *ApplyPatchTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patchText": map[string]any{
				"type":        "string",
				"description": "The full patch text that describes all changes to be made",
			},
		},
		"required": []string{"patchText"},
	}
}

// fileChange is one resolved, ready-to-write file operation.
type fileChange struct {
	path     string
	movePath string
	kind     string // add | update | move | delete
	content  string
	bom      bool
}

func (t *ApplyPatchTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	patchText := stringArg(input, "patchText")
	if patchText == "" {
		return "", fmt.Errorf("apply_patch: patchText is required")
	}

	hunks, err := patch.Parse(patchText)
	if err != nil {
		return "", fmt.Errorf("apply_patch verification failed: %w", err)
	}
	if len(hunks) == 0 {
		normalized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(patchText, "\r\n", "\n"), "\r", "\n"))
		if normalized == "*** Begin Patch\n*** End Patch" {
			return "", fmt.Errorf("patch rejected: empty patch")
		}
		return "", fmt.Errorf("apply_patch verification failed: no hunks found")
	}

	// Resolve everything first: a patch either applies whole or not at all.
	changes := make([]fileChange, 0, len(hunks))
	for _, hunk := range hunks {
		target, err := t.resolver.Resolve(hunk.Path)
		if err != nil {
			return "", err
		}
		switch hunk.Type {
		case patch.HunkAdd:
			contents := hunk.Contents
			if contents != "" && !strings.HasSuffix(contents, "\n") {
				contents += "\n"
			}
			body, bom := patch.SplitBOM(contents)
			changes = append(changes, fileChange{path: target, kind: "add", content: body, bom: bom})

		case patch.HunkUpdate:
			info, err := os.Stat(target)
			if err != nil || info.IsDir() {
				return "", fmt.Errorf("apply_patch verification failed: Failed to read file to update: %s", target)
			}
			raw, err := os.ReadFile(target)
			if err != nil {
				return "", fmt.Errorf("apply_patch verification failed: %w", err)
			}
			content, bom, err := patch.Derive(target, hunk.Chunks, string(raw))
			if err != nil {
				return "", fmt.Errorf("apply_patch verification failed: %w", err)
			}
			change := fileChange{path: target, kind: "update", content: content, bom: bom}
			if hunk.MovePath != "" {
				movePath, err := t.resolver.Resolve(hunk.MovePath)
				if err != nil {
					return "", err
				}
				change.kind, change.movePath = "move", movePath
			}
			changes = append(changes, change)

		case patch.HunkDelete:
			raw, err := os.ReadFile(target)
			if err != nil {
				return "", fmt.Errorf("apply_patch verification failed: %w", err)
			}
			_, bom := patch.SplitBOM(string(raw))
			changes = append(changes, fileChange{path: target, kind: "delete", bom: bom})

		default:
			return "", fmt.Errorf("apply_patch: unknown hunk type %q", hunk.Type)
		}
	}

	var summary []string
	for _, change := range changes {
		switch change.kind {
		case "add":
			if err := writeWithDirs(change.path, patch.JoinBOM(change.content, change.bom)); err != nil {
				return "", err
			}
			summary = append(summary, "A "+t.relative(change.path))
		case "update":
			if err := writeWithDirs(change.path, patch.JoinBOM(change.content, change.bom)); err != nil {
				return "", err
			}
			summary = append(summary, "M "+t.relative(change.path))
		case "move":
			if err := writeWithDirs(change.movePath, patch.JoinBOM(change.content, change.bom)); err != nil {
				return "", err
			}
			if err := os.Remove(change.path); err != nil && !os.IsNotExist(err) {
				return "", err
			}
			summary = append(summary, "M "+t.relative(change.movePath))
		case "delete":
			if err := os.Remove(change.path); err != nil && !os.IsNotExist(err) {
				return "", err
			}
			summary = append(summary, "D "+t.relative(change.path))
		}
	}
	return "Success. Updated the following files:\n" + strings.Join(summary, "\n"), nil
}

// relative renders a path for the summary, relative to the tool root and
// always with forward slashes so output is stable across platforms.
func (t *ApplyPatchTool) relative(target string) string {
	rel, err := filepath.Rel(t.resolver.Root, target)
	if err != nil {
		return target
	}
	return filepath.ToSlash(rel)
}

func writeWithDirs(target, content string) error {
	if dir := filepath.Dir(target); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(target, []byte(content), 0o644)
}
