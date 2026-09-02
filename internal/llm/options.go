package llm

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Options carries the per-provider request customization that the provider
// transform layer computes and a stream client applies. It is the Go
// equivalent of the `request: {headers, body}` block on ProviderV2.Info in
// packages/schema/src/provider.ts, plus the two hooks TypeScript gets for
// free from an @ai-sdk package and this port has to model explicitly:
// request signing and model-id remapping.
//
// The zero value is inert, so a client that is handed no options behaves
// exactly as it did before options existed.
type Options struct {
	// Headers are merged into every request. They are applied after the
	// client's own headers, so a provider can override Content-Type or an
	// API version if it has to.
	Headers map[string]string

	// Body fields are merged into the top level of every request payload,
	// overwriting keys the client itself produced. Used for provider-required
	// parameters that are not part of the base wire format.
	Body map[string]any

	// ModelID rewrites the model identifier just before the request is built.
	// Bedrock needs this for its region-prefixed inference profile IDs.
	ModelID func(string) string

	// Sign authenticates the request. When set it *replaces* the client's
	// default bearer-token header, because a provider that signs (AWS SigV4)
	// or uses a non-bearer scheme (Azure's api-key) must not also send an
	// Authorization header derived from the raw key. It receives the marshaled
	// payload because signature schemes hash the body.
	Sign func(req *http.Request, payload []byte) error

	// Endpoint overrides the request URL, receiving the client's resolved base
	// URL and the already-remapped model id. Providers that address a model
	// through the path rather than the body need it: Azure routes per
	// deployment, Vertex per project/location/publisher.
	Endpoint func(base, model string) string

	// Transport wraps the HTTP round tripper, and is this port's equivalent of
	// the per-provider `fetch` override the TypeScript plugins install (see
	// cortexFetch in plugin/provider/snowflake-cortex.ts). It is the hook for
	// quirks that are neither a header nor an added body field: rewriting or
	// removing a request field, or repairing a non-conforming response before
	// the client's parser sees it.
	Transport func(http.RoundTripper) http.RoundTripper
}

// HTTPClient applies the transport wrapper to a client, returning it
// unchanged when no wrapper is configured.
func (o Options) HTTPClient(client *http.Client) *http.Client {
	if o.Transport == nil || client == nil {
		return client
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	wrapped := *client
	wrapped.Transport = o.Transport(base)
	return &wrapped
}

// URL returns the endpoint for a request, falling back to the client's own
// default when no override is configured.
func (o Options) URL(base, model, fallback string) string {
	if o.Endpoint == nil {
		return fallback
	}
	return o.Endpoint(base, model)
}

// ApplyHeaders merges the configured headers into a request.
func (o Options) ApplyHeaders(req *http.Request) {
	for key, value := range o.Headers {
		req.Header.Set(key, value)
	}
}

// Authenticate applies the signing hook, reporting whether it handled auth.
// A false return means the caller should fall back to its default scheme.
func (o Options) Authenticate(req *http.Request, payload []byte) (bool, error) {
	if o.Sign == nil {
		return false, nil
	}
	if err := o.Sign(req, payload); err != nil {
		return true, fmt.Errorf("llm: signing request: %w", err)
	}
	return true, nil
}

// Model applies the model-id remapping, if any.
func (o Options) Model(id string) string {
	if o.ModelID == nil {
		return id
	}
	return o.ModelID(id)
}

// MergeBody splices the extra body fields into an already-marshaled payload.
// It round-trips through a generic map rather than being threaded into every
// client's typed request struct, so that adding a provider quirk never means
// touching the wire types of a protocol it does not apply to. With no extra
// fields the payload is returned untouched, so the common path pays nothing.
func (o Options) MergeBody(payload []byte) ([]byte, error) {
	if len(o.Body) == 0 {
		return payload, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(payload, &merged); err != nil {
		return nil, fmt.Errorf("llm: merging request body: %w", err)
	}
	for key, value := range o.Body {
		merged[key] = value
	}
	return json.Marshal(merged)
}
