package session

import (
	"context"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/plugin"
	"github.com/langazov/gocode-go/internal/tool"
)

// hostWith builds a one-plugin host for the runner tests.
func hostWith(t *testing.T, build func(*plugin.Hooks)) *plugin.Host {
	t.Helper()
	host := plugin.NewHost(func(pluginID, hook string, err error) {
		t.Errorf("plugin %s hook %s failed: %v", pluginID, hook, err)
	})
	hooks := &plugin.Hooks{}
	build(hooks)
	host.Add(&plugin.Instance{ID: "test", Spec: "test", Source: plugin.SourceNative, Hooks: hooks})
	return host
}

// The chat.params hook reaches the assembled request, and the system
// transform hook owns the whole prompt list.
func TestRunnerAppliesRequestHooks(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "ok"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.Plugins = hostWith(t, func(h *plugin.Hooks) {
		plugin.On(h, plugin.ChatParams, func(_ context.Context, in plugin.ChatInput, out *plugin.ChatParamsOutput) error {
			if in.Agent != "build" || in.Model.ProviderID != "anthropic" {
				t.Errorf("chat.params input = %+v, want the turn's agent and model", in)
			}
			out.Temperature = plugin.Float(0.3)
			out.MaxOutputTokens = plugin.Int(512)
			return nil
		})
		plugin.On(h, plugin.SystemTransform, func(_ context.Context, _ plugin.SystemTransformInput, out *plugin.SystemTransformOutput) error {
			out.System = append(out.System, "appended by plugin")
			return nil
		})
	})
	admitPrompt(t, bus, runner, "hi")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider saw %d requests, want 1", len(provider.requests))
	}
	request := provider.requests[0]
	if request.Temperature == nil || *request.Temperature != 0.3 {
		t.Errorf("Temperature = %v, want the hook's 0.3", request.Temperature)
	}
	if request.MaxTokens != 512 {
		t.Errorf("MaxTokens = %d, want the hook's 512", request.MaxTokens)
	}
	if len(request.System) != 2 || request.System[1] != "appended by plugin" {
		t.Errorf("System = %v, want the plugin's block appended", request.System)
	}
}

// The tool.definition hook rewrites what the model is shown, without touching
// what actually runs.
func TestRunnerAppliesToolDefinitionHook(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "ok"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	registry := tool.NewRegistry()
	registry.Register(&fakeTool{name: "search", output: "results"})
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.Plugins = hostWith(t, func(h *plugin.Hooks) {
		plugin.On(h, plugin.ToolDefinition, func(_ context.Context, in plugin.ToolDefinitionInput, out *plugin.ToolDefinitionOutput) error {
			if in.ToolID == "search" {
				out.Description = "rewritten description"
			}
			return nil
		})
	})
	admitPrompt(t, bus, runner, "hi")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	tools := provider.requests[0].Tools
	if len(tools) != 1 || tools[0].Description != "rewritten description" {
		t.Errorf("advertised tools = %+v, want the rewritten description", tools)
	}
}

// tool.execute.before rewrites the arguments the tool actually runs with, and
// tool.execute.after rewrites the output the model sees.
func TestRunnerAppliesToolExecutionHooks(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "search", Input: map[string]any{"query": "original"}}},
			{Type: llm.EventFinish, Finish: "tool_calls"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	target := &fakeTool{name: "search", output: "raw output"}
	registry := tool.NewRegistry()
	registry.Register(target)
	runner, bus := newRunnerFixture(t, provider, registry)

	var seenArgs map[string]any
	runner.Plugins = hostWith(t, func(h *plugin.Hooks) {
		plugin.On(h, plugin.ToolExecuteBefore, func(_ context.Context, in plugin.ToolExecuteBeforeInput, out *plugin.ToolExecuteBeforeOutput) error {
			if in.Tool != "search" || in.CallID != "call_1" {
				t.Errorf("before input = %+v, want the call being made", in)
			}
			out.Args["query"] = "rewritten"
			return nil
		})
		plugin.On(h, plugin.ToolExecuteAfter, func(_ context.Context, in plugin.ToolExecuteAfterInput, out *plugin.ToolExecuteAfterOutput) error {
			seenArgs = in.Args
			out.Output = "wrapped(" + out.Output + ")"
			return nil
		})
	})
	admitPrompt(t, bus, runner, "search please")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(target.inputs) != 1 || target.inputs[0]["query"] != "rewritten" {
		t.Errorf("tool ran with %+v, want the rewritten arguments", target.inputs)
	}
	// The after hook sees the arguments the tool actually ran with, not the
	// ones the model proposed.
	if seenArgs["query"] != "rewritten" {
		t.Errorf("after hook saw %+v, want the rewritten arguments", seenArgs)
	}

	// The rewritten output is what reaches the next turn's transcript.
	if len(provider.requests) != 2 {
		t.Fatalf("provider saw %d requests, want 2", len(provider.requests))
	}
	var toolResult string
	for _, message := range provider.requests[1].Messages {
		for _, part := range message.Content {
			if part.Type == llm.PartToolResult {
				toolResult = part.Result
			}
		}
	}
	if toolResult != "wrapped(raw output)" {
		t.Errorf("tool result in transcript = %q, want the hook's rewrite", toolResult)
	}
}

// A runner with no plugin host runs the path it ran before hooks existed.
func TestRunnerWithoutPluginsIsUnaffected(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "ok"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	admitPrompt(t, bus, runner, "hi")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.requests[0].Temperature != nil || provider.requests[0].TopP != nil {
		t.Error("sampling parameters were set with no plugins loaded")
	}
}
