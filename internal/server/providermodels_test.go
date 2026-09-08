package server

import (
	"testing"

	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/provider"
)

func strp(s string) *string { return &s }

// modelVariants lists what the runner itself would honor, in catalog order:
// effort values as declared, budget variants high before max — cycle steps
// this list, so map order would scramble the walk on every open.
func TestModelVariantsUseCatalogOrder(t *testing.T) {
	anthropic := modelsdev.Provider{NPM: "@ai-sdk/anthropic"}
	openai := modelsdev.Provider{NPM: "@ai-sdk/openai"}

	// Effort: declared order, "none" for a null entry.
	effort := modelsdev.Model{ReasoningOptions: []modelsdev.ReasoningOption{
		{Type: "effort", Values: []*string{strp("minimal"), nil, strp("high")}},
	}}
	got := modelVariants("openai", openai, effort)
	if len(got) != 3 || got[0] != "minimal" || got[1] != "none" || got[2] != "high" {
		t.Fatalf("effort variants = %v, want [minimal none high]", got)
	}

	// Budget tokens: high before max (claude-sonnet-4-5's real shape).
	budget := modelsdev.Model{ReasoningOptions: []modelsdev.ReasoningOption{
		{Type: "budget_tokens", Min: f64p(1024)},
	}, Limit: modelsdev.Limit{Output: 64000}}
	got = modelVariants("anthropic", anthropic, budget)
	if len(got) != 2 || got[0] != "high" || got[1] != "max" {
		t.Fatalf("budget variants = %v, want [high max]", got)
	}

	// No reasoning options: no variants.
	plain := modelsdev.Model{}
	if got := modelVariants("openai", openai, plain); got != nil {
		t.Fatalf("plain model variants = %v, want none", got)
	}

	// Sanity against the resolver the runner uses: every advertised id
	// resolves to a real option patch, so the TUI can never offer a variant
	// the server would ignore.
	variants := provider.ReasoningVariants(provider.Protocol(anthropic.NPM), budget.ReasoningOptions, int(budget.Limit.Output))
	for _, id := range got {
		if _, ok := variants[id]; !ok {
			t.Errorf("advertised variant %q does not resolve", id)
		}
	}
}

func f64p(f float64) *float64 { return &f }
