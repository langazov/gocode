package anthropic

import (
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
)

// TestConvertRequestAppliesThinking is the regression for "I can't see
// thinking in the go port": a selected reasoning variant's "thinking" patch
// (internal/provider.ReasoningVariants) must translate into the native
// Anthropic request field, and MaxTokens must grow past the thinking
// budget — Anthropic rejects max_tokens <= thinking.budget_tokens.
func TestConvertRequestAppliesThinking(t *testing.T) {
	req, err := convertRequest(llm.Request{
		ModelID:   "claude-sonnet-4-5",
		MaxTokens: 8192,
		Reasoning: map[string]any{
			"thinking": map[string]any{"type": "enabled", "budget_tokens": 16000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Thinking == nil {
		t.Fatal("expected Thinking to be set")
	}
	if req.Thinking.Type != "enabled" || req.Thinking.BudgetTokens != 16000 {
		t.Fatalf("Thinking = %+v", req.Thinking)
	}
	if req.MaxTokens <= req.Thinking.BudgetTokens {
		t.Fatalf("MaxTokens (%d) must exceed budget_tokens (%d)", req.MaxTokens, req.Thinking.BudgetTokens)
	}
}

func TestConvertRequestNoReasoningLeavesThinkingUnset(t *testing.T) {
	req, err := convertRequest(llm.Request{ModelID: "claude-sonnet-4-5", MaxTokens: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if req.Thinking != nil {
		t.Fatalf("expected no Thinking, got %+v", req.Thinking)
	}
	if req.MaxTokens != 8192 {
		t.Fatalf("MaxTokens should be unchanged, got %d", req.MaxTokens)
	}
}

func TestConvertRequestIgnoresMalformedReasoning(t *testing.T) {
	req, err := convertRequest(llm.Request{
		ModelID:   "claude-sonnet-4-5",
		MaxTokens: 8192,
		Reasoning: map[string]any{"thinking": "not-a-map"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if req.Thinking != nil {
		t.Fatalf("expected malformed reasoning to be ignored, got %+v", req.Thinking)
	}
}
