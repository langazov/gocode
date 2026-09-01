package main

import (
	"testing"

	"github.com/anomalyco/opencode-go/internal/modelsdev"
)

func rate(v float64) *float64 { return &v }

func TestRatesForUsesTheBaseBlockBelowAnyThreshold(t *testing.T) {
	cost := &modelsdev.Cost{Rates: modelsdev.Rates{
		Input: 3, Output: 15, CacheRead: rate(0.3), CacheWrite: rate(3.75),
	}}
	got := ratesFor(cost, 1000)
	if got.Input != 3 || got.Output != 15 || got.CacheRead != 0.3 || got.CacheWrite != 3.75 {
		t.Fatalf("rates = %+v", got)
	}
}

// A model with no cache rates prices those buckets at zero rather than
// inheriting the input/output rate.
func TestRatesForLeavesAbsentCacheRatesAtZero(t *testing.T) {
	got := ratesFor(&modelsdev.Cost{Rates: modelsdev.Rates{Input: 3, Output: 15}}, 1000)
	if got.CacheRead != 0 || got.CacheWrite != 0 {
		t.Fatalf("absent cache rates should be zero, got %+v", got)
	}
}

func TestRatesForAppliesTheOver200kBlock(t *testing.T) {
	cost := &modelsdev.Cost{
		Rates:           modelsdev.Rates{Input: 3, Output: 15},
		ContextOver200k: &modelsdev.Rates{Input: 6, Output: 22.5},
	}
	if got := ratesFor(cost, 200_000); got.Input != 3 {
		t.Fatalf("at the threshold the base rates still apply, got %+v", got)
	}
	if got := ratesFor(cost, 200_001); got.Input != 6 || got.Output != 22.5 {
		t.Fatalf("past the threshold the over-200k rates apply, got %+v", got)
	}
}

// Tiers win over the over-200k block, and the largest matching tier wins.
func TestRatesForPicksTheLargestMatchingContextTier(t *testing.T) {
	small := modelsdev.CostTier{Rates: modelsdev.Rates{Input: 5}}
	small.Tier.Type, small.Tier.Size = "context", 100_000
	large := modelsdev.CostTier{Rates: modelsdev.Rates{Input: 9}}
	large.Tier.Type, large.Tier.Size = "context", 500_000
	other := modelsdev.CostTier{Rates: modelsdev.Rates{Input: 99}}
	other.Tier.Type, other.Tier.Size = "output", 1

	cost := &modelsdev.Cost{
		Rates:           modelsdev.Rates{Input: 3},
		Tiers:           []modelsdev.CostTier{small, large, other},
		ContextOver200k: &modelsdev.Rates{Input: 6},
	}

	if got := ratesFor(cost, 50_000); got.Input != 3 {
		t.Fatalf("below every tier the base rates apply, got %+v", got)
	}
	if got := ratesFor(cost, 150_000); got.Input != 5 {
		t.Fatalf("the 100k tier should apply, got %+v", got)
	}
	if got := ratesFor(cost, 600_000); got.Input != 9 {
		t.Fatalf("the largest matching tier should win, got %+v", got)
	}
}
