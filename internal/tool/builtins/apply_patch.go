package builtins

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anomalyco/opencode-go/internal/diff"
	"github.com/anomalyco/opencode-go/internal/patch"
)

// ApplyPatchTool applies a multi-file patch in opencode's own patch format.
//
// Every hunk is parsed and resolved against the filesystem before anything is
// written, so a patch that fails partway through leaves no half-applied state.
type ApplyPatchTool struct {
	resolver Resolver
	// diagnoser, when set, appends the language servers' verdict on every
	// patched file.
	diagnoser Diagnoser
}

func NewApplyPatchTool(resolver Resolver) *ApplyPatchTool {
	return &ApplyPatchTool{resolver: resolver}
}

// NewApplyPatchToolWith adds LSP diagnostics reporting to apply_patch.
func NewApplyPatchToolWith(resolver Resolver, diagnoser Diagnoser) *ApplyPatchTool {
	return &ApplyPatchTool{resolver: resolver, diagnoser: diagnoser}
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
	oldText  string
	content  string
	bom      bool
	diff     string
	stat     diff.Stat
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
			changes = append(changes, newFileChange(target, "add", "", body, bom))

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
			oldText, _ := patch.SplitBOM(string(raw))
			change := newFileChange(target, "update", oldText, content, bom)
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
			oldText, bom := patch.SplitBOM(string(raw))
			changes = append(changes, newFileChange(target, "delete", oldText, "", bom))

		default:
			return "", fmt.Errorf("apply_patch: unknown hunk type %q", hunk.Type)
		}
	}

	var summary []string
	var combined strings.Builder
	for _, change := range changes {
		switch change.kind {
		case "add":
			if err := writeWithDirs(change.path, patch.JoinBOM(change.content, change.bom)); err != nil {
				return "", err
			}
			summary = append(summary, t.summaryLine("A", change.path, change))
		case "update":
			if err := writeWithDirs(change.path, patch.JoinBOM(change.content, change.bom)); err != nil {
				return "", err
			}
			summary = append(summary, t.summaryLine("M", change.path, change))
		case "move":
			if err := writeWithDirs(change.movePath, patch.JoinBOM(change.content, change.bom)); err != nil {
				return "", err
			}
			if err := os.Remove(change.path); err != nil && !os.IsNotExist(err) {
				return "", err
			}
			summary = append(summary, t.summaryLine("M", change.movePath, change))
		case "delete":
			if err := os.Remove(change.path); err != nil && !os.IsNotExist(err) {
				return "", err
			}
			summary = append(summary, t.summaryLine("D", change.path, change))
		}
		// Accumulated after the write succeeds, so a failure partway through
		// does not report a diff for changes that never landed.
		if change.diff != "" {
			target := change.path
			if change.movePath != "" {
				target = change.movePath
			}
			combined.WriteString("--- " + t.relative(target) + "\n")
			combined.WriteString(strings.TrimRight(change.diff, "\n") + "\n")
		}
	}
	out := "Success. Updated the following files:\n" + strings.Join(summary, "\n")
	if combined.Len() > 0 {
		out += "\n\n```diff\n" + strings.Join(
			truncateDiff(combined.String(), maxPatchDiffLines), "\n") + "\n```"
	}
	// A patch can touch several files, so each one is reported separately. A
	// deleted file has nothing to diagnose, and a moved one is diagnosed at
	// its new path.
	for _, change := range changes {
		switch change.kind {
		case "add", "update":
			out += diagnosticsFooter(ctx, t.diagnoser, change.path)
		case "move":
			out += diagnosticsFooter(ctx, t.diagnoser, change.movePath)
		}
	}
	return out, nil
}

// maxPatchDiffLines bounds the echoed diff. A patch can touch many files, and
// the model does not need every line read back to it.
const maxPatchDiffLines = 120

// newFileChange resolves one operation and computes its diff up front, so a
// patch that fails verification never writes and never reports a diff.
func newFileChange(target, kind, oldText, newText string, bom bool) fileChange {
	return fileChange{
		path:    target,
		kind:    kind,
		oldText: oldText,
		content: newText,
		bom:     bom,
		diff:    diff.Trim(diff.Unified(target, target, oldText, newText)),
		stat:    diff.Count(oldText, newText),
	}
}

// summaryLine renders one file's status line and accumulates its diff.
func (t *ApplyPatchTool) summaryLine(marker, path string, change fileChange) string {
	return fmt.Sprintf("%s %s (+%d -%d)", marker, t.relative(path), change.stat.Additions, change.stat.Deletions)
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
