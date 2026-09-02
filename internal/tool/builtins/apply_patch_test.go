package builtins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newApplyPatch(t *testing.T) (*ApplyPatchTool, string) {
	t.Helper()
	root := t.TempDir()
	return NewApplyPatchTool(Resolver{Root: root}), root
}

func write(t *testing.T, root, name, content string) string {
	t.Helper()
	target := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return target
}

func read(t *testing.T, target string) string {
	t.Helper()
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestApplyPatchAddsFile(t *testing.T) {
	tool, root := newApplyPatch(t)
	out, err := tool.Execute(context.Background(), map[string]any{"patchText": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: nested/hello.txt",
		"+Hello world",
		"*** End Patch",
	}, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, "nested/hello.txt")); got != "Hello world\n" {
		t.Fatalf("wrote %q", got)
	}
	if !strings.Contains(out, "A nested/hello.txt") {
		t.Fatalf("summary missing the add: %q", out)
	}
}

func TestApplyPatchUpdatesFile(t *testing.T) {
	tool, root := newApplyPatch(t)
	write(t, root, "app.py", "def greet():\n    print(\"Hi\")\n")

	out, err := tool.Execute(context.Background(), map[string]any{"patchText": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: app.py",
		"@@ def greet():",
		`-    print("Hi")`,
		`+    print("Hello, world!")`,
		"*** End Patch",
	}, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	want := "def greet():\n    print(\"Hello, world!\")\n"
	if got := read(t, filepath.Join(root, "app.py")); got != want {
		t.Fatalf("wrote %q, want %q", got, want)
	}
	if !strings.Contains(out, "M app.py") {
		t.Fatalf("summary missing the update: %q", out)
	}
}

func TestApplyPatchMovesFile(t *testing.T) {
	tool, root := newApplyPatch(t)
	write(t, root, "src/app.py", "value = 1\n")

	if _, err := tool.Execute(context.Background(), map[string]any{"patchText": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: src/app.py",
		"*** Move to: src/main.py",
		"@@",
		"-value = 1",
		"+value = 2",
		"*** End Patch",
	}, "\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "src/app.py")); !os.IsNotExist(err) {
		t.Fatal("the original file survived the move")
	}
	if got := read(t, filepath.Join(root, "src/main.py")); got != "value = 2\n" {
		t.Fatalf("moved file contains %q", got)
	}
}

func TestApplyPatchDeletesFile(t *testing.T) {
	tool, root := newApplyPatch(t)
	write(t, root, "obsolete.txt", "junk\n")
	if _, err := tool.Execute(context.Background(), map[string]any{"patchText": strings.Join([]string{
		"*** Begin Patch",
		"*** Delete File: obsolete.txt",
		"*** End Patch",
	}, "\n")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatal("file was not deleted")
	}
}

// TestApplyPatchIsAtomic is the important guarantee: a patch whose second hunk
// cannot apply must leave the first hunk's file untouched.
func TestApplyPatchIsAtomic(t *testing.T) {
	tool, root := newApplyPatch(t)
	write(t, root, "good.txt", "keep\n")

	_, err := tool.Execute(context.Background(), map[string]any{"patchText": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: created.txt",
		"+new file",
		"*** Update File: good.txt",
		"@@",
		"-this line is not in the file",
		"+replacement",
		"*** End Patch",
	}, "\n")})
	if err == nil {
		t.Fatal("expected the patch to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(root, "created.txt")); !os.IsNotExist(statErr) {
		t.Fatal("a file was created even though the patch failed")
	}
	if got := read(t, filepath.Join(root, "good.txt")); got != "keep\n" {
		t.Fatalf("existing file was modified by a failed patch: %q", got)
	}
}

func TestApplyPatchRejectsEmptyPatch(t *testing.T) {
	tool, _ := newApplyPatch(t)
	_, err := tool.Execute(context.Background(), map[string]any{
		"patchText": "*** Begin Patch\n*** End Patch",
	})
	if err == nil || !strings.Contains(err.Error(), "empty patch") {
		t.Fatalf("expected an empty-patch rejection, got %v", err)
	}
}

func TestApplyPatchRejectsMissingUpdateTarget(t *testing.T) {
	tool, _ := newApplyPatch(t)
	_, err := tool.Execute(context.Background(), map[string]any{"patchText": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: nope.txt",
		"@@",
		"-x",
		"+y",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "Failed to read file to update") {
		t.Fatalf("expected a missing-target error, got %v", err)
	}
}

// TestApplyPatchRejectsEscapingPath guards the sandbox: a patch must not write
// outside the tool's root.
func TestApplyPatchRejectsEscapingPath(t *testing.T) {
	tool, root := newApplyPatch(t)
	_, err := tool.Execute(context.Background(), map[string]any{"patchText": strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: ../escaped.txt",
		"+nope",
		"*** End Patch",
	}, "\n")})
	if err == nil || !strings.Contains(err.Error(), "escapes working directory") {
		t.Fatalf("expected the escape to be rejected, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(root), "escaped.txt")); !os.IsNotExist(statErr) {
		t.Fatal("a file was written outside the root")
	}
}

func TestApplyPatchPreservesBOM(t *testing.T) {
	tool, root := newApplyPatch(t)
	write(t, root, "bom.txt", "\uFEFFalpha\n")
	if _, err := tool.Execute(context.Background(), map[string]any{"patchText": strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: bom.txt",
		"@@",
		"-alpha",
		"+beta",
		"*** End Patch",
	}, "\n")}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, filepath.Join(root, "bom.txt")); got != "\uFEFFbeta\n" {
		t.Fatalf("BOM was not preserved: %q", got)
	}
}

func TestApplyPatchRequiresPatchText(t *testing.T) {
	tool, _ := newApplyPatch(t)
	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil {
		t.Fatal("expected patchText to be required")
	}
}
