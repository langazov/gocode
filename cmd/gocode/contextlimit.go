package main

import (
	"context"

	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/session"
)

// contextLimitResolver adapts the models.dev catalog into
// session.Runner.ContextLimitResolver, the same injection shape
// pricingResolver and outputLimitResolver use to keep the catalog out of
// internal/session.
//
// The runner budgeted compaction against a single 200k default for every
// model. On a 128k model the proactive check could never fire (the estimate
// never crosses 200k−20k inside a 128k window), so every compaction went
// through the wasteful path: a failed provider request, error-string
// matching, compact, retry. On a 1M model it fired around 180k, discarding
// context the model had room for. Resolving per-model puts the proactive
// trigger at each model's real boundary.
func contextLimitResolver(catalog *modelsdev.Service) session.ContextLimitResolver {
	return func(providerID, modelID string) (int, bool) {
		data, err := catalog.Get(context.Background())
		if err != nil {
			return 0, false
		}
		providerEntry, ok := data[providerID]
		if !ok {
			return 0, false
		}
		model, ok := providerEntry.Models[modelID]
		if !ok || model.Limit.Context <= 0 {
			return 0, false
		}
		return int(model.Limit.Context), true
	}
}
