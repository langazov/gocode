package server

import (
	"os"
	"strings"

	"github.com/langazov/gocode-go/internal/auth"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// Provider availability, ported from `Provider.list()` in
// packages/opencode/src/provider/provider.ts.
//
// The distinction that matters there is between the *database* and the *list*.
// The models.dev catalog is the database — every provider that exists, several
// hundred of them. The list starts empty and a provider is added to it only by
// a `mergeProvider` call, and those happen for exactly four reasons:
//
//	// load env
//	const apiKey = provider.env.map((item) => envs[item]).find(Boolean)
//	if (!apiKey) continue
//	mergeProvider(providerID, { source: "env", … })
//
//	// load apikeys  (auth.json)
//	if (provider.type === "api") mergeProvider(providerID, { source: "api", … })
//
//	// load config
//	mergeProvider(providerID, { source: "config", … })
//
//	// custom loaders that opt into autoload
//
// This port was listing the database: `GET /api/model` walked the whole
// catalog, so the model dialog offered thousands of models from providers the
// user has no credentials for and cannot use.

// providerAvailability answers "does this provider have a usable
// configuration" for one request, resolving auth.json once rather than per
// provider — the catalog has hundreds of entries.
type providerAvailability struct {
	auths  map[string]auth.Info
	config *config.Config
}

func newProviderAvailability(cfg *config.Config) providerAvailability {
	// A missing or unreadable auth.json just means no stored credentials.
	auths, err := auth.All()
	if err != nil {
		auths = nil
	}
	return providerAvailability{auths: auths, config: cfg}
}

// allowed applies the two config gates that run regardless of credentials:
// `disabled_providers`, and `enabled_providers` as an allowlist when set.
func (p providerAvailability) allowed(providerID string) bool {
	if p.config == nil {
		return true
	}
	if p.config.ProviderDisabled(providerID) {
		return false
	}
	if len(p.config.EnabledProviders) > 0 && !p.config.ProviderEnabled(providerID) {
		return false
	}
	return true
}

// available reports whether the provider has a credential source, mirroring
// the four mergeProvider callers. entry is its catalog record, which supplies
// the environment variable names to look for.
func (p providerAvailability) available(providerID string, entry modelsdev.Provider) bool {
	if !p.allowed(providerID) {
		return false
	}
	// source: "config" — an explicitly declared provider is listed whether or
	// not a key resolves, because the config may carry a baseURL to a gateway
	// that needs none.
	if p.config != nil {
		if _, ok := p.config.Provider[providerID]; ok {
			return true
		}
	}
	// source: "env"
	for _, name := range p.envNames(providerID, entry) {
		if os.Getenv(name) != "" {
			return true
		}
	}
	// source: "api" — anything stored by `gocode auth login`, including the
	// oauth entries the console flow writes.
	if _, ok := p.auths[providerID]; ok {
		return true
	}
	return false
}

// envNames is the catalog's own env list, falling back to the conventional
// {PROVIDER}_API_KEY that provider.ResolveAPIKey also assumes — so a provider
// whose catalog entry names no variable is still reachable by the convention
// this port already honours at connect time.
func (p providerAvailability) envNames(providerID string, entry modelsdev.Provider) []string {
	if len(entry.Env) > 0 {
		return entry.Env
	}
	return []string{strings.ToUpper(providerID) + "_API_KEY"}
}
