package plugin

import (
	"context"
	"errors"
	"sync"
)

// Host owns the loaded plugins and dispatches hooks to them. It ports the
// `Plugin` service in packages/opencode/src/plugin/index.ts: the service holds
// a flat, ordered list of hook objects and every `trigger` walks it.
//
// Ordering is load order, and load order is deliberate — native plugins first,
// then configured ones in config order — because a later hook sees the earlier
// hook's mutations and can override them.
//
// A Host is safe for concurrent use: tool calls settle in parallel, so
// tool.execute.before can be triggered from several goroutines at once.
type Host struct {
	mu        sync.RWMutex
	instances []*Instance
	dispatch  map[string][]entry

	// onError receives every hook failure. It exists because a plugin's
	// failure has nowhere good to go: stderr corrupts the TUI's alternate
	// screen, so the boot wiring points this at global.LogBackground.
	onError func(pluginID, hook string, err error)
}

// NewHost returns an empty host. onError may be nil, which discards failures.
func NewHost(onError func(pluginID, hook string, err error)) *Host {
	return &Host{dispatch: map[string][]entry{}, onError: onError}
}

func (h *Host) report(pluginID, hook string, err error) {
	if h.onError == nil {
		return
	}
	h.onError(pluginID, hook, err)
}

// Add installs a loaded plugin. Hooks whose names are not in the catalog are
// dropped with a report rather than silently kept: an unknown name is either a
// typo or a plugin built against a newer host, and in both cases the hook
// would never fire.
func (h *Host) Add(instance *Instance) {
	if instance == nil || instance.Hooks == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.instances = append(h.instances, instance)
	for _, e := range instance.Hooks.entries {
		if !defined(e.name) {
			h.report(instance.ID, e.name, errors.New("unknown hook"))
			continue
		}
		e.plugin = instance.ID
		h.dispatch[e.name] = append(h.dispatch[e.name], e)
	}
}

// Instances returns the loaded plugins in load order.
func (h *Host) Instances() []*Instance {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]*Instance(nil), h.instances...)
}

// Registered reports whether any plugin implements the named hook. Call sites
// use it to skip building a hook input that nothing will read — the tool
// definition hook runs once per tool per turn, so the check is worth it.
func (h *Host) Registered(name string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.dispatch[name]) > 0
}

// Tools returns every tool contributed by a plugin, in load order. A later
// plugin registering an existing name wins, matching the TypeScript record
// merge where the last assignment to a key survives.
func (h *Host) Tools() []Tool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	index := map[string]int{}
	var tools []Tool
	for _, instance := range h.instances {
		for _, t := range instance.Hooks.Tools {
			if at, ok := index[t.Name]; ok {
				tools[at] = t
				continue
			}
			index[t.Name] = len(tools)
			tools = append(tools, t)
		}
	}
	return tools
}

// Auth returns the auth registrations from every loaded plugin, in load order.
// Ports the collection the provider layer does over `hooks.auth`.
func (h *Host) Auth() []*AuthHook {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []*AuthHook
	for _, instance := range h.instances {
		if instance.Hooks.Auth != nil {
			out = append(out, instance.Hooks.Auth)
		}
	}
	return out
}

// Providers returns the provider registrations from every loaded plugin.
func (h *Host) Providers() []*ProviderHook {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []*ProviderHook
	for _, instance := range h.instances {
		if instance.Hooks.Provider != nil {
			out = append(out, instance.Hooks.Provider)
		}
	}
	return out
}

// Close disposes every plugin in reverse load order and tears down transports.
// It ports the `dispose` finalizer, and mirrors its tolerance: one plugin
// failing to shut down must not strand the ones after it.
func (h *Host) Close(ctx context.Context) error {
	h.mu.Lock()
	instances := h.instances
	h.instances = nil
	h.dispatch = map[string][]entry{}
	h.mu.Unlock()

	var failures []error
	for i := len(instances) - 1; i >= 0; i-- {
		instance := instances[i]
		if dispose := instance.Hooks.Dispose; dispose != nil {
			if err := dispose(ctx); err != nil {
				h.report(instance.ID, "dispose", err)
				failures = append(failures, err)
			}
		}
		if instance.closer == nil {
			continue
		}
		if err := instance.closer(ctx); err != nil {
			h.report(instance.ID, "close", err)
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Status is one entry of the plugin list the status surfaces show. State is
// "loaded" unless a process-level check says otherwise, so a native plugin is
// always "loaded" — there is nothing to fall over.
type Status struct {
	ID     string   `json:"id"`
	Spec   string   `json:"spec"`
	Source string   `json:"source"`
	State  string   `json:"state"`
	Hooks  []string `json:"hooks,omitempty"`
	Tools  []string `json:"tools,omitempty"`
}

// Status returns every loaded plugin with its tier, hook and tool names, and
// process state, in load order. It backs GET /api/plugin and the interface's
// plugin section; nil means no plugins are loaded, which the surfaces render
// as an explicit empty state rather than hiding the section.
func (h *Host) Status() []Status {
	h.mu.RLock()
	instances := append([]*Instance(nil), h.instances...)
	h.mu.RUnlock()

	out := make([]Status, 0, len(instances))
	for _, instance := range instances {
		state := "loaded"
		if instance.state != nil {
			state = instance.state()
		}
		out = append(out, Status{
			ID:     instance.ID,
			Spec:   instance.Spec,
			Source: string(instance.Source),
			State:  state,
			Hooks:  instance.Hooks.names(),
			Tools:  instance.Hooks.toolNames(),
		})
	}
	return out
}
