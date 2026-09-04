package plugin

import (
	"context"

	"github.com/langazov/gocode-go/internal/config"
)

// Helpers the boot wiring and the runtime seams use, so the same three lines
// are not repeated at every call site.

// Specs converts the config's `plugin` array into loader specs.
func Specs(entries []config.PluginSpec) []Spec {
	specs := make([]Spec, 0, len(entries))
	for _, entry := range entries {
		if entry.Ref == "" {
			continue
		}
		specs = append(specs, Spec{Ref: entry.Ref, Options: Options(entry.Options)})
	}
	return specs
}

// ApplyConfig runs the config hook, letting plugins mutate the merged config
// before the runtime is built from it. Ports the "notify plugins of current
// config" pass in packages/opencode/src/plugin/index.ts.
func ApplyConfig(ctx context.Context, host *Host, cfg *config.Config) error {
	if host == nil || cfg == nil {
		return nil
	}
	out := ConfigOutput{Config: cfg}
	if err := Trigger(ctx, host, ConfigHook, Empty{}, &out); err != nil {
		return err
	}
	// A hook that replaced the pointer wholesale — which is what a process
	// plugin's JSON round trip always does — has its result copied back into
	// the caller's config, so the change is visible through the pointer the
	// caller already holds.
	if out.Config != nil && out.Config != cfg {
		*cfg = *out.Config
	}
	return nil
}

// Notify delivers a committed event to every plugin. It ports the event
// listener the TypeScript host installs, including its fire-and-forget
// posture: the caller must not wait on plugins, and an event a plugin
// mishandles must not fail the commit that produced it.
//
// The caller converts its own event type into [Event]; this package stays
// clear of the event store so nothing below it has to depend on the database.
func Notify(ctx context.Context, host *Host, event Event) {
	if host == nil || !host.Registered(EventHook.Name()) {
		return
	}
	var out Empty
	// The error is deliberately dropped: it has already been reported through
	// the host's error sink, and there is no caller here to hand it to.
	_ = Trigger(ctx, host, EventHook, EventInput{Event: event}, &out)
}
