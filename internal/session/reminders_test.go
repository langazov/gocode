package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

func assistantMessage(t *testing.T, id, agent string) StoredMessage {
	t.Helper()
	data, err := json.Marshal(map[string]any{"agent": agent, "content": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	return StoredMessage{ID: id, Type: TypeAssistant, Data: data}
}

func userMessage(t *testing.T, id, text string) StoredMessage {
	t.Helper()
	data, err := json.Marshal(map[string]any{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	return StoredMessage{ID: id, Type: TypeUser, Data: data}
}

// lastUserText returns the concatenated text of the final user-role message,
// which is where applyReminders is expected to land.
func lastUserText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != llm.RoleUser {
			continue
		}
		var out strings.Builder
		for _, part := range messages[i].Content {
			out.WriteString(part.Text)
		}
		return out.String()
	}
	return ""
}

func TestRemindersAttachPlanPromptForPlanAgent(t *testing.T) {
	stored := []StoredMessage{userMessage(t, "msg_1", "add markdown lsp")}
	messages := []llm.Message{llm.UserText("msg_1", "add markdown lsp")}

	out := applyReminders(messages, stored, PlanAgentID)

	text := lastUserText(out)
	if !strings.Contains(text, "add markdown lsp") {
		t.Fatalf("reminder replaced the user's text: %q", text)
	}
	if !strings.Contains(text, "Plan Mode - System Reminder") {
		t.Fatalf("plan reminder missing: %q", text)
	}
	// The reminder must not mutate the caller's slice: the runner reuses the
	// converted history within a turn.
	if len(messages[0].Content) != 1 {
		t.Fatalf("input message was mutated: %d parts", len(messages[0].Content))
	}
}

func TestRemindersSkipBuildAgentWithNoPlanHistory(t *testing.T) {
	stored := []StoredMessage{
		userMessage(t, "msg_1", "hi"),
		assistantMessage(t, "msg_2", BuildAgentID),
	}
	messages := []llm.Message{llm.UserText("msg_1", "hi")}

	out := applyReminders(messages, stored, BuildAgentID)

	if text := lastUserText(out); text != "hi" {
		t.Fatalf("build with no plan history should get no reminder, got %q", text)
	}
}

func TestRemindersAnnounceSwitchBackToBuild(t *testing.T) {
	stored := []StoredMessage{
		userMessage(t, "msg_1", "add markdown lsp"),
		assistantMessage(t, "msg_2", PlanAgentID),
		userMessage(t, "msg_3", "The plan has been approved"),
	}
	messages := []llm.Message{
		llm.UserText("msg_1", "add markdown lsp"),
		llm.UserText("msg_3", "The plan has been approved"),
	}

	out := applyReminders(messages, stored, BuildAgentID)

	// Attached to the newest user message, not the first one.
	if text := lastUserText(out); !strings.Contains(text, "no longer in read-only mode") {
		t.Fatalf("build-switch reminder missing: %q", text)
	}
	if first := out[0].Content; len(first) != 1 {
		t.Fatalf("reminder landed on the wrong message: %d parts", len(first))
	}
}

func TestRemindersAttachToLastUserMessageAcrossToolResults(t *testing.T) {
	stored := []StoredMessage{userMessage(t, "msg_1", "add markdown lsp")}
	messages := []llm.Message{
		llm.UserText("msg_1", "add markdown lsp"),
		llm.AssistantText("msg_2", "looking"),
		llm.ToolResultMessage("", "call_1", "grep", "no matches", false),
	}

	out := applyReminders(messages, stored, PlanAgentID)

	if got := out[0].Content; len(got) != 2 || !strings.Contains(got[1].Text, "Plan Mode") {
		t.Fatalf("reminder should ride the user message, got %+v", got)
	}
	if out[2].Role != llm.RoleTool {
		t.Fatalf("tool result was displaced: %+v", out[2])
	}
}

func TestRemindersNoOpWithoutUserMessage(t *testing.T) {
	messages := []llm.Message{llm.AssistantText("msg_1", "hello")}

	out := applyReminders(messages, nil, PlanAgentID)

	if len(out) != 1 || len(out[0].Content) != 1 {
		t.Fatalf("expected the history unchanged, got %+v", out)
	}
}
