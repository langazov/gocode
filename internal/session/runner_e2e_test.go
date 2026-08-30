package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/llm"
	"github.com/anomalyco/opencode-go/internal/tool"
	"github.com/anomalyco/opencode-go/internal/tool/builtins"
)

// TestRunnerEndToEndWithBuiltins drives the full durable loop with a real
// built-in tool (read): the provider requests a read, the tool executes
// against the filesystem, and its output feeds the continuation turn.
func TestRunnerEndToEndWithBuiltins(t *testing.T) {
	workdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workdir, "greeting.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID:    "call_read",
				Name:  "read",
				Input: map[string]any{"path": "greeting.txt"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "The file says hello world"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}
	registry := tool.NewRegistry()
	builtins.Register(registry, workdir, nil)
	runner, bus := newRunnerFixture(t, provider, registry)
	admitPrompt(t, bus, runner, "read greeting.txt")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 2 {
		t.Fatalf("expected 2 provider turns, got %d", provider.callCount())
	}

	continuation := provider.requests[1]
	var toolResultText string
	for _, message := range continuation.Messages {
		if message.Role != llm.RoleTool {
			continue
		}
		for _, part := range message.Content {
			if part.Type == llm.PartToolResult && part.ToolCallID == "call_read" {
				toolResultText = part.Result
			}
		}
	}
	if toolResultText == "" || !strings.Contains(toolResultText, "hello world") {
		t.Fatalf("expected read tool output in continuation, got %q", toolResultText)
	}

	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	var assistantCount int
	for _, message := range messages {
		if message.Type == TypeAssistant {
			assistantCount++
		}
	}
	if assistantCount != 2 {
		t.Fatalf("expected 2 assistant messages, got %d", assistantCount)
	}
}
