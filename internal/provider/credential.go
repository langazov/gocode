package provider

import (
	"context"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// Refresher is the optional half of the Transform contract for providers whose
// OAuth credentials expire. It ports the `refresh` member of an
// IntegrationOAuthMethodRegistration (packages/core/src/integration.ts:81).
type Refresher interface {
	// RefreshCredential exchanges a stored credential for a fresh one. The
	// result is persisted by the caller.
	RefreshCredential(ctx context.Context, info auth.Info) (auth.Info, error)
}

// ResolveCredential returns the stored credential for a provider, renewing it first
// if it is close to expiring and the provider knows how to renew.
//
// This is the single read point for stored credentials. Before it existed, an
// expired OAuth token was handed to the provider unchanged and surfaced as a
// bare 401 with nothing pointing at the cause.
func ResolveCredential(ctx context.Context, providerID string, entry modelsdev.Provider) (*auth.Info, error) {
	info, err := auth.Get(providerID)
	if err != nil || info == nil {
		return info, err
	}
	if !auth.NeedsRefresh(*info) {
		return info, nil
	}

	for _, t := range transformsFor(providerID, entry) {
		refresher, ok := t.(Refresher)
		if !ok {
			continue
		}
		refreshed, err := refresher.RefreshCredential(ctx, *info)
		if err != nil {
			// Keep using the existing credential: it may still have minutes
			// left (the refresh window opens early), and a transient failure
			// at the token endpoint should not look like being logged out.
			global.LogBackground("provider: refreshing %s credentials failed: %v", providerID, err)
			return info, nil
		}
		if err := auth.Set(providerID, refreshed); err != nil {
			return nil, err
		}
		return &refreshed, nil
	}
	// No refresher: the credential is used as stored, matching TS's
	// `if (!implementation?.refresh) return credential.value`.
	return info, nil
}
