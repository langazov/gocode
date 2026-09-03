package main

import (
	"context"
	"sort"

	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/session"
)

// pricingResolver adapts the models.dev catalog into session.Runner.Pricing,
// the same injection shape reasoningVariantsResolver uses to keep the catalog
// out of internal/session.
//
// Rate selection follows packages/opencode/src/session/session.ts: a model can
// price differently once a turn's input passes a threshold, expressed either as
// explicit context tiers (largest matching tier wins) or as the older
// context_over_200k block. A model with no cost entry resolves to "unknown"
// rather than free, so the runner records no cost instead of a wrong one.
func pricingResolver(catalog *modelsdev.Service) session.PricingResolver {
	return func(providerID, modelID string, contextTokens int) (session.TokenRates, bool) {
		data, err := catalog.Get(context.Background())
		if err != nil {
			return session.TokenRates{}, false
		}
		providerEntry, ok := data[providerID]
		if !ok {
			return session.TokenRates{}, false
		}
		model, ok := providerEntry.Models[modelID]
		if !ok || model.Cost == nil {
			return session.TokenRates{}, false
		}
		return ratesFor(model.Cost, contextTokens), true
	}
}

// ratesFor picks the rate block that applies at contextTokens.
func ratesFor(cost *modelsdev.Cost, contextTokens int) session.TokenRates {
	rates := cost.Rates

	// Context tiers take precedence, largest matching threshold first.
	tiers := make([]modelsdev.CostTier, 0, len(cost.Tiers))
	for _, tier := range cost.Tiers {
		if tier.Tier.Type == "context" && float64(contextTokens) > tier.Tier.Size {
			tiers = append(tiers, tier)
		}
	}
	if len(tiers) > 0 {
		sort.Slice(tiers, func(i, j int) bool { return tiers[i].Tier.Size > tiers[j].Tier.Size })
		rates = tiers[0].Rates
	} else if cost.ContextOver200k != nil && contextTokens > 200_000 {
		rates = *cost.ContextOver200k
	}

	out := session.TokenRates{Input: rates.Input, Output: rates.Output}
	if rates.CacheRead != nil {
		out.CacheRead = *rates.CacheRead
	}
	if rates.CacheWrite != nil {
		out.CacheWrite = *rates.CacheWrite
	}
	return out
}
