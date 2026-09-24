package llm

import "github.com/langazov/gocode-go/internal/identifier"

// ToolCallID returns id, or a fresh unique ID when the provider streamed a
// tool call without one. Several openai-compatible backends and gateways
// omit the id on a real, named call; dropping such a call left the step with
// nothing to dispatch, so the turn settled as finished right after the model
// announced what it was about to do. Synthesizing one keeps the call
// runnable and its result paired on replay. The ID is unique across the
// session, not just the response — a positional one would repeat per step.
func ToolCallID(id string) string {
	if id != "" {
		return id
	}
	return "call_" + identifier.Ascending()
}
