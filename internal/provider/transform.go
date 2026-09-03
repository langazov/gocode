package provider

import (
	"context"
	"sort"

	"github.com/langazov/gocode-go/internal/modelsdev"
)

// Transform adjusts a Resolved provider for quirks the models.dev catalog
// cannot express. It is this port's answer to the per-provider plugin files
// in packages/core/src/plugin/provider/, and each transform_*.go file here
// corresponds to one of them.
//
// Transforms exist for the minority of providers that need them. The catalog
// already carries the endpoint, key env vars and model list for ~180 of its
// ~212 providers, and those resolve with no transform at all — which is the
// point of sourcing defaults from a downloaded catalog rather than a table in
// the binary.
type Transform interface {
	// Matches reports whether this transform applies. It sees the provider id
	// and the raw catalog entry, so a transform can key on either the id or
	// the SDK package the catalog names.
	Matches(id string, entry modelsdev.Provider) bool

	// Apply adjusts the resolved provider in place. It runs after the generic
	// catalog/config resolution, so fields are already populated and a
	// transform overrides only what it cares about.
	//
	// Returning an error fails provider construction: use it for missing
	// required configuration (an unset resource name, an unusable credential),
	// not for "this provider is not configured", which is the key resolver's
	// job to report.
	Apply(ctx context.Context, r *Resolved) error
}

// AuthProvider is the optional half of the Transform contract: a transform
// implements it to advertise login methods beyond the env/key pair every
// catalog entry gets automatically. See authmethods.go.
type AuthProvider interface {
	AuthMethods() []Method
}

// ModelSource is implemented by a transform whose provider publishes its own
// model list instead of relying on the catalog (github-copilot does).
//
// It is deliberately separate from Apply. Apply runs on every provider
// resolution — including once per candidate inside Fallback's scan — so it
// must stay cheap and offline. Fetching a live model list is neither, and
// belongs on the path that actually wants the list. See Resolved.LiveModels.
type ModelSource interface {
	FetchModels(ctx context.Context, r *Resolved) (map[string]modelsdev.Model, error)
}

// registry holds transforms in registration order. Registration happens from
// each transform file's init(), so adding a provider means adding one file.
var registry []Transform

// Register adds a transform. It is called from init(); ordering between
// transforms is registration order, and transforms are not expected to
// conflict — each matches a disjoint set of providers.
func Register(t Transform) {
	registry = append(registry, t)
}

// transformsFor returns the transforms matching a provider.
func transformsFor(id string, entry modelsdev.Provider) []Transform {
	var out []Transform
	for _, t := range registry {
		if t.Matches(id, entry) {
			out = append(out, t)
		}
	}
	return out
}

// applyTransforms runs every matching transform against a resolved provider.
func applyTransforms(ctx context.Context, r *Resolved) error {
	for _, t := range transformsFor(r.ID, r.Entry) {
		if err := t.Apply(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

// CatalogOverlay is implemented by a transform that contributes catalog
// entries beyond its own provider. It is the hook for a *downloaded* set of
// provider defaults layered on top of models.dev — an opencode/Zen account's
// `/api/config` describes several providers at once, which is why this is
// catalog-shaped rather than per-provider like ModelSource.
type CatalogOverlay interface {
	Overlay(ctx context.Context) (modelsdev.Catalog, error)
}

// ApplyOverlays merges every registered overlay onto a catalog, entry by
// entry, and returns the result. The input is not modified.
//
// An overlay that fails is skipped: a per-account catalog is an enhancement,
// and losing it must not take the public catalog down with it.
func ApplyOverlays(ctx context.Context, catalog modelsdev.Catalog) modelsdev.Catalog {
	var overlays []modelsdev.Catalog
	for _, t := range registry {
		source, ok := t.(CatalogOverlay)
		if !ok {
			continue
		}
		overlay, err := source.Overlay(ctx)
		if err != nil || len(overlay) == 0 {
			continue
		}
		overlays = append(overlays, overlay)
	}
	if len(overlays) == 0 {
		return catalog
	}

	merged := make(modelsdev.Catalog, len(catalog))
	for id, entry := range catalog {
		merged[id] = entry
	}
	for _, overlay := range overlays {
		for id, entry := range overlay {
			merged[id] = mergeProvider(merged[id], entry)
		}
	}
	return merged
}

// mergeProvider layers an overlay entry onto a catalog entry, field by field:
// a field the overlay does not set keeps the catalog's value, and models are
// merged by id rather than replaced wholesale.
func mergeProvider(base, overlay modelsdev.Provider) modelsdev.Provider {
	out := base
	if out.ID == "" {
		out.ID = overlay.ID
	}
	if overlay.Name != "" {
		out.Name = overlay.Name
	}
	if overlay.NPM != "" {
		out.NPM = overlay.NPM
	}
	if overlay.API != "" {
		out.API = overlay.API
	}
	if len(overlay.Env) > 0 {
		out.Env = overlay.Env
	}
	if len(overlay.Models) > 0 {
		models := make(map[string]modelsdev.Model, len(out.Models)+len(overlay.Models))
		for id, model := range out.Models {
			models[id] = model
		}
		for id, model := range overlay.Models {
			models[id] = mergeModel(models[id], model)
		}
		out.Models = models
	}
	// A whitelist means the account is entitled to exactly this set — not
	// "these plus whatever the public catalog otherwise lists". Without
	// pruning, the picker offered models the account's own device-flow
	// token can't actually use (rejected by the inference gateway, not by
	// this port), indistinguishable from a real outage until tried.
	if len(overlay.Whitelist) > 0 {
		allowed := make(map[string]bool, len(overlay.Whitelist))
		for _, id := range overlay.Whitelist {
			allowed[id] = true
		}
		pruned := make(map[string]modelsdev.Model, len(overlay.Whitelist))
		for id, model := range out.Models {
			if allowed[id] {
				pruned[id] = model
			}
		}
		out.Models = pruned
	}
	return out
}

func mergeModel(base, overlay modelsdev.Model) modelsdev.Model {
	out := base
	if out.ID == "" {
		out.ID = overlay.ID
	}
	if overlay.Name != "" {
		out.Name = overlay.Name
	}
	if overlay.Family != "" {
		out.Family = overlay.Family
	}
	if overlay.ReleaseDate != "" {
		out.ReleaseDate = overlay.ReleaseDate
	}
	if overlay.Status != "" {
		out.Status = overlay.Status
	}
	if overlay.Cost != nil {
		out.Cost = overlay.Cost
	}
	if overlay.Limit.Context != 0 || overlay.Limit.Output != 0 {
		out.Limit = overlay.Limit
	}
	if overlay.ToolCall {
		out.ToolCall = true
	}
	if overlay.Provider != nil {
		out.Provider = overlay.Provider
	}
	return out
}

// PublishesModels reports whether a provider has its own model list, so
// callers can skip the cost of resolving it when it does not.
func PublishesModels(id string, entry modelsdev.Provider) bool {
	for _, t := range transformsFor(id, entry) {
		if _, ok := t.(ModelSource); ok {
			return true
		}
	}
	return false
}

// LiveModels returns the provider's own model list when it publishes one,
// falling back to the catalog's. Callers that render a model picker want this;
// callers building a client do not, which is why it is not part of Resolve.
func (r *Resolved) LiveModels(ctx context.Context) map[string]modelsdev.Model {
	for _, t := range transformsFor(r.ID, r.Entry) {
		source, ok := t.(ModelSource)
		if !ok {
			continue
		}
		models, err := source.FetchModels(ctx, r)
		if err != nil || len(models) == 0 {
			// The catalog list is a usable fallback, and is what the TS plugins
			// fall back to as well.
			continue
		}
		return models
	}
	return r.Models
}

// TransformedProviders lists the provider ids that have a dedicated transform,
// for `gocode debug providers` and tests. Ids come from idMatcher-based
// transforms only, since a transform matching on npm package covers an
// open-ended set.
func TransformedProviders() []string {
	var out []string
	for _, t := range registry {
		if m, ok := t.(interface{ ProviderIDs() []string }); ok {
			out = append(out, m.ProviderIDs()...)
		}
	}
	sort.Strings(out)
	return out
}

// byID is the common case: a transform that matches a fixed set of provider
// ids. Embed it to get Matches and ProviderIDs.
type byID []string

func (b byID) Matches(id string, _ modelsdev.Provider) bool {
	for _, want := range b {
		if id == want {
			return true
		}
	}
	return false
}

func (b byID) ProviderIDs() []string { return []string(b) }
