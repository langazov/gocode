package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/langazov/gocode-go/internal/global"
)

// The loader, porting packages/opencode/src/plugin/loader.ts.
//
// TypeScript's pipeline is: normalize each config entry into a plan, resolve
// the plan to a concrete entrypoint (installing from npm if needed), check the
// declared opencode version range, then `import()` it. Failures are reported
// per stage so the user is told which step refused, and one bad plugin never
// stops the others from loading.
//
// The stages survive here. What changes is resolution, and it changes for a
// reason already documented in documentation/10-development.md: this port does
// no runtime npm install. A configured plugin is either compiled in or
// already on disk. So `resolve` looks in three places instead of reaching for
// a registry, and a name that is nowhere fails with "not installed" rather
// than being fetched.

// Stage names the step a load failed at, porting the `stage` union.
type Stage string

const (
	// StageResolve covers finding the plugin on disk or in the native
	// registry. Ports TypeScript's "install" stage.
	StageResolve Stage = "resolve"
	// StageEntry covers finding a runnable entrypoint inside a resolved
	// target.
	StageEntry Stage = "entry"
	// StageLoad covers running the factory or completing the handshake.
	StageLoad Stage = "load"
)

// Spec is one normalized config entry, porting `PluginLoader.Plan`.
type Spec struct {
	// Ref is the config string: a native name, a path, or a bare name looked
	// up in the plugin directory.
	Ref string
	// Options is the settings bag from a `["ref", {...}]` config entry.
	Options Options
}

// Resolved is a spec located concretely, porting `PluginLoader.Resolved`.
type Resolved struct {
	Spec
	Source Source
	// Target is the native plugin name, or the plugin's directory or file.
	Target string
	// Command is what to execute, for a process plugin.
	Command []string
	// Env is the environment overlay for a process plugin.
	Env []string
}

// Report is the loader's diagnostic seam, porting the `report` callbacks. It
// exists so the CLI can print load failures and the TUI can publish them as
// session errors, without the loader knowing which.
type Report struct {
	// Start fires before each attempt.
	Start func(spec Spec)
	// Error fires when an attempt fails, naming the stage that refused.
	Error func(spec Spec, stage Stage, err error)
	// Loaded fires for each successfully loaded plugin.
	Loaded func(instance *Instance)
}

func (r *Report) start(spec Spec) {
	if r != nil && r.Start != nil {
		r.Start(spec)
	}
}

func (r *Report) fail(spec Spec, stage Stage, err error) {
	if r != nil && r.Error != nil {
		r.Error(spec, stage, err)
	}
}

func (r *Report) loaded(instance *Instance) {
	if r != nil && r.Loaded != nil {
		r.Loaded(instance)
	}
}

// LoadInput configures a load pass.
type LoadInput struct {
	// Input is handed to every plugin factory and handshake.
	Input Input
	// Specs are the configured plugins, in config order.
	Specs []Spec
	// Pure disables the configured plugins, porting the `--pure` flag's
	// effect on the TypeScript host. Native plugins still load, because they
	// are part of the binary rather than the user's environment.
	Pure bool
	// DisableNative skips the built-in tier, porting `disableDefaultPlugins`.
	DisableNative bool
	// Report receives per-plugin diagnostics.
	Report *Report
	// Log receives a plugin's own output, both its log notifications and its
	// stderr. Point it at global.LogBackground: a plugin writing to the
	// terminal would corrupt the TUI's frame.
	Log func(message string)
}

// Load builds a host from the native tier and the configured specs, in that
// order. Order is the contract: a hook registered later sees the earlier
// hook's mutations, so built-ins establish defaults a user's plugin overrides.
//
// A plugin that fails to load is reported and skipped. Load returns an error
// only if it could not proceed at all, which today it cannot.
func Load(ctx context.Context, in LoadInput) (*Host, error) {
	log := in.Log
	if log == nil {
		log = func(string) {}
	}
	host := NewHost(func(pluginID, hook string, err error) {
		log(fmt.Sprintf("plugin %s: hook %s failed: %v", pluginID, hook, err))
	})

	if !in.DisableNative {
		for _, name := range Natives() {
			spec := Spec{Ref: name}
			in.Report.start(spec)
			factory, _ := Native(name)
			instance, err := loadNative(ctx, name, factory, in.Input, nil)
			if err != nil {
				in.Report.fail(spec, StageLoad, err)
				continue
			}
			host.Add(instance)
			in.Report.loaded(instance)
		}
	}

	if in.Pure {
		return host, nil
	}

	for _, spec := range in.Specs {
		if spec.Ref == "" {
			continue
		}
		in.Report.start(spec)
		instance, stage, err := load(ctx, spec, in.Input, log)
		if err != nil {
			in.Report.fail(spec, stage, err)
			continue
		}
		host.Add(instance)
		in.Report.loaded(instance)
	}
	return host, nil
}

// load runs one spec through resolve-then-load, returning the stage that
// failed so the report can say which step refused.
func load(ctx context.Context, spec Spec, in Input, log func(string)) (*Instance, Stage, error) {
	resolved, stage, err := Resolve(spec, in.Directory)
	if err != nil {
		return nil, stage, err
	}
	switch resolved.Source {
	case SourceNative:
		factory, ok := Native(resolved.Target)
		if !ok {
			return nil, StageResolve, fmt.Errorf("native plugin %q is not registered", resolved.Target)
		}
		instance, err := loadNative(ctx, resolved.Target, factory, in, spec.Options)
		if err != nil {
			return nil, StageLoad, err
		}
		instance.Spec = spec.Ref
		return instance, "", nil
	default:
		instance, err := Spawn(ctx, spec.Ref, SpawnConfig{
			Command: resolved.Command,
			Dir:     in.Directory,
			Env:     resolved.Env,
		}, in, spec.Options, log)
		if err != nil {
			return nil, StageLoad, err
		}
		return instance, "", nil
	}
}

// loadNative runs a native factory. A factory returning nil hooks has opted
// out, which is legal and yields an instance with nothing registered.
func loadNative(ctx context.Context, name string, factory Plugin, in Input, opts Options) (*Instance, error) {
	hooks, err := factory(ctx, in, opts)
	if err != nil {
		return nil, err
	}
	if hooks == nil {
		hooks = &Hooks{}
	}
	return &Instance{ID: name, Spec: name, Source: SourceNative, Hooks: hooks}, nil
}

// manifestFile is the descriptor a plugin directory uses to say how it runs.
// It is this port's answer to package.json's `exports`: the thing TypeScript
// reads to find an entrypoint, reduced to the one question that matters when
// the entrypoint is a process rather than a module.
const manifestFile = "gocode-plugin.json"

// descriptor is the parsed manifestFile.
type descriptor struct {
	// Command is the executable and arguments to run, relative to the plugin
	// directory unless absolute.
	Command []string `json:"command"`
	// Env are extra environment variables, as a map.
	Env map[string]string `json:"env,omitempty"`
}

// Resolve locates a spec, porting `PluginLoader.resolve`. It looks, in order:
//
//  1. the native registry, for a bare name or a "native:" prefix;
//  2. the filesystem, for a path-like spec, relative to the session directory;
//  3. the plugin directory (~/.config/gocode/plugin/<name>), for a bare name.
//
// There is no fourth step. TypeScript's would be npm.
func Resolve(spec Spec, directory string) (Resolved, Stage, error) {
	ref := spec.Ref
	if native, ok := strings.CutPrefix(ref, "native:"); ok {
		if _, exists := Native(native); !exists {
			return Resolved{}, StageResolve, fmt.Errorf("native plugin %q is not registered", native)
		}
		return Resolved{Spec: spec, Source: SourceNative, Target: native}, "", nil
	}

	if !isPathLike(ref) {
		if _, exists := Native(ref); exists {
			return Resolved{Spec: spec, Source: SourceNative, Target: ref}, "", nil
		}
	}

	target, err := locate(ref, directory)
	if err != nil {
		return Resolved{}, StageResolve, err
	}
	command, env, err := entrypoint(target)
	if err != nil {
		return Resolved{}, StageEntry, err
	}
	return Resolved{Spec: spec, Source: SourceProcess, Target: target, Command: command, Env: env}, "", nil
}

// InstallRoot is where a plugin referred to by bare name is looked up:
// $XDG_CONFIG_HOME/gocode/plugin, or ~/.config/gocode/plugin.
//
// [global.Paths.Config] already ends in the app name, so nothing is appended
// to it here — a second "gocode" segment would send the loader looking in
// ~/.config/gocode/gocode/plugin, which is where nobody installs anything.
func InstallRoot() string {
	return filepath.Join(global.Resolve().Config, "plugin")
}

// InstallDir is where the plugin named ref is installed. `make install-plugin`
// writes here, and [Resolve] reads here.
func InstallDir(ref string) string {
	return filepath.Join(InstallRoot(), ref)
}

// locate turns a spec into an existing path, or explains where it looked.
func locate(ref, directory string) (string, error) {
	if isPathLike(ref) {
		path := expand(ref)
		if !filepath.IsAbs(path) {
			path = filepath.Join(directory, path)
		}
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%s: %w", ref, err)
		}
		return filepath.Clean(path), nil
	}

	installed := InstallDir(ref)
	if _, err := os.Stat(installed); err == nil {
		return installed, nil
	}
	return "", fmt.Errorf(
		"plugin %q is not installed: no native plugin by that name, and nothing at %s (this port does not install plugins at runtime)",
		ref, installed)
}

// entrypoint decides how to run a resolved target: a manifest names the
// command, a directory without one must hold an executable named `plugin`, and
// a file must itself be executable.
func entrypoint(target string) ([]string, []string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		if err := executable(target, info); err != nil {
			return nil, nil, err
		}
		return []string{target}, nil, nil
	}

	raw, err := os.ReadFile(filepath.Join(target, manifestFile))
	switch {
	case err == nil:
		var parsed descriptor
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", manifestFile, err)
		}
		if len(parsed.Command) == 0 {
			return nil, nil, fmt.Errorf("%s declares no command", manifestFile)
		}
		command := append([]string(nil), parsed.Command...)
		// A relative command names a file inside the plugin, not something on
		// PATH: resolving it against the plugin directory is what lets a
		// plugin ship its own binary.
		if strings.ContainsAny(command[0], `/\`) && !filepath.IsAbs(command[0]) {
			command[0] = filepath.Join(target, command[0])
		}
		var env []string
		for key, value := range parsed.Env {
			env = append(env, key+"="+value)
		}
		return command, env, nil
	case errors.Is(err, fs.ErrNotExist):
		fallback := filepath.Join(target, "plugin")
		if runtime.GOOS == "windows" {
			fallback += ".exe"
		}
		fallbackInfo, statErr := os.Stat(fallback)
		if statErr != nil {
			return nil, nil, fmt.Errorf("no %s and no executable at %s", manifestFile, fallback)
		}
		if err := executable(fallback, fallbackInfo); err != nil {
			return nil, nil, err
		}
		return []string{fallback}, nil, nil
	default:
		return nil, nil, err
	}
}

// executable rejects a target that cannot be run. On Windows the permission
// bits carry no such meaning, so the check is skipped there.
func executable(path string, info fs.FileInfo) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

// isPathLike reports whether a spec names a location rather than a plugin.
func isPathLike(ref string) bool {
	switch {
	case strings.HasPrefix(ref, "."),
		strings.HasPrefix(ref, "~"),
		strings.HasPrefix(ref, "/"),
		strings.ContainsAny(ref, `/\`):
		return true
	}
	// A Windows drive-qualified path: C:\plugins\lint.
	return len(ref) > 2 && ref[1] == ':'
}

// expand resolves a leading ~ against the user's home directory.
func expand(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return filepath.Join(home, path[2:])
	}
	return path
}
