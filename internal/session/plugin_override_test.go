package session

import (
	"context"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/plugin"
	"github.com/langazov/gocode-go/internal/tool"
)

// allowEverythingPlugin answers every permission request with "allow" — the
// shape of a plugin that exists to stop the user being interrupted.
func allowEverythingPlugin(t *testing.T) *plugin.Host {
	t.Helper()
	hooks := &plugin.Hooks{}
	plugin.On(hooks, plugin.PermissionAsk,
		func(ctx context.Context, in plugin.PermissionAskInput, out *plugin.PermissionAskOutput) error {
			out.Status = plugin.PermissionAllow
			return nil
		})
	host := plugin.NewHost(nil)
	host.Add(&plugin.Instance{ID: "yes-plugin", Hooks: hooks})
	return host
}

// A plugin may settle a question the user would otherwise be asked. It may not
// settle one the rules have already answered: a deny is not a question.
//
// Without this the permission.ask hook was a switch that turned off every deny
// in the ruleset, plan mode's read-only constraint included, with nothing in
// the transcript to say it had.
func TestPluginAllowCannotOverrideADeny(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_1", Name: "bash", Input: map[string]any{"command": "rm -rf /"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	registry := tool.NewRegistry()
	bash := &fakeTool{name: "bash", output: "should not run"}
	registry.Register(bash)
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.Permissions = &EnginePermissionGate{Engine: permission.NewEngine(
		permission.StaticRules{Rules: permission.Ruleset{{Action: "bash", Resource: "*", Effect: permission.Deny}}},
		nil, permission.Hooks{}, nil)}
	runner.Plugins = allowEverythingPlugin(t)
	admitPrompt(t, bus, runner, "clean up")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(bash.inputs) != 0 {
		t.Fatalf("a plugin must not run a denied tool, ran %d times", len(bash.inputs))
	}
	if result := toolResultFor(provider.requests[1], "call_1"); !strings.Contains(result, "blocked") {
		t.Fatalf("expected the rule's own denial, got %q", result)
	}
}

// The hook still does its job where it is supposed to: settling an ask without
// interrupting the user.
func TestPluginAllowStillSettlesAnAsk(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID: "call_1", Name: "bash", Input: map[string]any{"command": "ls"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	registry := tool.NewRegistry()
	bash := &fakeTool{name: "bash", output: "listed"}
	registry.Register(bash)
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.Permissions = &EnginePermissionGate{Engine: permission.NewEngine(
		permission.StaticRules{Rules: permission.Ruleset{{Action: "bash", Resource: "*", Effect: permission.Ask}}},
		nil, permission.Hooks{}, nil)}
	runner.Plugins = allowEverythingPlugin(t)
	admitPrompt(t, bus, runner, "list files")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(bash.inputs) != 1 {
		t.Fatalf("a plugin should be able to settle an ask, ran %d times", len(bash.inputs))
	}
}
