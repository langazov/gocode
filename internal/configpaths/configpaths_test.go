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
	if err := os.WriteFile(filepath.Join(root, "opencode.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "opencode.jsonc"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Files("opencode", deep, root)
	// up() yields deepest-first; Files reverses so the outermost (root) is first
	want := []string{
		filepath.Join(root, "opencode.json"),
		filepath.Join(deep, "opencode.jsonc"),
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
	t.Setenv("OPENCODE_TEST_HOME", home)
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "")

	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(project, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Directories(project)
	found := false
	for _, d := range got {
		if d == filepath.Join(project, ".opencode") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected project .opencode in %v", got)
	}
}

func TestDirectoriesRespectsDisableProjectConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("OPENCODE_TEST_HOME", home)
	t.Setenv("OPENCODE_DISABLE_PROJECT_CONFIG", "true")
	t.Setenv("OPENCODE_CONFIG_DIR", "")

	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(project, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := Directories(project)
	for _, d := range got {
		if d == filepath.Join(project, ".opencode") {
			t.Fatalf("project .opencode should be excluded when disabled, got %v", got)
		}
	}
}

func TestFileInDirectory(t *testing.T) {
	got := FileInDirectory("/dir", "opencode")
	want := []string{"/dir/opencode.json", "/dir/opencode.jsonc"}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("expected %v, got %v", want, got)
	}
}
