package session

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

type staticSource []string

func (s staticSource) System() []string { return s }

// denyGuard refuses one tool and annotates every other's output.
type denyGuard struct{ deny string }

func (g denyGuard) Before(name string, _ map[string]any) error {
	if name == g.deny {
		return errors.New("blocked by harness rule: not here")
	}
	return nil
}

func (g denyGuard) After(_ string, _ map[string]any, output string) string {
	return output + " [checked]"
}

// The system prompt is the agent charter, then instruction files, then the
// harness overlay — the project's own AGENTS.md is read before any adapted
// guidance layered over it.
func TestRunnerSystemOrderWithInstructionsAndHarness(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "ok"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.Instructions = staticSource{"Instructions from: /p/AGENTS.md\nproject rules"}
	runner.Harness = staticSource{"# HARNESS\n\nadapted rules"}
	admitPrompt(t, bus, runner, "hi")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	system := provider.requests[0].System
	if len(system) != 3 || system[0] != "You are gocode." ||
		!strings.Contains(system[1], "project rules") || !strings.Contains(system[2], "adapted rules") {
		t.Fatalf("system = %q", system)
	}
}

func TestRunnerGuardBlocksAndAnnotates(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_w", Name: "write", Input: map[string]any{"filePath": ".env"}}},
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_r", Name: "read", Input: map[string]any{"filePath": "a"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	registry := tool.NewRegistry()
	write := &fakeTool{name: "write", output: "written"}
	read := &fakeTool{name: "read", output: "contents"}
	registry.Register(write)
	registry.Register(read)
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.Guard = denyGuard{deny: "write"}
	admitPrompt(t, bus, runner, "go")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(write.inputs) != 0 {
		t.Fatal("a guarded tool ran")
	}
	if result := toolResultFor(provider.requests[1], "call_w"); !strings.Contains(result, "not here") {
		t.Fatalf("write result = %q", result)
	}
	if result := toolResultFor(provider.requests[1], "call_r"); !strings.Contains(result, "contents [checked]") {
		t.Fatalf("read result = %q", result)
	}
}
