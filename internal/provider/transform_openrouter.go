package provider

import (
	"context"
)

func init() {
	Register(openrouterTransform{byID{"openrouter"}})
}

// OpenRouter app-attribution identifiers (openrouter.ai/docs/app-attribution).
//
// HTTP-Referer is the required one: without it OpenRouter creates no app page
// at all, and the usage never shows in the public rankings or on model pages.
// The URL becomes the app's identity — openrouter.ai/apps?url=<referer> — so
// it is the product's own domain, not an env-derived one, to keep every
// install attributing to the same app.
//
// X-OpenRouter-Title is the display name in those rankings (X-Title is the
// legacy spelling OpenRouter still accepts), and X-OpenRouter-Categories
// files the app under a marketplace category — "cli-agent" is their term for
// a terminal-based coding assistant, which is what gocode is.
const (
	openRouterReferer    = "https://gocoder.org"
	openRouterTitle      = "GoCode"
	openRouterCategories = "cli-agent"
)

// OpenRouterAttributionHeaders returns the app-attribution headers gocode
// sends with requests to openrouter.ai, so every client that talks to
// OpenRouter directly — the chat stream client through this transform, the
// embeddings client through internal/rag/embed — attributes its usage to the
// same app entry.
//
// It returns a fresh map each call: a caller mutating its result must not
// affect the next caller's headers.
func OpenRouterAttributionHeaders() map[string]string {
	return map[string]string{
		"HTTP-Referer":            openRouterReferer,
		"X-OpenRouter-Title":      openRouterTitle,
		"X-OpenRouter-Categories": openRouterCategories,
	}
}

// openrouterTransform opts gocode into OpenRouter's app attribution.
//
// OpenRouter is a plain catalog provider otherwise — endpoint and key env
// from models.dev, OpenAI wire protocol, no quirks — so this transform
// contributes nothing but the attribution headers, and only for the provider
// a user's own OpenRouter key resolves to. Traffic through gocoder.org's
// inference proxy (the gocoder provider) deliberately gets none: that proxy
// fronts OpenRouter with the site's own account, so how its usage is
// attributed is the proxy's decision, not this client's.
type openrouterTransform struct{ byID }

func (openrouterTransform) Apply(_ context.Context, r *Resolved) error {
	for key, value := range OpenRouterAttributionHeaders() {
		r.Header(key, value)
	}
	return nil
}
