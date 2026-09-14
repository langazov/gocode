package gemini

import (
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// The session runner's last step keeps its tools declared and disables them
// with tool_choice "none", which Gemini spells as function-calling mode NONE.
// Both must reach the wire.
func TestToolChoiceNoneKeepsToolsDeclared(t *testing.T) {
	out := convertRequest(llm.Request{
		ModelID:    "gemini-2.5-pro",
		MaxTokens:  1024,
		Tools:      []llm.ToolDefinition{{Name: "read", Description: "read a file", InputSchema: map[string]any{"type": "object"}}},
		ToolChoice: "none",
		Messages:   []llm.Message{llm.UserText("", "summarize")},
	})
	if len(out.Tools) != 1 {
		t.Fatalf("tools must stay declared, got %+v", out.Tools)
	}
	toolConfig, _ := out.GenerationConfig["toolConfig"].(map[string]any)
	calling, _ := toolConfig["functionCallingConfig"].(map[string]any)
	if calling["mode"] != "NONE" {
		t.Fatalf("functionCallingConfig = %+v, want mode NONE", toolConfig)
	}
}
