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
	"sync"
	"time"

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
	// CallTimeout is the per-call timeout from the plugin's manifest, or
	// zero if the manifest didn't declare one. A config "callTimeout"
	// option always takes precedence over this.
	CallTimeout time.Duration
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

	// A config entry may name a native plugin, and the only reason to write
	// one is to pass it options: `"plugin": [["memory", {"maxEntries": 50}]]`.
	// That entry must *configure* the built-in, not load a second copy of it —
	// two instances of one plugin means its hooks run twice, which for a hook
	// that appends to the system prompt is a visibly duplicated block.
	//
	// So the options are claimed here, before the native tier loads, and the
	// spec that supplied them is skipped in the loop below.
	nativeOptions := map[string]Options{}
	if !in.Pure {
		for _, spec := range in.Specs {
			name := strings.TrimPrefix(spec.Ref, "native:")
			if _, exists := Native(name); exists {
				nativeOptions[name] = spec.Options
			}
		}
	}

	if !in.DisableNative {
		for _, name := range Natives() {
			spec := Spec{Ref: name}
			in.Report.start(spec)
			factory, _ := Native(name)
			instance, err := loadNative(ctx, name, factory, in.Input, nativeOptions[name])
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

	// Configured plugins are resolved in order, spawned in parallel, and
	// installed in order again.
	//
	// The parallel middle step is the only departure from TypeScript's
	// sequential `for (const plan of plans) await load(plan)`, and it is here
	// because a process plugin's `initialize` handshake is a round trip to
	// another process (see process.go): two slow plugins used to cost the sum
	// of their handshakes on the boot path rather than the longest one.
	// Nothing observable changes, because a handshake has no side effects on
	// this heap — install order, which is what the hook contract is actually
	// about, is still config order.
	loads := make([]*attempt, 0, len(in.Specs))
	for _, spec := range in.Specs {
		if spec.Ref == "" {
			continue
		}
		// Already loaded above, with this entry's options.
		if _, claimed := nativeOptions[strings.TrimPrefix(spec.Ref, "native:")]; claimed && !in.DisableNative {
			continue
		}
		in.Report.start(spec)
		next := &attempt{spec: spec}
		next.resolved, next.stage, next.err = Resolve(spec, in.Input.Directory)
		loads = append(loads, next)
	}

	var wg sync.WaitGroup
	for _, next := range loads {
		if next.err != nil || next.resolved.Source != SourceProcess {
			continue
		}
		wg.Add(1)
		go func(next *attempt) {
			defer wg.Done()
			next.instance, next.err = Spawn(ctx, next.spec.Ref, SpawnConfig{
				Command:     next.resolved.Command,
				Dir:         in.Input.Directory,
				Env:         next.resolved.Env,
				CallTimeout: next.resolved.CallTimeout,
			}, in.Input, next.spec.Options, log)
			if next.err != nil {
				next.stage = StageLoad
			}
		}(next)
	}
	wg.Wait()

	// loaded tracks the ids already installed, native tier included, so a
	// second copy of one plugin is refused rather than quietly doubled. Two
	// config entries resolving to the same plugin — the bare name that finds
	// ~/.config/gocode/plugin/<name> and the absolute path to the same
	// program installed elsewhere — is an easy config to write and gives no
	// other symptom than every hook firing twice and every spawn's startup
	// cost being paid twice. Same reasoning as the native-options claim
	// above; this is the process tier's half of it.
	loaded := map[string]string{}
	for _, instance := range host.Instances() {
		loaded[instance.ID] = instance.Spec
	}

	for _, next := range loads {
		if next.err == nil && next.instance == nil {
			// A native ref: its factory runs here, in config order, so a
			// later plugin still sees an earlier one's mutations.
			next.instance, next.stage, next.err = loadResolved(ctx, next.spec, next.resolved, in.Input)
		}
		if next.err != nil {
			in.Report.fail(next.spec, next.stage, next.err)
			continue
		}
		if first, dup := loaded[next.instance.ID]; dup {
			err := fmt.Errorf("plugin %q was already loaded from %q; drop one of the two entries from \"plugin\" in gocode.json",
				next.instance.ID, first)
			if first == next.spec.Ref {
				err = fmt.Errorf("plugin %q is listed twice in \"plugin\" in gocode.json; loading it once", next.spec.Ref)
			}
			in.Report.fail(next.spec, StageLoad, err)
			if next.instance.closer != nil {
				_ = next.instance.closer(ctx)
			}
			continue
		}
		loaded[next.instance.ID] = next.spec.Ref
		host.Add(next.instance)
		in.Report.loaded(next.instance)
	}
	return host, nil
}

// attempt is one configured spec on its way through resolve, spawn and
// install. It exists so the spawn step can run concurrently while the two
// steps around it stay in config order.
type attempt struct {
	spec     Spec
	resolved Resolved
	stage    Stage
	err      error
	instance *Instance
}

// loadResolved runs an already-resolved spec's native factory, returning the
// stage that failed so the report can say which step refused. The process
// tier does not come through here: Load spawns those concurrently, before it
// walks the list to install them.
func loadResolved(ctx context.Context, spec Spec, resolved Resolved, in Input) (*Instance, Stage, error) {
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
	// CallTimeoutSeconds is the per-call timeout the host should apply when
	// the config options bag doesn't set "callTimeout" explicitly. A plugin
	// whose first call can be slow — rag-plugin's initial index of a large
	// repo — uses this to raise the 30s default without forcing every user
	// to add the option by hand.
	CallTimeoutSeconds int `json:"callTimeoutSeconds,omitempty"`
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
	command, env, callTimeout, err := entrypoint(target)
	if err != nil {
		return Resolved{}, StageEntry, err
	}
	return Resolved{Spec: spec, Source: SourceProcess, Target: target, Command: command, Env: env, CallTimeout: callTimeout}, "", nil
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

// PluginPathEnv names a colon-separated (semicolon on Windows) list of extra
// directories to search for bare-name plugins, ahead of the bundled ones. It
// exists for packagers whose layout this cannot infer — a distro package that
// splits the binary from its plugins, a Nix store path — and it is how the
// bundled lookup is tested, since a test binary's own location tells us
// nothing about where a release would have put things.
const PluginPathEnv = "GOCODE_PLUGIN_PATH"

// BundledRoots are the directories a packaged install puts plugins in, found
// relative to the running binary so that no absolute path has to be baked in
// or written into a user's config.
//
// Two layouts are recognized, which are the two this project actually ships:
//
//	<prefix>/bin/gocode + <prefix>/libexec/rag-plugin/   the Homebrew formula
//	<dir>/gocode        + <dir>/rag-plugin/              the release tarball
//
// Deriving them from os.Executable rather than hardcoding a Homebrew prefix
// is what makes this work across /opt/homebrew, /usr/local, Linuxbrew, and a
// tarball unpacked anywhere at all.
//
// Before this existed there was no way for a packaged plugin to be found by
// name, so the formula's post_install wrote the absolute libexec path into
// the user's config instead. That worked, but it made the config
// installation-specific: it broke when the Homebrew prefix changed, it named
// a path no one could reasonably type at `gocode plugin disable`, and — since
// an absolute path and a bare name are different refs that nothing
// deduplicates — a user who also ran `make install-rag-plugin` ended up
// loading the same plugin twice, spawning two copies of it at every boot.
func BundledRoots() []string {
	var roots []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" {
			return
		}
		clean := filepath.Clean(dir)
		if seen[clean] {
			return
		}
		seen[clean] = true
		roots = append(roots, clean)
	}

	for _, dir := range filepath.SplitList(os.Getenv(PluginPathEnv)) {
		add(dir)
	}

	exe, err := os.Executable()
	if err != nil {
		return roots
	}
	// Resolve symlinks: Homebrew puts a link in <prefix>/bin pointing into
	// the Cellar, and it is the Cellar copy that has libexec beside it.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	add(filepath.Join(filepath.Dir(dir), "libexec"))
	add(dir)
	return roots
}

// bundledDir returns the bundled directory holding the plugin named ref, and
// whether one was found.
//
// Only a directory carrying a manifest counts. That is stricter than
// [entrypoint]'s general rule, deliberately: one of the roots searched is the
// directory the gocode binary itself sits in, which on a tarball install is
// often something like /usr/local/bin, and the looser rule would treat every
// executable sitting next to gocode as a plugin.
func bundledDir(ref string) (string, bool) {
	if ref == "" || strings.ContainsAny(ref, `/\`) {
		return "", false
	}
	for _, root := range BundledRoots() {
		candidate := filepath.Join(root, ref)
		if _, err := os.Stat(filepath.Join(candidate, manifestFile)); err == nil {
			return candidate, true
		}
	}
	return "", false
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

	// The user's own install root wins over anything the package manager
	// shipped: someone who ran `make install-plugin` to try a local build is
	// asking for that build, not the packaged copy of the same name.
	installed := InstallDir(ref)
	if _, err := os.Stat(installed); err == nil {
		return installed, nil
	}
	if bundled, ok := bundledDir(ref); ok {
		return bundled, nil
	}
	searched := append([]string{installed}, BundledRoots()...)
	return "", fmt.Errorf(
		"plugin %q is not installed: no native plugin by that name, and nothing in %s (this port does not install plugins at runtime)",
		ref, strings.Join(searched, ", "))
}

// entrypoint decides how to run a resolved target: a manifest names the
// command, a directory without one must hold an executable named `plugin`, and
// a file must itself be executable.
func entrypoint(target string) ([]string, []string, time.Duration, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, nil, 0, err
	}
	if !info.IsDir() {
		if err := executable(target, info); err != nil {
			return nil, nil, 0, err
		}
		return []string{target}, nil, 0, nil
	}

	raw, err := os.ReadFile(filepath.Join(target, manifestFile))
	switch {
	case err == nil:
		var parsed descriptor
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return nil, nil, 0, fmt.Errorf("%s: %w", manifestFile, err)
		}
		if len(parsed.Command) == 0 {
			return nil, nil, 0, fmt.Errorf("%s declares no command", manifestFile)
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
		var callTimeout time.Duration
		if parsed.CallTimeoutSeconds > 0 {
			callTimeout = time.Duration(parsed.CallTimeoutSeconds) * time.Second
		}
		return command, env, callTimeout, nil
	case errors.Is(err, fs.ErrNotExist):
		fallback := filepath.Join(target, "plugin")
		if runtime.GOOS == "windows" {
			fallback += ".exe"
		}
		fallbackInfo, statErr := os.Stat(fallback)
		if statErr != nil {
			return nil, nil, 0, fmt.Errorf("no %s and no executable at %s", manifestFile, fallback)
		}
		if err := executable(fallback, fallbackInfo); err != nil {
			return nil, nil, 0, err
		}
		return []string{fallback}, nil, 0, nil
	default:
		return nil, nil, 0, err
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
