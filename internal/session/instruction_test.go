package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInstructionsProjectFirstNameWins(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "root agents")
	writeFile(t, filepath.Join(root, "a", "AGENTS.md"), "nested agents")
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "claude")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	in := &Instructions{Directory: deep, Worktree: root}
	got := in.Paths()
	want := []string{filepath.Join(root, "a", "AGENTS.md"), filepath.Join(root, "AGENTS.md")}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("paths = %v, want %v (AGENTS.md at every level, CLAUDE.md not stacked)", got, want)
	}
}

func TestInstructionsClaudeFallbackAndDisable(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "claude")

	in := &Instructions{Directory: root, Worktree: root}
	if got := in.Paths(); len(got) != 1 || filepath.Base(got[0]) != "CLAUDE.md" {
		t.Fatalf("paths = %v, want the CLAUDE.md fallback", got)
	}
	in.DisableClaudeCode = true
	if got := in.Paths(); len(got) != 0 {
		t.Fatalf("paths = %v, want none with CLAUDE.md disabled", got)
	}
}

func TestInstructionsGlobalFirstMatchOnly(t *testing.T) {
	config := t.TempDir()
	home := t.TempDir()
	writeFile(t, filepath.Join(config, "AGENTS.md"), "global agents")
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "global claude")

	in := &Instructions{GlobalConfig: config, Home: home}
	got := in.Paths()
	if len(got) != 1 || got[0] != filepath.Join(config, "AGENTS.md") {
		t.Fatalf("paths = %v, want only the global AGENTS.md", got)
	}
}

func TestInstructionsConfigEntries(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	writeFile(t, filepath.Join(root, "docs", "style.md"), "style")
	writeFile(t, filepath.Join(root, "docs", "rules.md"), "rules")
	writeFile(t, filepath.Join(home, "notes.md"), "notes")
	abs := filepath.Join(t.TempDir(), "abs.md")
	writeFile(t, abs, "abs")

	in := &Instructions{
		Directory: root,
		Worktree:  root,
		Home:      home,
		Config:    []string{"docs/*.md", "~/notes.md", abs, "https://example.com/x.md"},
	}
	got := in.Paths()
	if len(got) != 4 {
		t.Fatalf("paths = %v, want two globbed, the ~/ one and the absolute one (URL skipped)", got)
	}
	blocks := in.System()
	if len(blocks) != 4 || !strings.HasPrefix(blocks[0], "Instructions from: ") {
		t.Fatalf("blocks = %q", blocks)
	}
}

func TestInstructionsNilAndEmptyFiles(t *testing.T) {
	var in *Instructions
	if in.Paths() != nil || in.System() != nil {
		t.Fatal("nil Instructions must render nothing")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "  \n")
	got := (&Instructions{Directory: root, Worktree: root}).System()
	if len(got) != 0 {
		t.Fatalf("an empty file must not render a block, got %q", got)
	}
}
