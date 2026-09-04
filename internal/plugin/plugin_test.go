package plugin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/tool"
)

func testHost(t *testing.T) (*Host, *[]string) {
	t.Helper()
	var reports []string
	host := NewHost(func(pluginID, hook string, err error) {
		reports = append(reports, pluginID+"/"+hook+": "+err.Error())
	})
	return host, &reports
}

func add(t *testing.T, host *Host, id string, build func(*Hooks)) {
	t.Helper()
	hooks := &Hooks{}
	build(hooks)
	host.Add(&Instance{ID: id, Spec: id, Source: SourceNative, Hooks: hooks})
}

// A hook chain threads one output through every plugin in load order, so a
// later plugin sees and can override an earlier one's edit.
func TestTriggerThreadsOutputInLoadOrder(t *testing.T) {
	host, _ := testHost(t)
	add(t, host, "first", func(h *Hooks) {
		On(h, ChatParams, func(_ context.Context, _ ChatInput, out *ChatParamsOutput) error {
			out.Temperature = Float(0.1)
			out.MaxOutputTokens = Int(100)
			return nil
		})
	})
	add(t, host, "second", func(h *Hooks) {
		On(h, ChatParams, func(_ context.Context, _ ChatInput, out *ChatParamsOutput) error {
			if out.Temperature == nil || *out.Temperature != 0.1 {
				t.Errorf("second plugin did not see the first's edit: %v", out.Temperature)
			}
			out.Temperature = Float(0.9)
			return nil
		})
	})

	out := ChatParamsOutput{}
	if err := Trigger(context.Background(), host, ChatParams, ChatInput{}, &out); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if out.Temperature == nil || *out.Temperature != 0.9 {
		t.Errorf("Temperature = %v, want 0.9", out.Temperature)
	}
	if out.MaxOutputTokens == nil || *out.MaxOutputTokens != 100 {
		t.Errorf("MaxOutputTokens = %v, want 100 (the first plugin's edit must survive)", out.MaxOutputTokens)
	}
}

// A failing hook is reported and skipped; the rest of the chain still runs and
// its edits survive. This is the deliberate divergence from TypeScript, where
// a rejected hook becomes a defect that aborts the turn.
func TestTriggerContinuesAfterFailure(t *testing.T) {
	host, reports := testHost(t)
	add(t, host, "broken", func(h *Hooks) {
		On(h, ChatHeaders, func(_ context.Context, _ ChatInput, _ *ChatHeadersOutput) error {
			return errors.New("boom")
		})
	})
	add(t, host, "working", func(h *Hooks) {
		On(h, ChatHeaders, func(_ context.Context, _ ChatInput, out *ChatHeadersOutput) error {
			out.Headers = map[string]string{"x-test": "1"}
			return nil
		})
	})

	out := ChatHeadersOutput{}
	err := Trigger(context.Background(), host, ChatHeaders, ChatInput{}, &out)
	if err == nil {
		t.Fatal("Trigger returned nil, want the failure surfaced to the caller")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v, want it to name the failure", err)
	}
	if out.Headers["x-test"] != "1" {
		t.Errorf("the working plugin's edit was lost: %v", out.Headers)
	}
	if len(*reports) != 1 || !strings.Contains((*reports)[0], "broken/chat.headers") {
		t.Errorf("reports = %v, want one attributed to the broken plugin", *reports)
	}
}

// Triggering a hook nobody registered is free and changes nothing.
func TestTriggerWithNoRegistrations(t *testing.T) {
	host, _ := testHost(t)
	if host.Registered(ShellEnv.Name()) {
		t.Fatal("Registered reported a hook nobody registered")
	}
	out := ShellEnvOutput{Env: map[string]string{"KEEP": "1"}}
	if err := Trigger(context.Background(), host, ShellEnv, ShellEnvInput{}, &out); err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if out.Env["KEEP"] != "1" {
		t.Error("output was modified with no hooks registered")
	}
}

// A hook name outside the catalog would never fire, so it is dropped at
// registration with a report rather than kept.
func TestUnknownHookIsDropped(t *testing.T) {
	host, reports := testHost(t)
	hooks := &Hooks{}
	hooks.entries = append(hooks.entries, entry{name: "chat.nonsense", fn: func() {}})
	host.Add(&Instance{ID: "typo", Hooks: hooks})

	if host.Registered("chat.nonsense") {
		t.Error("an unknown hook was installed")
	}
	if len(*reports) != 1 || !strings.Contains((*reports)[0], "unknown hook") {
		t.Errorf("reports = %v, want the unknown hook reported", *reports)
	}
}

// Close disposes in reverse load order, and one plugin's failure does not
// strand the others.
func TestCloseDisposesInReverseOrder(t *testing.T) {
	host, _ := testHost(t)
	var disposed []string
	add(t, host, "first", func(h *Hooks) {
		h.Dispose = func(context.Context) error { disposed = append(disposed, "first"); return nil }
	})
	add(t, host, "broken", func(h *Hooks) {
		h.Dispose = func(context.Context) error { return errors.New("no") }
	})
	add(t, host, "last", func(h *Hooks) {
		h.Dispose = func(context.Context) error { disposed = append(disposed, "last"); return nil }
	})

	err := host.Close(context.Background())
	if err == nil {
		t.Error("Close returned nil, want the dispose failure surfaced")
	}
	if len(disposed) != 2 || disposed[0] != "last" || disposed[1] != "first" {
		t.Errorf("disposed = %v, want [last first]", disposed)
	}
}

// A later plugin registering an existing tool name replaces it, matching the
// TypeScript record merge.
func TestToolsLastRegistrationWins(t *testing.T) {
	host, _ := testHost(t)
	add(t, host, "first", func(h *Hooks) {
		h.Tools = []Tool{{Name: "lint", Description: "original"}, {Name: "other"}}
	})
	add(t, host, "second", func(h *Hooks) {
		h.Tools = []Tool{{Name: "lint", Description: "replacement"}}
	})

	tools := host.Tools()
	if len(tools) != 2 {
		t.Fatalf("Tools() returned %d tools, want 2", len(tools))
	}
	if tools[0].Name != "lint" || tools[0].Description != "replacement" {
		t.Errorf("tools[0] = %+v, want lint/replacement in its original position", tools[0])
	}
}

// A plugin tool reaches the runtime through the same interface a built-in
// does, carrying the call's session context.
func TestRegisterToolsBridgesSessionContext(t *testing.T) {
	host, _ := testHost(t)
	var seen ToolContext
	add(t, host, "tooling", func(h *Hooks) {
		h.Tools = []Tool{{
			Name:        "echo",
			Description: "echoes",
			Parameters:  map[string]any{"type": "object"},
			Execute: func(_ context.Context, args map[string]any, tc ToolContext) (ToolResult, error) {
				seen = tc
				return ToolResult{Output: "said " + args["text"].(string), Title: "dropped"}, nil
			},
		}}
	})

	registry := tool.NewRegistry()
	RegisterTools(registry, host, "/work/dir", "/work")

	output, err := registry.Execute(context.Background(), "echo",
		map[string]any{"text": "hi"},
		tool.ExecContext{SessionID: "ses_1", Agent: "build", CallID: "call_1", AssistantMessageID: "msg_1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if output != "said hi" {
		t.Errorf("output = %q, want %q", output, "said hi")
	}
	if seen.SessionID != "ses_1" || seen.CallID != "call_1" || seen.Agent != "build" {
		t.Errorf("tool context = %+v, want the call's session context", seen)
	}
	if seen.Directory != "/work/dir" || seen.Worktree != "/work" {
		t.Errorf("tool context paths = %q/%q, want the host's", seen.Directory, seen.Worktree)
	}
}

// The config hook mutates the config the caller already holds, so a plugin's
// change is visible to everything built from it afterwards.
func TestApplyConfigMutatesThroughCallersPointer(t *testing.T) {
	host, _ := testHost(t)
	add(t, host, "opinionated", func(h *Hooks) {
		On(h, ConfigHook, func(_ context.Context, _ Empty, out *ConfigOutput) error {
			out.Config.Model = "anthropic/claude-opus-4-1"
			return nil
		})
	})

	cfg := &config.Config{Model: "openai/gpt-4o"}
	if err := ApplyConfig(context.Background(), host, cfg); err != nil {
		t.Fatalf("ApplyConfig: %v", err)
	}
	if cfg.Model != "anthropic/claude-opus-4-1" {
		t.Errorf("Model = %q, want the plugin's value", cfg.Model)
	}
}

// A prompt's `when` rule gates it on an earlier answer; a nil rule always
// matches, which is what makes the field optional.
func TestRuleMatch(t *testing.T) {
	answers := map[string]string{"mode": "enterprise"}
	cases := []struct {
		name string
		rule *Rule
		want bool
	}{
		{"nil matches", nil, true},
		{"eq hit", &Rule{Key: "mode", Op: "eq", Value: "enterprise"}, true},
		{"eq miss", &Rule{Key: "mode", Op: "eq", Value: "cloud"}, false},
		{"neq hit", &Rule{Key: "mode", Op: "neq", Value: "cloud"}, true},
		{"missing key", &Rule{Key: "absent", Op: "eq", Value: ""}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.Match(answers); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}
