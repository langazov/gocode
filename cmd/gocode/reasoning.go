package main

import (
	"context"

	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/provider"
)

// reasoningVariantsResolver adapts the models.dev catalog into
// session.Runner.ReasoningVariants: given a resolved provider/model/variant
// for a turn, look up the model's catalog entry, compute its available
// reasoning variants (internal/provider.ReasoningVariants), and return the
// selected one's provider-specific options — or nil if the model or variant
// isn't found, so an unknown --variant just runs without reasoning rather
// than erroring the whole turn.
func reasoningVariantsResolver(catalog *modelsdev.Service) func(providerID, modelID, variantID string) map[string]any {
	return func(providerID, modelID, variantID string) map[string]any {
		data, err := catalog.Get(context.Background())
		if err != nil {
			return nil
		}
		providerEntry, ok := data[providerID]
		if !ok {
			return nil
		}
		model, ok := providerEntry.Models[modelID]
		if !ok {
			return nil
		}
		protocol := provider.Protocol(providerEntry.NPM)
		variants := provider.ReasoningVariants(protocol, model.ReasoningOptions, int(model.Limit.Output))
		return variants[variantID]
	}
}
