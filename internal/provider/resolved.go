package provider

import (
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/modelsdev"
)

// Wire protocols this port speaks. Every provider resolves to one of them;
// there is no per-provider SDK the way TypeScript has an @ai-sdk package per
// vendor, so a provider whose API is none of these needs Go code rather than
// a catalog entry. See Protocol.
const (
	ProtocolAnthropic = "anthropic"
	ProtocolGemini    = "gemini"
	ProtocolOpenAI    = "openai"
)

// Resolved is a provider's fully-materialized configuration, built from the
// models.dev catalog plus user config and then handed to each matching
// Transform for adjustment. It is the Go analogue of ProviderV2.Info in
// packages/schema/src/provider.ts.
//
// The split between this and the client is deliberate: everything a provider
// needs to say about itself is data on this struct, so a new provider is a
// Transform that fills fields in, never a new branch in the client-building
// switch.
type Resolved struct {
	ID       string
	Name     string
	Protocol string
	BaseURL  string
	APIKey   string

	// Options are the request-level hooks the stream client applies.
	Options llm.Options

	// Entry is the raw catalog record, and Config the user's provider block
	// (nil when they have none). Transforms read both; neither is consulted
	// after transforms have run.
	Entry  modelsdev.Provider
	Config *config.Provider

	// Models is the catalog's model set, which a transform may replace when
	// the provider publishes its own list (github-copilot does).
	Models map[string]modelsdev.Model

	// keyErr records why credential resolution failed, deferred until Client
	// so that a transform has the chance to supply a credential or to make one
	// unnecessary. Reported only if the provider still has no way to
	// authenticate once transforms have run.
	keyErr error
}

// Header sets a request header, allocating the map on first use.
func (r *Resolved) Header(key, value string) {
	if r.Options.Headers == nil {
		r.Options.Headers = map[string]string{}
	}
	r.Options.Headers[key] = value
}

// BodyField sets a request body field, allocating the map on first use.
func (r *Resolved) BodyField(key string, value any) {
	if r.Options.Body == nil {
		r.Options.Body = map[string]any{}
	}
	r.Options.Body[key] = value
}

// Option returns a provider option from the user's config, checking the typed
// fields first and then the untyped passthrough map that ProviderOptions keeps
// for provider-specific keys.
func (r *Resolved) Option(key string) string {
	if r.Config == nil {
		return ""
	}
	switch key {
	case "apiKey":
		return r.Config.Options.APIKey
	case "baseURL":
		return r.Config.Options.BaseURL
	case "enterpriseUrl":
		return r.Config.Options.EnterpriseURL
	}
	if value, ok := r.Config.Options.Extra[key].(string); ok {
		return value
	}
	return ""
}
