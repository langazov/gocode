package plugin

import (
	"context"
	"reflect"
	"testing"
)

// Status() reports every loaded plugin in load order with its tier and the
// hook/tool names it registered, and a native plugin is always "loaded" —
// there is no process to fall over.
func TestStatusReportsLoadedPlugins(t *testing.T) {
	host, _ := testHost(t)
	add(t, host, "first", func(h *Hooks) {
		On(h, ChatParams, func(context.Context, ChatInput, *ChatParamsOutput) error { return nil })
		h.Tools = []Tool{{Name: "lint"}, {Name: "audit"}}
	})
	add(t, host, "second", func(h *Hooks) {
		On(h, ChatHeaders, func(context.Context, ChatInput, *ChatHeadersOutput) error { return nil })
		On(h, ChatParams, func(context.Context, ChatInput, *ChatParamsOutput) error { return nil })
	})

	status := host.Status()
	if len(status) != 2 {
		t.Fatalf("Status() = %d entries, want 2", len(status))
	}
	first, second := status[0], status[1]

	if first.ID != "first" || first.Source != string(SourceNative) || first.State != "loaded" {
		t.Errorf("first = %+v, want id first, native, loaded", first)
	}
	if !reflect.DeepEqual(first.Tools, []string{"audit", "lint"}) {
		t.Errorf("first.Tools = %v, want sorted [audit lint]", first.Tools)
	}
	if !reflect.DeepEqual(second.Hooks, []string{"chat.headers", "chat.params"}) {
		t.Errorf("second.Hooks = %v, want sorted [chat.headers chat.params]", second.Hooks)
	}
}

// An empty host reports an empty (non-nil) list, which is what the status
// surfaces render as "none loaded" rather than hiding the section.
func TestStatusEmptyHost(t *testing.T) {
	host, _ := testHost(t)
	status := host.Status()
	if status == nil || len(status) != 0 {
		t.Fatalf("Status() = %v, want an empty non-nil slice", status)
	}
}

// A process plugin whose executable has exited reports "exited" — the state
// the sidebar's red dot keys off.
func TestStatusReportsExitedProcess(t *testing.T) {
	host, _ := testHost(t)
	exited := false
	state := func() string {
		if exited {
			return "exited"
		}
		return "running"
	}
	host.Add(&Instance{
		ID: "remote", Spec: "./remote", Source: SourceProcess,
		Hooks: &Hooks{}, state: state,
	})

	if got := host.Status()[0].State; got != "running" {
		t.Fatalf("state = %q, want running", got)
	}
	exited = true
	if got := host.Status()[0].State; got != "exited" {
		t.Fatalf("state = %q, want exited once the process is gone", got)
	}
}
