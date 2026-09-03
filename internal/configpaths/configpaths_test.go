package configpaths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilesReversed(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gocode.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "gocode.jsonc"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Files("gocode", deep, root)
	// up() yields deepest-first; Files reverses so the outermost (root) is first
	want := []string{
		filepath.Join(root, "gocode.json"),
		filepath.Join(deep, "gocode.jsonc"),
	}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: expected %s, got %s", i, want[i], got[i])
		}
	}
}

func TestDirectoriesIncludesConfigAndProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOCODE_TEST_HOME", home)
	t.Setenv("GOCODE_CONFIG_DIR", "")
	t.Setenv("GOCODE_DISABLE_PROJECT_CONFIG", "")

	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(project, ".gocode"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Directories(project)
	found := false
	for _, d := range got {
		if d == filepath.Join(project, ".gocode") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected project .gocode in %v", got)
	}
}

func TestDirectoriesRespectsDisableProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOCODE_TEST_HOME", home)
	t.Setenv("GOCODE_DISABLE_PROJECT_CONFIG", "true")
	t.Setenv("GOCODE_CONFIG_DIR", "")

	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(project, ".gocode"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Directories(project)
	for _, d := range got {
		if d == filepath.Join(project, ".gocode") {
			t.Fatalf("project .gocode should be excluded when disabled, got %v", got)
		}
	}
}

func TestFileInDirectory(t *testing.T) {
	dir := filepath.Join(string(filepath.Separator), "dir")
	got := FileInDirectory(dir, "gocode")
	want := []string{filepath.Join(dir, "gocode.json"), filepath.Join(dir, "gocode.jsonc")}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
