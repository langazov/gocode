package session

import (
	"context"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// A session's model can change mid-flight (TUI model dialog, /model, the
// task tool's agent switches). Everything model-derived must come from the
// session row, re-read per turn — never from boot-time state. This test
// flips the session from a 200k model to a 1M one between turns and checks
// every derived value on the wire follows:
//
//   - routing: request.ProviderID/ModelID (lazyProvider keys clients on it)
//   - max_tokens: resolved from the new model's output limit
//   - reasoning options: resolved from the new model's variant
//   - compaction budget: the new model's context limit, not the old one's
//   - pricing: the new model's rates for the settled step
//
// What must NOT follow is the old turn's reasoning blocks: to_llm rewrites
// another model's reasoning as plain text so the new provider never sees
// foreign thinking signatures.
func TestModelSwitchRebindsEveryDerivedValue(t *testing.T) {
	oldModel := ModelRef{ProviderID: "small-inc", ID: "small-200k"}
	newModel := ModelRef{ProviderID: "big-inc", ID: "big-1m", Variant: "max"}

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{{Type: llm.EventTextDelta, Text: "from small"},
			{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 100, Output: 10}}},
		{{Type: llm.EventTextDelta, Text: "from big"},
			{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 500_000, Output: 20}}},
	}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.ContextLimit = 200_000 // the static fallback: neither model known
	runner.ContextLimitResolver = func(providerID, modelID string) (int, bool) {
		if providerID == newModel.ProviderID && modelID == newModel.ID {
			return 1_000_000, true
		}
		if providerID == oldModel.ProviderID && modelID == oldModel.ID {
			return 200_000, true
		}
		return 0, false
	}
	runner.OutputLimit = func(providerID, modelID string) (int, bool) {
		if providerID == newModel.ProviderID && modelID == newModel.ID {
			return 128_000, true
		}
		return 8_192, true // the small model's cap
	}
	runner.ReasoningVariants = func(providerID, modelID, variantID string) map[string]any {
		if providerID == newModel.ProviderID && modelID == newModel.ID {
			return map[string]any{"thinking": map[string]any{"type": "enabled"}}
		}
		return nil
	}
	priced := map[string]float64{}
	runner.Pricing = func(providerID, modelID string, _ int) (TokenRates, bool) {
		if providerID == newModel.ProviderID {
			priced[modelID] = 5.0
			return TokenRates{Input: 5}, true
		}
		priced[modelID] = 1.0
		return TokenRates{Input: 1}, true
	}
	runner.Compactor = &Compactor{
		Bus:      bus,
		Provider: &summaryProvider{},
		Settings: DefaultCompactionSettings(),
	}
	ctx := context.Background()

	// Turn 1 runs on the old model: history stays small (its 200k budget).
	setSessionModel(t, runner, oldModel)
	admitPrompt(t, bus, runner, "first turn")
	if err := runner.Run(ctx, RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.requests[0].ProviderID + "/" + provider.requests[0].ModelID; got != oldModel.ProviderID+"/"+oldModel.ID {
		t.Fatalf("first turn routed to %s, want %s", got, oldModel.ProviderID+"/"+oldModel.ID)
	}
	if got := provider.requests[0].MaxTokens; got != 8_192 {
		t.Fatalf("first turn max_tokens = %d, want the small model's 8192", got)
	}
	if provider.requests[0].Reasoning != nil {
		t.Fatalf("first turn should carry no reasoning options, got %+v", provider.requests[0].Reasoning)
	}

	// The switch: exactly what the TUI model dialog and the /model endpoint do,
	// with a --variant-style reasoning effort pinned the way cmd_run.go does.
	if _, err := runner.DB.Exec(ctx,
		`UPDATE session SET model = ? WHERE id = 'ses_1'`,
		`{"providerID":"`+newModel.ProviderID+`","id":"`+newModel.ID+`","variant":"`+newModel.Variant+`"}`); err != nil {
		t.Fatal(err)
	}

	// Turn 2 must run entirely on the new model's terms.
	admitPrompt2(t, bus, runner, "second turn")
	if err := runner.Run(ctx, RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	second := provider.requests[1]
	if got := second.ProviderID + "/" + second.ModelID; got != newModel.ProviderID+"/"+newModel.ID {
		t.Fatalf("second turn routed to %s, want the switched model %s", got, newModel.ProviderID+"/"+newModel.ID)
	}
	if second.MaxTokens != 128_000 {
		t.Fatalf("second turn max_tokens = %d, want the new model's 128000", second.MaxTokens)
	}
	if second.Reasoning == nil || second.Reasoning["thinking"] == nil {
		t.Fatalf("second turn must carry the new model's reasoning options, got %+v", second.Reasoning)
	}
	if _, ok := priced[newModel.ID]; !ok || priced[newModel.ID] != 5.0 {
		t.Fatalf("settled step priced at %v, want the new model's rates", priced)
	}

	// The compaction budget follows too: the new model's 1M window must not
	// have compacted this small history, and equally must not have inherited
	// the old budget (nothing proves no-compaction here except the budget
	// actually resolving from the new model — a large-history variant lives
	// in TestLongContextModelsRidePastTheOldStaticBudget).
	history, err := runner.Messages.List(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range history {
		if message.Type == "compaction" {
			t.Fatal("a small history must not compact just because the model switched")
		}
	}
}

func setSessionModel(t *testing.T, runner *Runner, model ModelRef) {
	t.Helper()
	if _, err := runner.DB.Exec(context.Background(),
		`UPDATE session SET model = ? WHERE id = 'ses_1'`,
		`{"providerID":"`+model.ProviderID+`","id":"`+model.ID+`"}`); err != nil {
		t.Fatal(err)
	}
}
