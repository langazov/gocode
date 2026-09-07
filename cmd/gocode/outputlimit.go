package main

import (
	"context"

	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/session"
)

// outputLimitResolver adapts the models.dev catalog into
// session.Runner.OutputLimit, the same injection shape pricingResolver uses to
// keep the catalog out of internal/session.
//
// The runner used to cap every request at 8192 output tokens regardless of the
// model. On a thinking model that is not a generous ceiling but a guillotine:
// reasoning is charged against the same budget, so a model with a 131072-token
// output limit would think its way through 8190 of those tokens, get cut off
// with finish "length", and return no text and no tool call — a turn that
// stopped mid-thought with nothing to show for it. See the glm-5.3-flash
// sessions where every truncated step reports reasoning 8188-8192, output 0-4.
func outputLimitResolver(catalog *modelsdev.Service) session.OutputLimitResolver {
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
		if !ok || model.Limit.Output <= 0 {
			return 0, false
		}
		return int(model.Limit.Output), true
	}
}
