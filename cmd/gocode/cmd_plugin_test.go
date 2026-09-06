package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/plugin"
)

// pluginListHome points the global config at a scratch tree and returns its
// plugin folder.
//
// XDG_CONFIG_HOME is the knob that matters — global.Resolve derives Config
// from it, and GOCODE_TEST_HOME does not redirect it — so the resolved path is
// asserted before the test runs. Getting this wrong means the test reads (and
// a sibling command could rewrite) the developer's own config, which is the
// same trap internal/configedit's withHome guards against.
func pluginListHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("GOCODE_CONFIG_CONTENT", "")
	// Keep the bundled search path out of it: the test binary's own directory
	// is one of the roots, and what sits beside it is not this test's business.
	t.Setenv(plugin.PluginPathEnv, t.TempDir())

	if resolved := plugin.InstallRoot(); !strings.HasPrefix(resolved, root) {
		t.Fatalf("refusing to run: install root resolves to %s, outside %s", resolved, root)
	}
	folder := filepath.Join(root, "gocode", "plugin")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	return folder
}

func writeGlobalConfig(t *testing.T, body string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gocode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gocode.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// installPluginDir creates a loadable plugin: a manifest naming an executable
// beside it, the layout `make install-plugin` produces.
func installPluginDir(t *testing.T, folder, name string) string {
	t.Helper()
	dir := filepath.Join(folder, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"command": ["./` + name + `"]}`
	if err := os.WriteFile(filepath.Join(dir, "gocode-plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runListCommand(t *testing.T) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runPluginList(&clix.Args{}) })
	if err != nil {
		t.Fatalf("runPluginList: %v", err)
	}
	return out
}

func TestPluginListReportsConfiguredAndAvailable(t *testing.T) {
	folder := pluginListHome(t)
	enabled := installPluginDir(t, folder, "enabled-example")
	installPluginDir(t, folder, "spare-example")
	writeGlobalConfig(t, `{"plugin": ["enabled-example"]}`)

	out := runListCommand(t)

	if !strings.Contains(out, "enabled-example") || !strings.Contains(out, enabled) {
		t.Errorf("configured plugin should be listed with its resolved path, got:\n%s", out)
	}
	// The spare is installed but unconfigured, which is the state the
	// "installed but not enabled" section exists to surface.
	if !strings.Contains(out, "Installed but not enabled") || !strings.Contains(out, "spare-example") {
		t.Errorf("unconfigured install should be offered, got:\n%s", out)
	}
	if !strings.Contains(out, "gocode plugin enable") {
		t.Errorf("output should say how to enable one, got:\n%s", out)
	}
}

// A configured plugin that cannot resolve is the case this command exists to
// diagnose, so it must be shown with its reason — and must not be quietly
// omitted the way the live /api/plugin list omits it.
func TestPluginListReportsUnresolvableEntries(t *testing.T) {
	pluginListHome(t)
	writeGlobalConfig(t, `{"plugin": ["ghost"]}`)

	out := runListCommand(t)

	if !strings.Contains(out, "ghost") {
		t.Fatalf("the broken entry should be listed, got:\n%s", out)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("the reason should be shown, got:\n%s", out)
	}
	if !strings.Contains(out, "could not be resolved") {
		t.Errorf("a summary should warn it will not load, got:\n%s", out)
	}
}

// A native is loaded whether or not the config names it, so it belongs in the
// built-in section — and naming it in the config only supplies options, so it
// must then appear once, under Configured, not twice.
func TestPluginListSeparatesBuiltinsFromConfigured(t *testing.T) {
	pluginListHome(t)
	natives := plugin.NativeNamesSorted()
	if len(natives) == 0 {
		t.Skip("no native plugins registered in this build")
	}
	name := natives[0]

	writeGlobalConfig(t, `{"plugin": []}`)
	out := runListCommand(t)
	if !strings.Contains(out, "Built-in") || !strings.Contains(out, name) {
		t.Fatalf("an unconfigured native should be listed as built-in, got:\n%s", out)
	}

	writeGlobalConfig(t, `{"plugin": ["`+name+`"]}`)
	out = runListCommand(t)
	if strings.Count(out, name) != 1 {
		t.Errorf("a configured native should be listed once, got %d mentions:\n%s", strings.Count(out, name), out)
	}
	if strings.Contains(out, "Built-in") {
		t.Errorf("the built-in section should be empty once the only native is configured, got:\n%s", out)
	}
}

func TestPluginListWithNothingConfigured(t *testing.T) {
	pluginListHome(t)
	writeGlobalConfig(t, `{"plugin": []}`)

	out := runListCommand(t)
	if !strings.Contains(out, "none") {
		t.Errorf("an empty plugin array should say so, got:\n%s", out)
	}
}

// The command only reads. It is run to diagnose a broken config, so it must
// never rewrite the file it is reporting on.
func TestPluginListDoesNotModifyConfig(t *testing.T) {
	pluginListHome(t)
	body := `{"plugin": ["ghost"]}`
	writeGlobalConfig(t, body)
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gocode", "gocode.json")

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	runListCommand(t)

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("config mtime changed: %v -> %v", before.ModTime(), after.ModTime())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body {
		t.Errorf("config was rewritten:\n%s", data)
	}
}
