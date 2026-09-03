package session

import (
	"context"
	"testing"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

func TestStepCostPricesEveryBucketPerMillionTokens(t *testing.T) {
	rates := TokenRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}
	usage := TokenUsage{Input: 1_000_000, Output: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000}

	if got := stepCost(rates, usage); got != 3+15+0.3+3.75 {
		t.Fatalf("cost = %v, want %v", got, 3+15+0.3+3.75)
	}
}

// Upstream bills reasoning at the output rate (models.dev has no separate one).
func TestStepCostBillsReasoningAtTheOutputRate(t *testing.T) {
	rates := TokenRates{Output: 15}
	reasoning := stepCost(rates, TokenUsage{Reasoning: 500_000})
	output := stepCost(rates, TokenUsage{Output: 500_000})
	if reasoning != output {
		t.Fatalf("reasoning cost %v should equal the same output cost %v", reasoning, output)
	}
}

func TestStepCostIsZeroWithoutRates(t *testing.T) {
	if got := stepCost(TokenRates{}, TokenUsage{Input: 1_000_000, Output: 1_000_000}); got != 0 {
		t.Fatalf("a free model costs nothing, got %v", got)
	}
}

// A missing catalog entry must record no cost rather than a guessed one.
func TestRunnerRecordsNoCostWithoutPricing(t *testing.T) {
	runner := &Runner{}
	if got := runner.stepCost("anthropic", "claude", TokenUsage{Input: 1_000_000}); got != 0 {
		t.Fatalf("no resolver means no cost, got %v", got)
	}
	runner.Pricing = func(string, string, int) (TokenRates, bool) { return TokenRates{Input: 3}, false }
	if got := runner.stepCost("anthropic", "claude", TokenUsage{Input: 1_000_000}); got != 0 {
		t.Fatalf("an unresolved model means no cost, got %v", got)
	}
}

// The session row carries the running totals the stats endpoint reports. They
// were never written: every session reported 0 tokens and $0.00 for its whole
// life, which is what made the sidebar look frozen.
func TestSettledStepAccumulatesSessionTotals(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{{Type: llm.EventTextDelta, Text: "one"},
			{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 1_000_000, Output: 200_000}}},
		{{Type: llm.EventTextDelta, Text: "two"},
			{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 500_000, Output: 100_000}}},
	}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.Pricing = func(string, string, int) (TokenRates, bool) {
		return TokenRates{Input: 3, Output: 15}, true
	}
	ctx := context.Background()

	admitPrompt(t, bus, runner, "first")
	if err := runner.Run(ctx, RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	service := &Service{DB: runner.DB, Bus: bus}
	stats, err := service.Stats(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if stats["tokensInput"] != 1_000_000 || stats["tokensOutput"] != 200_000 {
		t.Fatalf("first turn tokens not accumulated: %v", stats)
	}
	// 1M input at $3/M + 200k output at $15/M.
	if cost := stats["cost"].(float64); cost < 5.999 || cost > 6.001 {
		t.Fatalf("cost = %v, want 6.00", cost)
	}

	// A second turn adds to the running totals rather than replacing them.
	admitPrompt2(t, bus, runner, "second")
	if err := runner.Run(ctx, RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	stats, err = service.Stats(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	if stats["tokensInput"] != 1_500_000 || stats["tokensOutput"] != 300_000 {
		t.Fatalf("totals should accumulate across turns: %v", stats)
	}
	// The second turn adds 500k input ($1.50) + 100k output ($1.50).
	if cost := stats["cost"].(float64); cost < 8.999 || cost > 9.001 {
		t.Fatalf("cost = %v, want 9.00", cost)
	}
}

func admitPrompt2(t *testing.T, bus *event.Bus, runner *Runner, text string) {
	t.Helper()
	_, err := Admit(context.Background(), bus, runner.DB, AdmitInput{
		ID:        "msg_user_2",
		SessionID: "ses_1",
		Prompt:    Prompt{Text: text},
		Delivery:  DeliverySteer,
	})
	if err != nil {
		t.Fatal(err)
	}
}
