package provider

import "context"

func init() {
	Register(anthropicTransform{byID{"anthropic"}})
	Register(googleTransform{byID{"google", "gemini", "google-vertex"}})
}

// anthropicTransform ports packages/core/src/plugin/provider/anthropic.ts.
//
// It exists to pin the wire protocol by provider id rather than trusting the
// catalog's npm package. The catalog is right about anthropic today, but the
// id is the stronger signal: a mis-set `provider.anthropic.api` in a user's
// config must not send Anthropic traffic through the OpenAI wire format.
type anthropicTransform struct{ byID }

func (anthropicTransform) Apply(_ context.Context, r *Resolved) error {
	r.Protocol = ProtocolAnthropic
	return nil
}

// googleTransform ports packages/core/src/plugin/provider/google.ts, pinning
// the Gemini protocol for the same reason. It covers google-vertex too, which
// shares the wire format; vertex's own transform layers the endpoint and
// credential exchange on top rather than restating the protocol.
type googleTransform struct{ byID }

func (googleTransform) Apply(_ context.Context, r *Resolved) error {
	r.Protocol = ProtocolGemini
	return nil
}

var (
	_ Transform = anthropicTransform{}
	_ Transform = googleTransform{}
)
