package provider

import (
	"testing"

	"github.com/langazov/gocode-go/internal/modelsdev"
)

func strPtr(s string) *string   { return &s }
func f64Ptr(f float64) *float64 { return &f }

func TestProtocolClassification(t *testing.T) {
	cases := map[string]string{
		"@ai-sdk/anthropic":         "anthropic",
		"@ai-sdk/google-vertex":     "gemini",
		"@ai-sdk/google":            "gemini",
		"@ai-sdk/openai":            "openai",
		"@ai-sdk/openai-compatible": "openai",
	}
	for npm, want := range cases {
		if got := Protocol(npm); got != want {
			t.Errorf("Protocol(%q) = %q, want %q", npm, got, want)
		}
	}
}

// TestReasoningVariantsBudgetTokens mirrors claude-sonnet-4-5's real
// models.dev catalog entry: reasoning_options [{type:"budget_tokens",
// min:1024}], limit.output 64000.
func TestReasoningVariantsBudgetTokens(t *testing.T) {
	options := []modelsdev.ReasoningOption{{Type: "budget_tokens", Min: f64Ptr(1024)}}
	variants := ReasoningVariants("anthropic", options, 64000)

	if len(variants) != 2 {
		t.Fatalf("expected high+max variants, got %v", variants)
	}
	high := variants["high"]["thinking"].(map[string]any)
	if high["type"] != "enabled" || high["budget_tokens"] != 16000 {
		t.Fatalf("high variant = %+v, want budget_tokens 16000", high)
	}
	max := variants["max"]["thinking"].(map[string]any)
	if max["budget_tokens"] != outputTokenMax-1 {
		t.Fatalf("max variant = %+v, want budget_tokens %d", max, outputTokenMax-1)
	}
}

func TestReasoningVariantsEffortOpenAI(t *testing.T) {
	options := []modelsdev.ReasoningOption{
		{Type: "effort", Values: []*string{strPtr("minimal"), strPtr("low"), strPtr("medium"), strPtr("high")}},
	}
	variants := ReasoningVariants("openai", options, 100000)
	if len(variants) != 4 {
		t.Fatalf("expected 4 effort variants, got %v", variants)
	}
	if variants["high"]["reasoning_effort"] != "high" {
		t.Fatalf("high variant = %+v", variants["high"])
	}
}

func TestReasoningVariantsEffortGemini(t *testing.T) {
	options := []modelsdev.ReasoningOption{{Type: "effort", Values: []*string{strPtr("low"), strPtr("high")}}}
	variants := ReasoningVariants("gemini", options, 100000)
	high := variants["high"]["thinkingConfig"].(map[string]any)
	if high["thinkingLevel"] != "high" || high["includeThoughts"] != true {
		t.Fatalf("high variant = %+v", high)
	}
}

// TestReasoningVariantsPrefersEffortOverBudget mirrors claude-opus-4-5,
// whose catalog entry declares both effort and budget_tokens options — TS
// prioritizes effort when present.
func TestReasoningVariantsPrefersEffortOverBudget(t *testing.T) {
	options := []modelsdev.ReasoningOption{
		{Type: "effort", Values: []*string{strPtr("low"), strPtr("medium"), strPtr("high")}},
		{Type: "budget_tokens", Min: f64Ptr(1024)},
	}
	variants := ReasoningVariants("anthropic", options, 64000)
	if _, ok := variants["low"]; !ok {
		t.Fatalf("expected effort-derived variant ids, got %v", variants)
	}
	if _, ok := variants["max"]; ok {
		t.Fatalf("budget-derived ids should not appear when effort is present, got %v", variants)
	}
}

// TestReasoningVariantsToggleOnlyProducesNothing matches every npm package
// this port speaks (anthropic/gemini/openai): TS's reasoningToggle only has
// non-empty results for alibaba/cohere, neither of which the Go port has an
// adapter for.
func TestReasoningVariantsToggleOnlyProducesNothing(t *testing.T) {
	options := []modelsdev.ReasoningOption{{Type: "toggle"}}
	for _, protocol := range []string{"anthropic", "gemini", "openai"} {
		if variants := ReasoningVariants(protocol, options, 64000); variants != nil {
			t.Fatalf("%s: expected no variants for toggle-only, got %v", protocol, variants)
		}
	}
}

func TestReasoningVariantsEmptyOptions(t *testing.T) {
	if variants := ReasoningVariants("anthropic", nil, 64000); variants != nil {
		t.Fatalf("expected nil for no reasoning_options, got %v", variants)
	}
}

// TestReasoningVariantsBudgetOpenAIUnsupported matches TS: the OpenAI/
// openai-compatible reasoningBudget case returns nothing (no budget-based
// reasoning param on that protocol), only effort is supported there.
func TestReasoningVariantsBudgetOpenAIUnsupported(t *testing.T) {
	options := []modelsdev.ReasoningOption{{Type: "budget_tokens", Min: f64Ptr(512), Max: f64Ptr(24576)}}
	if variants := ReasoningVariants("openai", options, 100000); variants != nil {
		t.Fatalf("expected no budget-based variants for openai protocol, got %v", variants)
	}
}
