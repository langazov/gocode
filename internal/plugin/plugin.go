// Package plugin ports the opencode plugin system
// (packages/plugin/src/index.ts, packages/opencode/src/plugin/index.ts and
// packages/opencode/src/plugin/loader.ts) to Go.
//
// # The TypeScript shape
//
// A TypeScript plugin is a factory — `(input, options) => Promise<Hooks>` —
// that returns an object of optional callbacks. Almost every callback has the
// signature `(input, output) => Promise<void>`: it reads an immutable input
// and mutates the output object in place, and the host uses the mutated
// object afterwards. The host (`Plugin.trigger`) walks its list of loaded
// hook objects in registration order, calls the ones that define the named
// callback, and returns the output.
//
// Plugins reach the host in two ways there: a fixed list of built-in modules
// imported directly (`internalPlugins()`), and user-configured ones resolved
// from config and pulled in with a dynamic `import()`.
//
// # What changes in Go
//
// The hook shape survives intact — an input value plus a pointer to an output
// struct the hook mutates. What cannot survive is `import()`: a Go binary is
// linked once, so there is no way to pull unknown code into the process. The
// two loading paths become two tiers instead:
//
//   - Native plugins are Go factories registered at init time and compiled
//     into the binary. This is the direct analogue of `internalPlugins()`,
//     and it is the tier that carries auth and provider registrations.
//   - Process plugins are separate executables the host spawns and speaks
//     newline-delimited JSON-RPC to over stdio (see process.go). This is the
//     analogue of a dynamically imported package: code the binary knows
//     nothing about at build time, in any language, loaded from config.
//
// Both tiers converge on the same [Hooks] value, so [Trigger] — and every
// call site in the runtime — cannot tell them apart.
package plugin

import (
	"context"
	"sort"

	"github.com/langazov/gocode-go/internal/db"
)

// Input is what a plugin factory receives, porting `PluginInput` in
// packages/plugin/src/index.ts.
//
// TypeScript passes a live `client` (a bound SDK instance) and Bun's `$`
// shell. Neither crosses a process boundary, so this carries the server's URL
// and auth headers instead and lets the plugin talk HTTP to the same API the
// TUI uses. A native plugin can of course dial it in-process.
type Input struct {
	// Directory is the session's working directory.
	Directory string `json:"directory"`
	// Worktree is the project root, for building stable relative paths.
	Worktree string `json:"worktree"`
	// ProjectID identifies the project the plugin is loaded for.
	ProjectID string `json:"projectID,omitempty"`
	// ServerURL is the base URL of this process's HTTP API. Empty when the
	// runtime is booted without a server (some CLI subcommands).
	ServerURL string `json:"serverURL,omitempty"`
	// Headers are the auth headers a plugin must send to ServerURL.
	Headers map[string]string `json:"headers,omitempty"`
	// Version is the gocode version, so a plugin can gate on it the way an
	// npm plugin gates on its `opencode` peer range.
	Version string `json:"version,omitempty"`

	// Services carries in-process handles, for the native tier only.
	//
	// Everything above this line is data that survives a pipe, because this
	// struct is JSON-marshaled into a process plugin's handshake (see
	// process.go). A native plugin is linked into the binary and runs on the
	// same heap as the runtime, so handing it the live database is both
	// possible and correct — the alternative, opening a second connection to
	// the same file, would put a second writer outside the write semaphore
	// internal/db uses to serialize them.
	//
	// The `json:"-"` tag is what keeps that distinction structural rather
	// than a rule someone has to remember: a process plugin cannot see this
	// field, so nothing can accidentally start depending on it there.
	Services Services `json:"-"`
}

// Services are the runtime handles a native plugin may be given. A zero value
// is valid and means the runtime was booted without them — some CLI
// subcommands build no database — so every consumer must check before use and
// opt out (return nil hooks) rather than fail the load.
type Services struct {
	// DB is the runtime's database handle.
	DB *db.DB
	// ProjectID identifies the project the runtime booted in, already resolved
	// through session.EnsureProject. Empty when no database backs this boot.
	ProjectID string
}

// Options is the free-form settings bag a plugin is configured with, porting
// `PluginOptions`. It comes from the second element of a config entry:
// `"plugin": [["./lint", {"strict": true}]]`.
type Options map[string]any

// Plugin is a native plugin factory, porting the `Plugin` type. It runs once
// per host and returns the hooks it wants installed. Returning nil hooks is
// legal and means the plugin opted out (a provider plugin that finds no
// credentials, say).
type Plugin func(ctx context.Context, in Input, opts Options) (*Hooks, error)

// Hooks is what a factory returns: the callbacks a plugin wants invoked. It
// ports the `Hooks` interface.
//
// The trigger-style hooks — everything with an `(input, output)` signature —
// are registered through [On] rather than being struct fields, because Go
// cannot express a field whose type varies per hook name. The remaining
// members of the TypeScript interface, which are registrations rather than
// callbacks, stay as plain fields here.
type Hooks struct {
	// Tools are extra tools the plugin contributes, porting the `tool` record.
	// They are bridged into the runtime's tool registry by [Host.Tools].
	Tools []Tool

	// Auth registers a provider login flow, porting the `auth` hook.
	// Native tier only — see the note on [AuthHook].
	Auth *AuthHook

	// Provider registers dynamic model discovery, porting the `provider`
	// hook. Native tier only.
	Provider *ProviderHook

	// Dispose is called when the host shuts down, porting `dispose`.
	Dispose func(ctx context.Context) error

	entries []entry
}

// entry is one registered trigger hook.
type entry struct {
	// plugin is the owning instance's ID, used to attribute failures.
	plugin string
	name   string
	// fn holds a HookFunc[I, O] for an in-process hook.
	fn any
	// remote is set instead of fn when the hook lives in another process.
	remote invoker
}

// names returns the trigger hook names this plugin registered, including any
// the host dropped as unknown — the status surfaces show what the plugin
// asked for, not what survived validation. Sorted for stable output.
func (h *Hooks) names() []string {
	if h == nil || len(h.entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(h.entries))
	for _, e := range h.entries {
		out = append(out, e.name)
	}
	sort.Strings(out)
	return out
}

// toolNames returns the names of the tools this plugin contributed, sorted
// for stable output.
func (h *Hooks) toolNames() []string {
	if h == nil || len(h.Tools) == 0 {
		return nil
	}
	out := make([]string, 0, len(h.Tools))
	for _, t := range h.Tools {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// invoker is the seam between an in-process hook and one reached over a
// transport. [Process] implements it.
type invoker interface {
	// Invoke sends in and out to the plugin and unmarshals the plugin's
	// mutated output back into out, which is always a non-nil pointer.
	Invoke(ctx context.Context, hook string, in, out any) error
}

// Instance is one loaded plugin.
type Instance struct {
	// ID is the plugin's own identifier: its declared id when it has one,
	// otherwise the config spec it was loaded from.
	ID string
	// Spec is the config entry that produced this instance.
	Spec string
	// Source records which tier loaded it.
	Source Source
	// Hooks is what the factory (or handshake) returned.
	Hooks *Hooks

	// closer tears down transport-level resources; nil for native plugins.
	closer func(context.Context) error

	// state reports liveness for the status surfaces; nil for native plugins,
	// which have no separate process to observe.
	state func() string
}

// Source is the tier a plugin was loaded from.
type Source string

const (
	// SourceNative is a plugin compiled into the binary and registered with
	// [Register]. Ports `internalPlugins()`.
	SourceNative Source = "native"
	// SourceProcess is an external executable spoken to over stdio. Ports the
	// dynamically imported npm/file plugin.
	SourceProcess Source = "process"
)
