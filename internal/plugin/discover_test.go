package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// discoverHome isolates the config directories Discover walks: the global
// install root under a temp home, and a project .gocode beside it.
func discoverHome(t *testing.T) (home, project string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("GOCODE_TEST_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GOCODE_CONFIG_DIR", "")
	t.Setenv("GOCODE_DISABLE_PROJECT_CONFIG", "")
	project = filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, project
}

// writeExecutable creates a runnable file, or a plain one when mode says so.
func writeExecutable(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

// The global install root's entries are reported by bare name — the ref the
// loader resolves and `make install-plugin` writes.
func TestDiscoverFindsExecutablesInInstallRoot(t *testing.T) {
	discoverHome(t)
	root := InstallRoot()
	writeExecutable(t, filepath.Join(root, "lint"), 0o755)
	// A directory plugin: the loader's fallback entrypoint is an executable
	// named `plugin` inside it.
	writeExecutable(t, filepath.Join(root, "rag", "plugin"), 0o755)

	found := Discover(t.TempDir())
	if len(found) != 2 {
		t.Fatalf("found %+v, want lint and rag", found)
	}
	byName := map[string]Available{}
	for _, entry := range found {
		byName[entry.Name] = entry
	}
	if got := byName["lint"].Ref; got != "lint" {
		t.Errorf("install-root ref = %q, want the bare name", got)
	}
	if got := byName["rag"].Path; got != filepath.Join(root, "rag") {
		t.Errorf("path = %q, want the plugin directory", got)
	}
}

// Anything the loader could not start is not offered: enabling it could only
// fail, so it is not a choice.
func TestDiscoverSkipsWhatCannotRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits carry no meaning on windows")
	}
	discoverHome(t)
	root := InstallRoot()
	writeExecutable(t, filepath.Join(root, "README.md"), 0o644)
	writeExecutable(t, filepath.Join(root, ".gitkeep"), 0o755)
	// A directory with neither a manifest nor an executable named `plugin`.
	writeExecutable(t, filepath.Join(root, "source", "main.go"), 0o644)
	writeExecutable(t, filepath.Join(root, "real"), 0o755)

	found := Discover(t.TempDir())
	if len(found) != 1 || found[0].Name != "real" {
		t.Fatalf("found %+v, want just the executable", found)
	}
}

// A project's own .gocode/plugin folder is scanned too, and reported by path:
// a bare name would resolve against the global root, which is not where it is.
func TestDiscoverFindsProjectPluginsByPath(t *testing.T) {
	_, project := discoverHome(t)
	path := filepath.Join(project, ".gocode", "plugin", "local")
	writeExecutable(t, path, 0o755)

	found := Discover(project)
	if len(found) != 1 {
		t.Fatalf("found %+v, want the project plugin", found)
	}
	if found[0].Ref != path {
		t.Errorf("project ref = %q, want the absolute path %q", found[0].Ref, path)
	}
}

// A name installed in two folders is reported once, from the first — the
// same precedence config-directory order implies everywhere else.
func TestDiscoverReportsDuplicateNameOnce(t *testing.T) {
	_, project := discoverHome(t)
	writeExecutable(t, filepath.Join(InstallRoot(), "lint"), 0o755)
	writeExecutable(t, filepath.Join(project, ".gocode", "plugin", "lint"), 0o755)

	found := Discover(project)
	if len(found) != 1 {
		t.Fatalf("found %+v, want one lint", found)
	}
	if found[0].Root != InstallRoot() {
		t.Errorf("kept %q, want the global install root to win", found[0].Root)
	}
}

// Installed drops what the config already names, however the config spells
// it — bare name or path.
func TestInstalledExcludesConfiguredPlugins(t *testing.T) {
	discoverHome(t)
	root := InstallRoot()
	writeExecutable(t, filepath.Join(root, "lint"), 0o755)
	writeExecutable(t, filepath.Join(root, "rag"), 0o755)
	writeExecutable(t, filepath.Join(root, "spare"), 0o755)
	directory := t.TempDir()

	got := Installed([]Spec{
		{Ref: "lint"},                     // by bare name
		{Ref: filepath.Join(root, "rag")}, // the same plugin, by path
		{Ref: "/nowhere/missing"},         // configured but absent
	}, directory)

	if len(got) != 1 || got[0].Name != "spare" {
		t.Fatalf("installed = %+v, want just spare", got)
	}
}

// No plugin folder anywhere is the normal case, not an error.
func TestDiscoverToleratesMissingFolders(t *testing.T) {
	discoverHome(t)
	if found := Discover(t.TempDir()); len(found) != 0 {
		t.Fatalf("found %+v, want nothing", found)
	}
}
