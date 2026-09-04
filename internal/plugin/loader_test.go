package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/config"
)

func withNatives(t *testing.T) {
	t.Helper()
	resetNatives()
	t.Cleanup(resetNatives)
}

// The native tier loads before configured plugins, so a built-in establishes
// defaults a user's plugin can override.
func TestLoadRunsNativesFirstInRegistrationOrder(t *testing.T) {
	withNatives(t)
	var order []string
	Register("second", func(context.Context, Input, Options) (*Hooks, error) {
		order = append(order, "second")
		return &Hooks{}, nil
	})
	Register("first", func(context.Context, Input, Options) (*Hooks, error) {
		order = append(order, "first")
		return &Hooks{}, nil
	})

	host, err := Load(context.Background(), LoadInput{Input: Input{Directory: t.TempDir()}})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(order) != 2 || order[0] != "second" || order[1] != "first" {
		t.Errorf("load order = %v, want registration order [second first]", order)
	}
	if len(host.Instances()) != 2 {
		t.Errorf("host has %d instances, want 2", len(host.Instances()))
	}
}

// A native factory that fails is reported and skipped; the rest still load.
func TestLoadSkipsFailedNative(t *testing.T) {
	withNatives(t)
	Register("broken", func(context.Context, Input, Options) (*Hooks, error) {
		return nil, os.ErrPermission
	})
	Register("fine", func(context.Context, Input, Options) (*Hooks, error) {
		return &Hooks{}, nil
	})

	var failures []string
	host, err := Load(context.Background(), LoadInput{
		Input: Input{Directory: t.TempDir()},
		Report: &Report{Error: func(spec Spec, stage Stage, err error) {
			failures = append(failures, spec.Ref+"@"+string(stage))
		}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(failures) != 1 || failures[0] != "broken@load" {
		t.Errorf("failures = %v, want [broken@load]", failures)
	}
	if len(host.Instances()) != 1 || host.Instances()[0].ID != "fine" {
		t.Errorf("instances = %+v, want only the working plugin", host.Instances())
	}
}

// --pure disables the configured plugins but not the built-in tier, which is
// part of the binary rather than the user's environment.
func TestLoadPureKeepsNatives(t *testing.T) {
	withNatives(t)
	Register("builtin", func(context.Context, Input, Options) (*Hooks, error) { return &Hooks{}, nil })

	host, err := Load(context.Background(), LoadInput{
		Input: Input{Directory: t.TempDir()},
		Specs: []Spec{{Ref: "./nothing-here"}},
		Pure:  true,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(host.Instances()) != 1 || host.Instances()[0].ID != "builtin" {
		t.Errorf("instances = %+v, want only the native plugin", host.Instances())
	}
}

// A bare name matching the native registry resolves to it; the "native:"
// prefix forces that lookup even when a like-named path exists.
func TestResolveNative(t *testing.T) {
	withNatives(t)
	Register("review", func(context.Context, Input, Options) (*Hooks, error) { return &Hooks{}, nil })

	resolved, _, err := Resolve(Spec{Ref: "review"}, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Source != SourceNative || resolved.Target != "review" {
		t.Errorf("resolved = %+v, want the native plugin", resolved)
	}

	if _, stage, err := Resolve(Spec{Ref: "native:absent"}, t.TempDir()); err == nil {
		t.Error("Resolve accepted an unregistered native plugin")
	} else if stage != StageResolve {
		t.Errorf("stage = %q, want %q", stage, StageResolve)
	}
}

// A directory plugin declares how it runs in its manifest, and a relative
// command in that manifest names a file inside the plugin rather than
// something on PATH.
func TestResolveDirectoryManifest(t *testing.T) {
	withNatives(t)
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "lint")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	descriptor := map[string]any{
		"command": []string{"./run.sh", "--strict"},
		"env":     map[string]string{"LINT_MODE": "ci"},
	}
	raw, _ := json.Marshal(descriptor)
	if err := os.WriteFile(filepath.Join(pluginDir, manifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, _, err := Resolve(Spec{Ref: "./lint"}, dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Source != SourceProcess {
		t.Errorf("Source = %q, want %q", resolved.Source, SourceProcess)
	}
	wantCommand := filepath.Join(pluginDir, "run.sh")
	if len(resolved.Command) != 2 || resolved.Command[0] != wantCommand || resolved.Command[1] != "--strict" {
		t.Errorf("Command = %v, want [%s --strict]", resolved.Command, wantCommand)
	}
	if len(resolved.Env) != 1 || resolved.Env[0] != "LINT_MODE=ci" {
		t.Errorf("Env = %v, want [LINT_MODE=ci]", resolved.Env)
	}
}

// A directory with no manifest falls back to an executable named `plugin`.
func TestResolveDirectoryFallback(t *testing.T) {
	withNatives(t)
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, "fallback")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "plugin"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(pluginDir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	resolved, _, err := Resolve(Spec{Ref: "./fallback"}, dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved.Command) != 1 || filepath.Base(resolved.Command[0]) != name {
		t.Errorf("Command = %v, want the fallback executable", resolved.Command)
	}
}

// A directory with neither fails at the entry stage, not the resolve stage:
// the plugin was found, it just has no way to run.
func TestResolveDirectoryWithNoEntrypoint(t *testing.T) {
	withNatives(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, stage, err := Resolve(Spec{Ref: "./empty"}, dir)
	if err == nil {
		t.Fatal("Resolve accepted a directory with no entrypoint")
	}
	if stage != StageEntry {
		t.Errorf("stage = %q, want %q", stage, StageEntry)
	}
}

// The install directory is the contract `make install-plugin` writes into, so
// it is pinned here: global.Paths.Config already ends in the app name, and
// appending a second one would send the loader hunting in
// ~/.config/gocode/gocode/plugin.
func TestInstallDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join("/xdg", "config"))

	want := filepath.Join("/xdg", "config", "gocode", "plugin")
	if got := InstallRoot(); got != want {
		t.Errorf("InstallRoot() = %q, want %q", got, want)
	}
	if got := InstallDir("lint"); got != filepath.Join(want, "lint") {
		t.Errorf("InstallDir(lint) = %q, want %q", got, filepath.Join(want, "lint"))
	}
}

// A bare name resolves to an installed plugin, which is what makes
// `make install-plugin` followed by "plugin": ["plugin-echo"] work.
func TestResolveInstalledByBareName(t *testing.T) {
	withNatives(t)
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	installed := filepath.Join(root, "gocode", "plugin", "installed-example")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"command": []string{"./run"}})
	if err := os.WriteFile(filepath.Join(installed, manifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	resolved, _, err := Resolve(Spec{Ref: "installed-example"}, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Source != SourceProcess || resolved.Target != installed {
		t.Errorf("resolved = %+v, want the installed plugin at %s", resolved, installed)
	}
}

// A name that is neither built in nor on disk says so, rather than reaching
// for a registry — this port installs nothing at runtime.
func TestResolveUninstalledName(t *testing.T) {
	withNatives(t)
	_, stage, err := Resolve(Spec{Ref: "some-published-plugin"}, t.TempDir())
	if err == nil {
		t.Fatal("Resolve accepted a plugin that is nowhere")
	}
	if stage != StageResolve {
		t.Errorf("stage = %q, want %q", stage, StageResolve)
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v, want it to say the plugin is not installed", err)
	}
}

// A non-executable file is rejected before it is spawned, so the failure names
// the real problem instead of surfacing an exec error later.
func TestResolveNonExecutableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits carry no executable meaning on Windows")
	}
	withNatives(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.js")
	if err := os.WriteFile(path, []byte("// not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stage, err := Resolve(Spec{Ref: "./plugin.js"}, dir)
	if err == nil {
		t.Fatal("Resolve accepted a non-executable file")
	}
	if stage != StageEntry {
		t.Errorf("stage = %q, want %q", stage, StageEntry)
	}
}

// Both config forms parse, and the options bag reaches the loader spec.
func TestSpecsFromConfig(t *testing.T) {
	var cfg struct {
		Plugin []config.PluginSpec `json:"plugin"`
	}
	raw := `{"plugin": ["./bare", ["review", {"strict": true}]]}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	specs := Specs(cfg.Plugin)
	if len(specs) != 2 {
		t.Fatalf("Specs returned %d entries, want 2", len(specs))
	}
	if specs[0].Ref != "./bare" || specs[0].Options != nil {
		t.Errorf("specs[0] = %+v, want the bare form with no options", specs[0])
	}
	if specs[1].Ref != "review" {
		t.Errorf("specs[1].Ref = %q, want review", specs[1].Ref)
	}
	if strict, _ := specs[1].Options["strict"].(bool); !strict {
		t.Errorf("specs[1].Options = %v, want strict:true", specs[1].Options)
	}
}

// A config round trip preserves whichever form the user wrote.
func TestPluginSpecMarshalRoundTrip(t *testing.T) {
	for _, raw := range []string{`"./bare"`, `["review",{"strict":true}]`} {
		var spec config.PluginSpec
		if err := json.Unmarshal([]byte(raw), &spec); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		out, err := json.Marshal(spec)
		if err != nil {
			t.Fatalf("marshal %s: %v", raw, err)
		}
		if string(out) != raw {
			t.Errorf("round trip of %s produced %s", raw, out)
		}
	}
}
