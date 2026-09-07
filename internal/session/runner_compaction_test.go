package session

import (
	"context"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/tool"
)

// resolveModelLimit is a thin wrapper so the table test reads as data.
func resolveModelLimit(resolver ContextLimitResolver, static int, model ModelRef) int {
	runner := &Runner{ContextLimit: static, ContextLimitResolver: resolver}
	return runner.effectiveContextLimit(model)
}

// The proactive compaction check must budget against the turn model's real
// context window from the catalog, not a single static default. A 128k model
// compacts early; a 1M model keeps its full window; an unknown model falls
// back to the static default.
func TestCompactionBudgetsAgainstTheModelContextLimit(t *testing.T) {
	cases := []struct {
		name     string
		resolver ContextLimitResolver
		static   int
		model    ModelRef
		want     int
	}{
		{
			name:     "catalog limit wins for a known model",
			resolver: func(string, string) (int, bool) { return 128_000, true },
			static:   200_000,
			model:    ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
			want:     128_000,
		},
		{
			name:     "unknown model falls back to the static default",
			resolver: func(string, string) (int, bool) { return 0, false },
			static:   200_000,
			model:    ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
			want:     200_000,
		},
		{
			name:     "nil resolver falls back to the static default",
			resolver: nil,
			static:   200_000,
			model:    ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
			want:     200_000,
		},
		{
			name:     "a 1M model compacts at its own boundary",
			resolver: func(string, string) (int, bool) { return 1_000_000, true },
			static:   200_000,
			model:    ModelRef{ProviderID: "google", ID: "gemini-1m"},
			want:     1_000_000,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveModelLimit(c.resolver, c.static, c.model); got != c.want {
				t.Fatalf("budgeted limit = %d, want %d", got, c.want)
			}
		})
	}
}

// A model whose catalog window is 128k must compact proactively on history
// the old static 200k budget would have let through: the ~120k-token history
// is past 128k−20k but under 200k−20k. Both subtests use the same history;
// only the resolved window differs.
func TestCompactIfNeededFiresOnASmallWindowModel(t *testing.T) {
	// 60_000 × 8 chars = 480_000 chars → ~120k tokens. The decision sees the
	// prompt alone: the assistant reply lands after compactIfNeeded ran.
	prompt := strings.Repeat("context ", 60_000)
	newRunner := func(t *testing.T, window int) (*Runner, *event.Bus) {
		provider := &fakeProvider{turns: [][]llm.StreamEvent{{
			{Type: llm.EventTextDelta, Text: "ok"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		}}}
		runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
		runner.ContextLimit = 200_000 // the old static default
		runner.ContextLimitResolver = func(string, string) (int, bool) { return window, true }
		runner.Compactor = &Compactor{
			Bus:      bus,
			Provider: &summaryProvider{},
			Settings: DefaultCompactionSettings(),
		}
		return runner, bus
	}
	hasCompaction := func(t *testing.T, runner *Runner) bool {
		history, err := runner.Messages.List(context.Background(), "ses_1")
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range history {
			if message.Type == "compaction" {
				return true
			}
		}
		return false
	}

	t.Run("compacts at the 128k window", func(t *testing.T) {
		runner, bus := newRunner(t, 128_000)
		admitPrompt(t, bus, runner, prompt)
		if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
			t.Fatal(err)
		}
		if !hasCompaction(t, runner) {
			t.Fatal("~150k tokens on a 128k model must compact proactively, before the provider rejects the request")
		}
	})

	t.Run("the same history rides on a 200k window", func(t *testing.T) {
		// This subtest is the old behavior: budget = 200k static, the same
		// ~150k history did not compact and the turn overflowed at the
		// provider instead.
		runner, bus := newRunner(t, 200_000)
		admitPrompt(t, bus, runner, prompt)
		if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
			t.Fatal(err)
		}
		if hasCompaction(t, runner) {
			t.Fatal("~150k tokens on a 200k window must not compact")
		}
	})
}

// An unknown-model resolver (false) must leave the static default in charge:
// a small history on a 200k fallback budget must not compact.
func TestCompactIfNeededKeepsStaticDefaultWhenModelUnknown(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "ok"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.ContextLimit = 200_000
	runner.ContextLimitResolver = func(string, string) (int, bool) { return 0, false }
	runner.Compactor = &Compactor{
		Bus:      bus,
		Provider: &summaryProvider{},
		Settings: DefaultCompactionSettings(),
	}
	ctx := context.Background()

	// ~5k tokens of history: nowhere near 200k−20k.
	admitPrompt(t, bus, runner, strings.Repeat("context ", 20_000))
	if err := runner.Run(ctx, RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	history, err := runner.Messages.ListForRunner(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range history {
		if message.Type == "compaction" {
			t.Fatal("small history must not compact against the static default")
		}
	}
}

// GLM-5.3-Flash and Claude Opus 5 hold 1M tokens per vendor documentation
// (docs.z.ai/guides/llm/glm-5.3-flash; platform.claude.com context-windows).
// A history past the old static 200k budget must ride along uncompacted on
// either of them — compacting there was the bug.
func TestLongContextModelsRidePastTheOldStaticBudget(t *testing.T) {
	// 300_000 × 8 chars = 2_400_000 chars → ~600k tokens: far past
	// 200k−20k, comfortably under 1M−20k.
	prompt := strings.Repeat("context ", 300_000)
	for _, model := range []struct {
		name string
		ref  ModelRef
	}{
		{"glm-5.3-flash", ModelRef{ProviderID: "zhipuai", ID: "glm-5.3-flash"}},
		{"claude-opus-5", ModelRef{ProviderID: "anthropic", ID: "claude-opus-5"}},
	} {
		t.Run(model.name, func(t *testing.T) {
			provider := &fakeProvider{turns: [][]llm.StreamEvent{{
				{Type: llm.EventTextDelta, Text: "done"},
				{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 600_000}},
			}}}
			runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
			runner.ContextLimit = 200_000 // the old static default
			runner.ContextLimitResolver = func(providerID, modelID string) (int, bool) {
				// The catalog's 1M entries, per the vendor docs.
				if (providerID == "zhipuai" && modelID == "glm-5.3-flash") ||
					(providerID == "anthropic" && modelID == "claude-opus-5") {
					return 1_000_000, true
				}
				return 0, false
			}
			runner.Compactor = &Compactor{
				Bus:      bus,
				Provider: &summaryProvider{},
				Settings: DefaultCompactionSettings(),
			}

			// The session runs on the long-context model itself, the way
			// SetModel stores it: without this the fixture's default
			// (anthropic/claude-sonnet-4-5) misses the resolver and the
			// static 200k fallback silently applies.
			ctx := context.Background()
			if _, err := runner.DB.Exec(ctx,
				`UPDATE session SET model = ? WHERE id = 'ses_1'`,
				`{"providerID":"`+model.ref.ProviderID+`","id":"`+model.ref.ID+`"}`); err != nil {
				t.Fatal(err)
			}

			admitPrompt(t, bus, runner, prompt)
			if err := runner.Run(ctx, RunInput{SessionID: "ses_1"}); err != nil {
				t.Fatal(err)
			}
			history, err := runner.Messages.List(context.Background(), "ses_1")
			if err != nil {
				t.Fatal(err)
			}
			for _, message := range history {
				if message.Type == "compaction" {
					t.Fatalf("%s holds 1M tokens; a ~600k history must not compact", model.name)
				}
			}
		})
	}
}
