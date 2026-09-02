package server

import (
	"context"

	"github.com/anomalyco/opencode-go/internal/config"
	"github.com/anomalyco/opencode-go/internal/modelsdev"
	"github.com/anomalyco/opencode-go/internal/provider"
)

// resolveCatalog returns the models.dev catalog with every registered overlay
// applied — currently the opencode/Zen account's per-org provider config,
// which is the fourth and last layer of downloaded provider defaults.
//
// With no Zen credential stored this is a no-op returning the catalog
// unchanged, so the common path costs nothing.
func resolveCatalog(ctx context.Context, catalog modelsdev.Catalog) modelsdev.Catalog {
	return provider.ApplyOverlays(ctx, catalog)
}

// providerModels returns the models to advertise for a provider: the ones it
// publishes itself when it has an opinion, and the catalog's list otherwise.
//
// Only github-copilot currently publishes its own, and only when the user is
// logged in; every other provider takes the catalog path with no network call.
// A provider that fails to resolve keeps its catalog models rather than
// disappearing from the picker.
func providerModels(ctx context.Context, providerID string, entry modelsdev.Provider, cfg *config.Config) map[string]modelsdev.Model {
	if !provider.PublishesModels(providerID, entry) {
		return entry.Models
	}
	resolved, err := provider.Resolve(ctx, providerID, cfg)
	if err != nil {
		return entry.Models
	}
	return resolved.LiveModels(ctx)
}
