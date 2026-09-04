package plugin

import (
	"fmt"
	"sort"
	"sync"
)

// The native tier: plugins compiled into the binary.
//
// This ports `internalPlugins()` in packages/opencode/src/plugin/index.ts,
// which is a literal array of imported plugin factories. A Go file cannot hold
// that array without every plugin package being imported by this one, which
// would invert the dependency (a provider auth plugin needs the provider
// layer, which sits above this package). So registration is inverted too: a
// plugin package calls [Register] from its own init, and the boot wiring
// imports it for effect. The registry is what the array was — an ordered list
// of factories the host runs at startup.

var (
	nativeMu sync.Mutex
	natives  = map[string]Plugin{}
	// order preserves registration order, so a native plugin's hooks run in
	// the order its package was linked rather than in map order.
	order []string
)

// Register adds a native plugin factory under a stable name. It panics on a
// duplicate name, because two plugins answering to one name means one of them
// silently never loads.
//
// Call it from an init function:
//
//	func init() { plugin.Register("copilot-auth", New) }
func Register(name string, factory Plugin) {
	if name == "" || factory == nil {
		panic("plugin: Register needs a name and a factory")
	}
	nativeMu.Lock()
	defer nativeMu.Unlock()
	if _, exists := natives[name]; exists {
		panic(fmt.Sprintf("plugin: native plugin %q already registered", name))
	}
	natives[name] = factory
	order = append(order, name)
}

// Native returns a registered factory by name.
func Native(name string) (Plugin, bool) {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	factory, ok := natives[name]
	return factory, ok
}

// Natives returns every registered native plugin name in registration order.
// The loader uses it to load the built-in tier before configured plugins.
func Natives() []string {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	return append([]string(nil), order...)
}

// NativeNamesSorted returns the registered names sorted, for `gocode debug`
// output where a stable display order matters more than load order.
func NativeNamesSorted() []string {
	names := Natives()
	sort.Strings(names)
	return names
}

// resetNatives clears the registry. Tests only.
func resetNatives() {
	nativeMu.Lock()
	defer nativeMu.Unlock()
	natives = map[string]Plugin{}
	order = nil
}
