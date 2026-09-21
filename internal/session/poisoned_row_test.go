package session

import (
	"os"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// TestPoisonedProductionRowReplaysWellFormed replays the exact assistant
// row recorded by ses_f3c9b48f6ffee5Vu3fhxloXzaP — the session whose four
// "try again" prompts all failed with openai 400 "tool_call_id must be
// provided for tool messages". Its tool part was recorded with an empty ID
// and name, and every later turn replayed that row verbatim, so the request
// carried a tool message with no tool_call_id and the endpoint refused it
// until the history was rewritten. This guards the read-time repair: the
// row as stored, replayed through ToLLMMessages and the openai adapter,
// must produce a well-formed pair.
func TestPoisonedProductionRowReplaysWellFormed(t *testing.T) {
	raw, err := os.ReadFile("testdata/poisoned_assistant_row.json")
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	out, err := ToLLMMessages([]StoredMessage{{
		ID: "msg_0c377755b001hfZSWVrjABF6du", Type: TypeAssistant, Data: raw,
	}}, testModel)
	if err != nil {
		t.Fatal(err)
	}
	var callID string
	for _, message := range out {
		for _, part := range message.Content {
			if part.Type == llm.PartToolCall && part.ToolCallID == "" {
				t.Fatal("replay must not emit a tool call with an empty ID")
			}
			if part.Type == llm.PartToolResult {
				if part.ToolCallID == "" {
					t.Fatal("replay must not emit a tool result with an empty ID")
				}
				callID = part.ToolCallID
			}
		}
	}
	if callID == "" {
		t.Fatal("the poisoned row's tool result went missing")
	}

	// And the wire form keeps the pair: the openai adapter drops id-less
	// results outright, so the synthesized ID must be present and shared.
	assistant := llm.Message{Role: llm.RoleAssistant, Content: nil}
	var tool llm.Message
	for _, message := range out {
		switch message.Role {
		case llm.RoleAssistant:
			assistant = message
		case llm.RoleTool:
			tool = message
		}
	}
	for _, part := range assistant.Content {
		if part.Type == llm.PartToolCall && part.ToolCallID != callID {
			t.Fatalf("call id %q does not match its result's %q", part.ToolCallID, callID)
		}
	}
	if len(tool.Content) == 0 {
		t.Fatal("expected the tool result message to survive replay")
	}
}
