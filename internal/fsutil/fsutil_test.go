package fsutil

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFindUp(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{root, filepath.Join(root, "a"), deep} {
		if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got := FindUp("marker", deep, root)
	want := []string{
		filepath.Join(deep, "marker"),
		filepath.Join(root, "a", "marker"),
		filepath.Join(root, "marker"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestFindUpStopsAtStop(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// stop at "a" means root's marker is never reached
	got := FindUp("marker", deep, filepath.Join(root, "a"))
	if len(got) != 0 {
		t.Fatalf("expected no results, got %v", got)
	}
}

func TestUpMultipleTargets(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "x.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x.jsonc"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Up([]string{"x.jsonc", "x.json"}, deep, root)
	want := []string{
		filepath.Join(deep, "x.json"),
		filepath.Join(root, "x.jsonc"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestIsDirFile(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsDir(root) {
		t.Fatalf("expected %s to be a dir", root)
	}
	if !IsFile(file) {
		t.Fatalf("expected %s to be a file", file)
	}
	if IsDir(file) || IsFile(root) {
		t.Fatalf("dir/file detection inverted")
	}
}
