package session

import (
	_ "embed"
	"encoding/json"

	"github.com/langazov/gocode-go/internal/llm"
)

// PlanAgentID is the agent plan mode runs under. Named here rather than
// inlined because three layers agree on it: the registry that defines the
// agent, the plan_exit tool that switches away from it, and the reminder
// below that decides which system reminder a turn gets.
const PlanAgentID = "plan"

// BuildAgentID is the agent plan mode hands back to.
const BuildAgentID = "build"

// planPrompt and buildSwitchPrompt are verbatim copies of
// packages/opencode/src/session/prompt/{plan,build-switch}.txt. Kept as files
// rather than Go string constants so a `diff` against upstream stays useful.
//
//go:embed prompt/plan.txt
var planPrompt string

//go:embed prompt/build-switch.txt
var buildSwitchPrompt string

// applyReminders ports packages/opencode/src/session/reminders.ts (the
// non-experimental branch — the plan-file workflow behind
// OPENCODE_EXPERIMENTAL_PLAN_MODE is not implemented here).
//
// Plan mode's read-only constraint lives in a system reminder, not in the
// agent's system prompt: the plan agent deliberately inherits the base prompt,
// and the reminder rides on the newest user message so it stays adjacent to
// the request it constrains instead of decaying into the far past of a long
// conversation. Without this the plan agent is just build with edit denied,
// and the model spends its turn hitting permission errors it was never told
// about.
func applyReminders(messages []llm.Message, stored []StoredMessage, agentID string) []llm.Message {
	reminder := reminderFor(stored, agentID)
	if reminder == "" {
		return messages
	}
	// findLast(role === "user") upstream. Attaching to the last user message
	// rather than appending a new one keeps the reminder inside the user turn
	// even mid-step, where the tail of the conversation is tool results.
	target := -1
	for i := range messages {
		if messages[i].Role == llm.RoleUser {
			target = i
		}
	}
	if target < 0 {
		return messages
	}
	out := make([]llm.Message, len(messages))
	copy(out, messages)
	content := make([]llm.ContentPart, len(out[target].Content), len(out[target].Content)+1)
	copy(content, out[target].Content)
	out[target].Content = append(content, llm.ContentPart{Type: llm.PartText, Text: reminder})
	return out
}

// reminderFor picks the reminder for a turn: the read-only charter while the
// plan agent is driving, and the release from it on the first build turn
// after. Returns "" when neither applies.
func reminderFor(stored []StoredMessage, agentID string) string {
	if agentID == PlanAgentID {
		return planPrompt
	}
	if agentID != BuildAgentID || !ranAsPlan(stored) {
		return ""
	}
	return buildSwitchPrompt
}

// ranAsPlan reports whether any assistant message in the visible history was
// produced by the plan agent. Upstream scans the whole history the same way,
// so every build turn after a planning phase carries the release reminder —
// not just the first one.
func ranAsPlan(stored []StoredMessage) bool {
	for _, message := range stored {
		if message.Type != TypeAssistant {
			continue
		}
		var assistant struct {
			Agent string `json:"agent"`
		}
		if err := json.Unmarshal(message.Data, &assistant); err != nil {
			continue
		}
		if assistant.Agent == PlanAgentID {
			return true
		}
	}
	return false
}
