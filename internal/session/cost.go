package session

// Token pricing, ported from packages/opencode/src/session/session.ts's usage
// accounting.
//
// Every model's rates are quoted per million tokens. Reasoning tokens have no
// rate of their own and are billed at the output rate, which upstream marks
// with a TODO against models.dev — reproduced here so the two agree.
//
// The rates themselves come from models.dev, but this package must not import
// the catalog (the runner already takes its other catalog-derived inputs as
// injected funcs — see Runner.ReasoningVariants), so pricing arrives the same
// way: as a resolver the boot wiring supplies.

// TokenRates is one model's per-million-token pricing. A rate of zero means
// free, which is also the fallback when the catalog has no entry — a missing
// price must never invent one.
type TokenRates struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
}

// PricingResolver returns the rates that apply to a turn. contextTokens is the
// input size, which upstream uses to pick between a model's context tiers and
// its over-200k rates, so the resolver needs it to make that choice.
type PricingResolver func(providerID, modelID string, contextTokens int) (TokenRates, bool)

// stepCost prices one settled step. It mirrors the Decimal arithmetic in
// session.ts; float64 is enough here because the result is only ever displayed
// to cents, and the accumulated total is stored as a REAL either way.
func stepCost(rates TokenRates, usage TokenUsage) float64 {
	perMillion := func(tokens int, rate float64) float64 {
		if tokens <= 0 || rate <= 0 {
			return 0
		}
		return float64(tokens) * rate / 1_000_000
	}
	return perMillion(usage.Input, rates.Input) +
		perMillion(usage.Output, rates.Output) +
		perMillion(usage.CacheRead, rates.CacheRead) +
		perMillion(usage.CacheWrite, rates.CacheWrite) +
		// Reasoning is billed at the output rate; see this file's header.
		perMillion(usage.Reasoning, rates.Output)
}

// TokenUsage is the per-step token count stepCost prices.
type TokenUsage struct {
	Input      int
	Output     int
	Reasoning  int
	CacheRead  int
	CacheWrite int
}
