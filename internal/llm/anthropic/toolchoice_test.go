package anthropic

import (
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// The session runner's last step keeps its tools declared and disables them
// with tool_choice "none": history holds tool calls, and a request that
// declares no tools cannot carry them. Both must reach the wire.
func TestToolChoiceNoneKeepsToolsDeclared(t *testing.T) {
	out, err := convertRequest(llm.Request{
		ModelID:    "claude-sonnet-4-5",
		MaxTokens:  1024,
		Tools:      []llm.ToolDefinition{{Name: "read", Description: "read a file", InputSchema: map[string]any{"type": "object"}}},
		ToolChoice: "none",
		Messages:   []llm.Message{llm.UserText("", "summarize")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 1 || out.Tools[0].Name != "read" {
		t.Fatalf("tools must stay declared, got %+v", out.Tools)
	}
	if out.ToolChoice == nil || out.ToolChoice.Type != "none" {
		t.Fatalf("tool_choice = %+v, want none", out.ToolChoice)
	}
}
