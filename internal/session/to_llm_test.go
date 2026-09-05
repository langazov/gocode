package session

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// Ported from packages/opencode/test/session/message-v2.test.ts's
// `toModelMessage` suite, for the parts this port implements. Upstream
// additionally covers provider-specific media handling (bedrock PDF splitting,
// jpeg passthrough), OpenRouter reasoning details, step-start boundaries, and
// the empty-text-between-signed-reasoning-blocks workaround; none of those
// exist here.

var testModel = ModelRef{ProviderID: "anthropic", ID: "claude"}

func stored(t *testing.T, id, kind string, payload any) StoredMessage {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return StoredMessage{ID: id, Type: kind, Data: data}
}

func convert(t *testing.T, messages ...StoredMessage) []llm.Message {
	t.Helper()
	out, err := ToLLMMessages(messages, testModel)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// countParts tallies part types across every message, which is how the
// well-formedness invariants below are stated.
func countParts(messages []llm.Message, kind string) int {
	n := 0
	for _, message := range messages {
		for _, part := range message.Content {
			if part.Type == kind {
				n++
			}
		}
	}
	return n
}

// The invariant every provider enforces: each tool call carries a matching
// result. Anthropic refuses a request that breaks it, and because history is
// replayed on every step, one unsettled tool poisons the conversation from
// then on rather than just failing the turn it appeared in.
//
// A tool left pending or running is one whose turn ended before it did.
func TestToLLMEveryToolCallHasAResult(t *testing.T) {
	for _, status := range []string{ToolPending, ToolRunning, ToolCompleted, ToolError} {
		t.Run(status, func(t *testing.T) {
			out := convert(t, stored(t, "msg_1", TypeAssistant, AssistantMessage{
				Model: testModel,
				Content: []AssistantContent{{
					Type: "tool", ID: "call_1", Name: "bash",
					State: &ToolState{Status: status, Input: map[string]any{"command": "ls"}, Output: "listed"},
				}},
			}))
			calls := countParts(out, llm.PartToolCall)
			results := countParts(out, llm.PartToolResult)
			if calls != 1 {
				t.Fatalf("expected one tool call, got %d", calls)
			}
			if results != calls {
				t.Fatalf("status %s left a dangling tool call: %d calls, %d results", status, calls, results)
			}
		})
	}
}

// An unsettled tool reports as an error, so the model is told the call did not
// finish rather than being handed an empty success.
func TestToLLMUnsettledToolReportsAsError(t *testing.T) {
	out := convert(t, stored(t, "msg_1", TypeAssistant, AssistantMessage{
		Model: testModel,
		Content: []AssistantContent{{
			Type: "tool", ID: "call_1", Name: "bash",
			State: &ToolState{Status: ToolRunning, Input: map[string]any{"command": "sleep 100"}},
		}},
	}))
	for _, message := range out {
		for _, part := range message.Content {
			if part.Type != llm.PartToolResult {
				continue
			}
			if !part.IsError {
				t.Error("an unfinished tool must be reported as an error result")
			}
			if strings.TrimSpace(part.Result) == "" {
				t.Error("an error result must say something")
			}
		}
	}
}

// A completed tool's output is forwarded verbatim; an errored tool forwards
// its message and is flagged.
func TestToLLMToolResultContent(t *testing.T) {
	out := convert(t, stored(t, "msg_1", TypeAssistant, AssistantMessage{
		Model: testModel,
		Content: []AssistantContent{
			{Type: "tool", ID: "ok", Name: "read", State: &ToolState{Status: ToolCompleted, Output: "file body"}},
			{Type: "tool", ID: "bad", Name: "write", State: &ToolState{Status: ToolError, Error: "permission denied"}},
		},
	}))

	found := map[string]llm.ContentPart{}
	for _, message := range out {
		for _, part := range message.Content {
			if part.Type == llm.PartToolResult {
				found[part.ToolCallID] = part
			}
		}
	}
	if got := found["ok"]; got.Result != "file body" || got.IsError {
		t.Errorf("completed result = %+v", got)
	}
	if got := found["bad"]; got.Result != "permission denied" || !got.IsError {
		t.Errorf("errored result = %+v", got)
	}
}

// A provider-executed tool ran inside the provider, which already has the
// result; sending one back would duplicate it.
func TestToLLMSkipsResultsForProviderExecutedTools(t *testing.T) {
	out := convert(t, stored(t, "msg_1", TypeAssistant, AssistantMessage{
		Model: testModel,
		Content: []AssistantContent{{
			Type: "tool", ID: "call_1", Name: "websearch",
			Provider: &ToolMeta{Executed: true},
			State:    &ToolState{Status: ToolCompleted, Output: "results"},
		}},
	}))
	if got := countParts(out, llm.PartToolCall); got != 1 {
		t.Errorf("expected the call to be kept, got %d", got)
	}
	if got := countParts(out, llm.PartToolResult); got != 0 {
		t.Errorf("expected no result for a provider-executed tool, got %d", got)
	}
}

// Reasoning is only replayable to the model that produced it — the signature
// belongs to that model. Against a different one it degrades to plain text
// rather than being sent as reasoning the model cannot verify.
func TestToLLMReasoningDependsOnTheModel(t *testing.T) {
	message := stored(t, "msg_1", TypeAssistant, AssistantMessage{
		Model:   testModel,
		Content: []AssistantContent{{Type: "reasoning", ID: "r1", Text: "thinking out loud"}},
	})

	same := convert(t, message)
	if got := countParts(same, llm.PartReasoning); got != 1 {
		t.Errorf("the same model should get its reasoning back, got %d reasoning parts", got)
	}

	other, err := ToLLMMessages([]StoredMessage{message}, ModelRef{ProviderID: "openai", ID: "gpt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := countParts(other, llm.PartReasoning); got != 0 {
		t.Errorf("another model must not receive reasoning parts, got %d", got)
	}
	if got := countParts(other, llm.PartText); got != 1 {
		t.Errorf("the reasoning text should survive as text, got %d text parts", got)
	}
}

// Empty content is dropped rather than sent: providers reject empty text
// blocks, and an assistant message with nothing in it is not a turn.
func TestToLLMDropsEmptyContent(t *testing.T) {
	t.Run("assistant with no content", func(t *testing.T) {
		out := convert(t, stored(t, "msg_1", TypeAssistant, AssistantMessage{Model: testModel}))
		if len(out) != 0 {
			t.Fatalf("expected nothing, got %+v", out)
		}
	})

	t.Run("assistant with only empty text", func(t *testing.T) {
		out := convert(t, stored(t, "msg_1", TypeAssistant, AssistantMessage{
			Model:   testModel,
			Content: []AssistantContent{{Type: "text", ID: "t", Text: ""}},
		}))
		if len(out) != 0 {
			t.Fatalf("expected nothing, got %+v", out)
		}
	})

	// The reachable case: a prompt that is only an attachment. The server
	// accepts it (text is optional when files are present), and the empty
	// text part that used to ride along with it made Anthropic reject the
	// whole turn.
	t.Run("user message with only an attachment", func(t *testing.T) {
		out := convert(t, stored(t, "msg_1", TypeUser, UserMessage{
			Files: []FileAttachment{{Name: "shot.png", Mime: "image/png", URI: "data:image/png;base64,AAAA"}},
		}))
		if len(out) != 1 {
			t.Fatalf("expected the attachment to be sent, got %+v", out)
		}
		if got := countParts(out, llm.PartText); got != 0 {
			t.Errorf("an empty text part must not be sent, got %d", got)
		}
		if got := countParts(out, llm.PartImage); got != 1 {
			t.Errorf("expected one image part, got %d", got)
		}
	})

	t.Run("user message with neither text nor attachment", func(t *testing.T) {
		out := convert(t, stored(t, "msg_1", TypeUser, UserMessage{}))
		if len(out) != 0 {
			t.Fatalf("expected nothing, got %+v", out)
		}
	})
}

// Only attachments whose bytes are in the message can be forwarded. A path or
// http reference would need fetching, which nothing here does, so it is
// dropped rather than sent as a reference the model cannot follow.
func TestToLLMForwardsOnlyInlineAttachments(t *testing.T) {
	out := convert(t, stored(t, "msg_1", TypeUser, UserMessage{
		Text: "look at these",
		Files: []FileAttachment{
			{Name: "a.png", Mime: "image/png", URI: "data:image/png;base64,AAAA"},
			{Name: "b.png", Mime: "image/png", URI: "file:///tmp/b.png"},
			{Name: "c.png", Mime: "image/png", URI: "https://example.invalid/c.png"},
			{Name: "d.txt", Mime: "text/plain", URI: "data:text/plain,not-base64"},
		},
	}))
	if got := countParts(out, llm.PartImage); got != 1 {
		t.Fatalf("expected only the inline base64 attachment, got %d image parts", got)
	}
	for _, part := range out[0].Content {
		if part.Type != llm.PartImage {
			continue
		}
		if part.Mime != "image/png" || part.Data != "AAAA" {
			t.Errorf("attachment decoded as %q / %q", part.Mime, part.Data)
		}
	}
}

// Records that exist for the reader of a session, not for the model, are not
// part of the conversation.
func TestToLLMDropsUIOnlyRecords(t *testing.T) {
	out := convert(t,
		stored(t, "m1", TypeAgentSwitched, map[string]any{"agent": "plan"}),
		stored(t, "m2", TypeModelSwitched, map[string]any{"model": "gpt"}),
	)
	if len(out) != 0 {
		t.Fatalf("agent and model switches are not conversation, got %+v", out)
	}
}

// Each remaining record type lowers to the role the model should see it as.
func TestToLLMRoleMapping(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		payload any
		role    string
		want    string
	}{
		{TypeUser, UserMessage{Text: "hello"}, llm.RoleUser, "hello"},
		{TypeSynthetic, map[string]any{"text": "injected"}, llm.RoleUser, "injected"},
		{TypeSystem, map[string]any{"text": "be terse"}, llm.RoleSystem, "be terse"},
		{TypeShell, map[string]any{"command": "ls", "output": "a\nb"}, llm.RoleUser, "Shell command: ls"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			out := convert(t, stored(t, "m1", tc.kind, tc.payload))
			if len(out) != 1 {
				t.Fatalf("expected one message, got %d", len(out))
			}
			if out[0].Role != tc.role {
				t.Errorf("role = %q, want %q", out[0].Role, tc.role)
			}
			if !strings.Contains(out[0].Content[0].Text, tc.want) {
				t.Errorf("text = %q, want it to contain %q", out[0].Content[0].Text, tc.want)
			}
		})
	}
}

// A compaction is replayed as historical context, and says so: without the
// framing the model reads a summary of its own past instructions as fresh
// ones and acts on them again.
func TestToLLMCompactionIsFramedAsHistory(t *testing.T) {
	out := convert(t, stored(t, "m1", TypeCompaction, map[string]any{
		"summary": "we refactored the parser",
		"recent":  "last few messages",
	}))
	if len(out) != 1 || out[0].Role != llm.RoleUser {
		t.Fatalf("expected one user message, got %+v", out)
	}
	text := out[0].Content[0].Text
	for _, want := range []string{
		"conversation-checkpoint",
		"historical context, not as new instructions",
		"we refactored the parser",
		"last few messages",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("checkpoint text is missing %q:\n%s", want, text)
		}
	}
}

// Ordering is the contract: tool results follow the assistant message that
// called them, so the provider can pair them up.
func TestToLLMOrdersResultsAfterTheirCall(t *testing.T) {
	out := convert(t,
		stored(t, "m1", TypeUser, UserMessage{Text: "run it"}),
		stored(t, "m2", TypeAssistant, AssistantMessage{
			Model: testModel,
			Content: []AssistantContent{
				{Type: "text", ID: "t", Text: "running"},
				{Type: "tool", ID: "call_1", Name: "bash", State: &ToolState{Status: ToolCompleted, Output: "done"}},
			},
		}),
	)
	if len(out) != 3 {
		t.Fatalf("expected user, assistant, tool-result; got %d: %+v", len(out), out)
	}
	if out[0].Role != llm.RoleUser || out[1].Role != llm.RoleAssistant {
		t.Fatalf("unexpected roles: %q, %q", out[0].Role, out[1].Role)
	}
	if out[2].Content[0].Type != llm.PartToolResult {
		t.Fatalf("the tool result must follow its call, got %q", out[2].Content[0].Type)
	}
	// The call and its result agree on the id, which is what pairs them.
	if out[1].Content[1].ToolCallID != out[2].Content[0].ToolCallID {
		t.Errorf("call id %q does not match result id %q",
			out[1].Content[1].ToolCallID, out[2].Content[0].ToolCallID)
	}
}

func TestToLLMReportsMalformedRecords(t *testing.T) {
	_, err := ToLLMMessages([]StoredMessage{{
		ID: "m1", Type: TypeUser, Data: []byte("{not json"),
	}}, testModel)
	if err == nil {
		t.Fatal("a record that cannot be decoded must be reported, not skipped")
	}
}

// The 4-chars-per-token heuristic compaction budgets against, ported from
// upstream's util.token.estimate cases.
func TestEstimateTokens(t *testing.T) {
	for _, tc := range []struct {
		text string
		want int
	}{
		{"", 0},
		{"a", 1},
		{"abcd", 1},
		{"abcde", 2},
		{strings.Repeat("x", 400), 100},
	} {
		if got := estimateTokens(tc.text); got != tc.want {
			t.Errorf("estimateTokens(%d chars) = %d, want %d", len(tc.text), got, tc.want)
		}
	}
}
