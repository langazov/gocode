package openai

import (
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// The session runner's last step keeps its tools declared and disables them
// with tool_choice "none" — OpenAI rejects a tool_choice sent without tools.
// Both must reach the wire.
func TestToolChoiceNoneKeepsToolsDeclared(t *testing.T) {
	out, err := convertRequest(llm.Request{
		ModelID:    "gpt-5",
		MaxTokens:  1024,
		Tools:      []llm.ToolDefinition{{Name: "read", Description: "read a file", InputSchema: map[string]any{"type": "object"}}},
		ToolChoice: "none",
		Messages:   []llm.Message{llm.UserText("", "summarize")},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("tools must stay declared, got %+v", out.Tools)
	}
	if out.ToolChoice != "none" {
		t.Fatalf("tool_choice = %v, want none", out.ToolChoice)
	}
}
