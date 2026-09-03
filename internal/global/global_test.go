package global

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveXdgDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// os.UserHomeDir reads %USERPROFILE% on Windows, not $HOME.
	t.Setenv("USERPROFILE", home)
	t.Setenv("GOCODE_TEST_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	paths := Resolve()
	if paths.Data != filepath.Join(home, ".local", "share", "gocode") {
		t.Fatalf("unexpected data path: %s", paths.Data)
	}
	if paths.Config != filepath.Join(home, ".config", "gocode") {
		t.Fatalf("unexpected config path: %s", paths.Config)
	}
	if paths.Cache != filepath.Join(home, ".cache", "gocode") {
		t.Fatalf("unexpected cache path: %s", paths.Cache)
	}
	if paths.State != filepath.Join(home, ".local", "state", "gocode") {
		t.Fatalf("unexpected state path: %s", paths.State)
	}
	if paths.Bin != filepath.Join(paths.Cache, "bin") {
		t.Fatalf("unexpected bin path: %s", paths.Bin)
	}
	if paths.Log != filepath.Join(paths.Data, "log") {
		t.Fatalf("unexpected log path: %s", paths.Log)
	}
	if paths.Repos != filepath.Join(paths.Data, "repos") {
		t.Fatalf("unexpected repos path: %s", paths.Repos)
	}
	if !strings.HasSuffix(paths.Tmp, "gocode") {
		t.Fatalf("unexpected tmp path: %s", paths.Tmp)
	}
}

func TestResolveXdgOverrides(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	paths := Resolve()
	if paths.Data != filepath.Join(data, "gocode") {
		t.Fatalf("expected XDG_DATA_HOME override, got %s", paths.Data)
	}
}

func TestResolveTestHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GOCODE_TEST_HOME", home)
	if got := Resolve().Home; got != home {
		t.Fatalf("expected GOCODE_TEST_HOME %s, got %s", home, got)
	}
}

func TestMakeAppliesConfigDirFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GOCODE_CONFIG_DIR", dir)
	if got := Make().Config; got != dir {
		t.Fatalf("expected config dir %s, got %s", dir, got)
	}
}

func TestInitCreatesDirs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))

	if err := Init(); err != nil {
		t.Fatal(err)
	}
	paths := Resolve()
	for _, dir := range []string{paths.Data, paths.Config, paths.State, paths.Bin, paths.Log, paths.Repos} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("expected dir %s to exist", dir)
		}
	}
}
