package llm_test

import (
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

func cacheRequest() llm.Request {
	return llm.Request{
		System: []string{"You are gocode."},
		Messages: []llm.Message{
			llm.UserText("m1", "first"),
			llm.UserText("m2", "second"),
			llm.AssistantText("m3", "reply"),
		},
		Tools: []llm.ToolDefinition{{Name: "bash"}, {Name: "read"}},
	}
}

func TestApplyCachePolicyAuto(t *testing.T) {
	out := llm.ApplyCachePolicy(cacheRequest())

	if out.Tools[0].Cache != nil {
		t.Error("only the last tool is marked")
	}
	if out.Tools[1].Cache == nil {
		t.Error("last tool should be marked")
	}
	if out.SystemCache == nil {
		t.Error("system prompt should be marked")
	}
	if out.Messages[1].Content[0].Cache == nil {
		t.Error("newest user message should be marked")
	}
	if out.Messages[0].Content[0].Cache != nil || out.Messages[2].Content[0].Cache != nil {
		t.Error("only the newest user message is marked")
	}
}

// The request may be reused across retries and providers, so marking has to
// copy rather than write through the caller's slices.
func TestApplyCachePolicyDoesNotMutateInput(t *testing.T) {
	in := cacheRequest()
	llm.ApplyCachePolicy(in)

	if in.SystemCache != nil {
		t.Error("SystemCache written back to the caller's request")
	}
	if in.Tools[1].Cache != nil {
		t.Error("tool hint written back to the caller's slice")
	}
	for i, message := range in.Messages {
		for j, part := range message.Content {
			if part.Cache != nil {
				t.Errorf("message %d part %d hint written back to the caller's slice", i, j)
			}
		}
	}
}

// Automatic placement fills gaps; it never moves a hint the caller placed.
func TestApplyCachePolicyKeepsManualHints(t *testing.T) {
	manual := &llm.CacheHint{TTLSeconds: 3600}
	in := cacheRequest()
	in.Messages[1].Content[0].Cache = manual
	in.Tools[1].Cache = manual
	in.SystemCache = manual

	out := llm.ApplyCachePolicy(in)
	if out.Messages[1].Content[0].Cache != manual {
		t.Error("message hint replaced")
	}
	if out.Tools[1].Cache != manual {
		t.Error("tool hint replaced")
	}
	if out.SystemCache != manual {
		t.Error("system hint replaced")
	}
}

func TestApplyCachePolicyNone(t *testing.T) {
	in := cacheRequest()
	in.Cache = &llm.CachePolicy{}
	out := llm.ApplyCachePolicy(in)

	if out.SystemCache != nil || out.Tools[1].Cache != nil || out.Messages[1].Content[0].Cache != nil {
		t.Error("an empty policy should place nothing")
	}
}

func TestApplyCachePolicyLatestAssistant(t *testing.T) {
	in := cacheRequest()
	in.Cache = &llm.CachePolicy{Messages: llm.CacheLatestAssistantMessage}
	out := llm.ApplyCachePolicy(in)

	if out.Messages[2].Content[0].Cache == nil {
		t.Error("newest assistant message should be marked")
	}
	if out.Messages[1].Content[0].Cache != nil {
		t.Error("user message should be left alone")
	}
}

// A message with no text part at all still takes a breakpoint, on its final
// part.
func TestApplyCachePolicyMarksLastPartWhenNoText(t *testing.T) {
	in := llm.Request{
		Messages: []llm.Message{{ID: "m1", Role: llm.RoleUser, Content: []llm.ContentPart{
			{Type: llm.PartImage, Mime: "image/png", Data: "aGk="},
			{Type: llm.PartImage, Mime: "image/png", Data: "eW8="},
		}}},
	}
	out := llm.ApplyCachePolicy(in)
	if out.Messages[0].Content[1].Cache == nil {
		t.Error("last part should take the breakpoint when there is no text")
	}
	if out.Messages[0].Content[0].Cache != nil {
		t.Error("only one part is marked")
	}
}

// Tool results are their own role in this port, so the latest-user-message
// strategy skips them and stays anchored on the message that opened the turn.
// This is the placement that makes a tool loop cacheable: the prefix under the
// breakpoint is identical on every step of the turn.
func TestApplyCachePolicySkipsToolMessages(t *testing.T) {
	in := llm.Request{
		Messages: []llm.Message{
			llm.UserText("m1", "run ls"),
			llm.ToolResultMessage("m2", "call_1", "bash", "a.go", false),
		},
	}
	out := llm.ApplyCachePolicy(in)
	if out.Messages[0].Content[0].Cache == nil {
		t.Error("user message should hold the breakpoint")
	}
	if out.Messages[1].Content[0].Cache != nil {
		t.Error("tool result should not take the breakpoint")
	}
}

// Text wins over a trailing attachment: the text is what the next request
// repeats verbatim.
func TestApplyCachePolicyPrefersLastTextPart(t *testing.T) {
	in := llm.Request{
		Messages: []llm.Message{{ID: "m1", Role: llm.RoleUser, Content: []llm.ContentPart{
			{Type: llm.PartText, Text: "look at this"},
			{Type: llm.PartImage, Mime: "image/png", Data: "aGk="},
		}}},
	}
	out := llm.ApplyCachePolicy(in)
	if out.Messages[0].Content[0].Cache == nil {
		t.Error("text part should be marked")
	}
	if out.Messages[0].Content[1].Cache != nil {
		t.Error("image part should be left alone")
	}
}

func TestApplyCachePolicyEmptyRequest(t *testing.T) {
	out := llm.ApplyCachePolicy(llm.Request{})
	if out.SystemCache != nil || len(out.Tools) != 0 || len(out.Messages) != 0 {
		t.Error("nothing to mark should stay nothing")
	}
}

func TestBreakpointsBudget(t *testing.T) {
	budget := llm.NewBreakpoints(2)
	hint := &llm.CacheHint{}

	if budget.Take(nil) {
		t.Error("a nil hint is not a breakpoint and must not spend one")
	}
	if !budget.Take(hint) || !budget.Take(hint) {
		t.Fatal("the first two breakpoints fit the budget")
	}
	if budget.Take(hint) {
		t.Error("the third overruns the budget")
	}
	if budget.Dropped != 1 {
		t.Errorf("Dropped = %d, want 1", budget.Dropped)
	}
	if budget.Remaining != 0 {
		t.Errorf("Remaining = %d, want 0", budget.Remaining)
	}
}

func TestCacheHintExtended(t *testing.T) {
	var nilHint *llm.CacheHint
	if nilHint.Extended() {
		t.Error("a nil hint asks for nothing")
	}
	if (&llm.CacheHint{}).Extended() {
		t.Error("the zero hint takes the default window")
	}
	if (&llm.CacheHint{TTLSeconds: 3599}).Extended() {
		t.Error("under an hour rounds down to the default window")
	}
	if !(&llm.CacheHint{TTLSeconds: 3600}).Extended() {
		t.Error("an hour is the extended window")
	}
}
