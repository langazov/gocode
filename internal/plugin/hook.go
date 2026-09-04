package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
)

// HookFunc is the Go shape of a TypeScript plugin hook.
//
// Every trigger hook in packages/plugin/src/index.ts is declared as
// `(input, output) => Promise<void>`: read the input, mutate the output in
// place, return nothing. Go has no in-place mutation of a value parameter, so
// the output arrives as a pointer; the contract is otherwise identical, down
// to hooks running in registration order and each one seeing the previous
// hook's edits.
//
// The error return is the one addition. TypeScript's `trigger` wraps each call
// in `Effect.promise`, which turns a rejected hook into a defect that takes
// down the run. [Trigger] instead reports the failure and moves on — see the
// note there.
type HookFunc[I any, O any] func(ctx context.Context, in I, out *O) error

// Definition names a hook and binds its input and output types. Hooks are
// addressed by name on the wire, so the binding is what keeps a process
// plugin's JSON and a native plugin's Go types describing the same thing.
type Definition[I any, O any] struct{ name string }

// Name returns the wire name of the hook, as it appears in a process plugin's
// handshake manifest.
func (d Definition[I, O]) Name() string { return d.name }

// Empty is the output of a notification hook — one that tells plugins
// something happened without collecting anything back. TypeScript's `event`
// hook, which takes an input and no output, is the only such hook.
type Empty struct{}

var (
	catalogMu sync.Mutex
	catalog   = map[string]reflect.Type{}
)

// Define declares a hook. It panics if the same name is defined twice with
// different types, which turns a copy-paste mistake in the catalog into a
// startup failure rather than a type assertion that silently never matches.
func Define[I any, O any](name string) Definition[I, O] {
	signature := reflect.TypeOf((*HookFunc[I, O])(nil)).Elem()
	catalogMu.Lock()
	defer catalogMu.Unlock()
	if existing, ok := catalog[name]; ok && existing != signature {
		panic(fmt.Sprintf("plugin: hook %q already defined as %s, redefined as %s", name, existing, signature))
	}
	catalog[name] = signature
	return Definition[I, O]{name: name}
}

// Names returns every defined hook name, sorted. The host uses it to reject
// unknown hook names in a process plugin's manifest.
func Names() []string {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// defined reports whether name is a known hook.
func defined(name string) bool {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	_, ok := catalog[name]
	return ok
}

// On registers a trigger hook on a plugin's hook set. It is the Go stand-in
// for assigning a named method on the returned TypeScript object:
//
//	plugin.On(hooks, plugin.ChatParams, func(ctx context.Context, in plugin.ChatInput, out *plugin.ChatParamsOutput) error {
//		out.Temperature = plugin.Float(0)
//		return nil
//	})
func On[I any, O any](h *Hooks, def Definition[I, O], fn HookFunc[I, O]) {
	if h == nil || fn == nil {
		return
	}
	h.entries = append(h.entries, entry{name: def.name, fn: fn})
}

// Trigger runs every hook registered for def, in load order, threading the
// same output through all of them. It ports `Plugin.trigger`.
//
// Deliberate divergence: a hook that fails is reported through the host's
// error sink and skipped, and the remaining hooks still run. TypeScript's
// `Effect.promise` makes a rejected hook a defect that aborts the turn, which
// lets one broken third-party plugin take down the agent. `out` keeps every
// successful hook's mutations either way, so a caller that wants the strict
// behavior checks the returned error before using it — as the tool and
// permission call sites do.
func Trigger[I any, O any](ctx context.Context, h *Host, def Definition[I, O], in I, out *O) error {
	if h == nil || out == nil {
		return nil
	}
	h.mu.RLock()
	list := append([]entry(nil), h.dispatch[def.name]...)
	h.mu.RUnlock()

	var failures []error
	for _, e := range list {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		switch {
		case e.remote != nil:
			err = e.remote.Invoke(ctx, def.name, in, out)
		default:
			fn, ok := e.fn.(HookFunc[I, O])
			if !ok {
				err = fmt.Errorf("registered with signature %T, want %T", e.fn, HookFunc[I, O](nil))
				break
			}
			err = fn(ctx, in, out)
		}
		if err == nil {
			continue
		}
		h.report(e.plugin, def.name, err)
		failures = append(failures, fmt.Errorf("plugin %s: hook %s: %w", e.plugin, def.name, err))
	}
	return errors.Join(failures...)
}

// Float and Int return pointers to a literal, for the optional numeric fields
// on hook outputs. Those are pointers because zero is a meaningful value —
// `temperature: 0` and "leave the temperature alone" are different requests,
// and TypeScript tells them apart with `undefined`.
func Float(v float64) *float64 { return &v }

// Int is Float's counterpart for token counts.
func Int(v int) *int { return &v }
