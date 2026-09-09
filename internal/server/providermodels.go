package server

import (
	"context"
	"sort"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/provider"
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

// modelVariants lists the selectable reasoning variants of one catalog
// model, in the order the turn runner itself resolves them
// (provider.ReasoningVariants): effort values when the catalog declares
// them, else budget_tokens' high/max, else none. Deterministic order is
// what the TUI's cycle gesture needs — variant.cycle steps this list and
// a map iteration order would scramble it on every open.
//
// Only catalog models have reasoning_options; config-defined models take
// none, which is also why appendModel passes an empty entry for them.
func modelVariants(providerID string, entry modelsdev.Provider, model modelsdev.Model) []string {
	variants := provider.ReasoningVariants(provider.Protocol(entry.NPM), model.ReasoningOptions, int(model.Limit.Output))
	if len(variants) == 0 {
		return nil
	}
	ids := make([]string, 0, len(variants))
	for id := range variants {
		ids = append(ids, id)
	}
	// ReasoningVariants only ever returns map keys, never a declared order,
	// so impose the catalog's own order: effort values as listed, and for
	// budget variants high before max. This is the order the models.dev
	// entry declares, and the one a user cycling variants expects to walk.
	if len(model.ReasoningOptions) > 0 {
		for _, opt := range model.ReasoningOptions {
			if opt.Type != "effort" {
				continue
			}
			ordered := make([]string, 0, len(ids))
			for _, value := range opt.Values {
				id := "none"
				if value != nil {
					id = *value
				}
				if _, ok := variants[id]; ok {
					ordered = append(ordered, id)
				}
			}
			if len(ordered) == len(ids) {
				return ordered
			}
			break
		}
	}
	// Budget (or unlisted effort) variants: high then max, then anything
	// unexpected alphabetically so the result is still stable.
	sort.Slice(ids, func(i, j int) bool {
		return variantOrder(ids[i]) < variantOrder(ids[j])
	})
	return ids
}

// variantOrder ranks budget-style variant ids high before max, pushing any
// other id after them alphabetically.
func variantOrder(id string) int {
	switch id {
	case "high":
		return 0
	case "max":
		return 1
	default:
		return 2
	}
}
