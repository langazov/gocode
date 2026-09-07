package tui

import "testing"

// A step cut off at max_tokens has to say so. Each protocol spells the reason
// differently, and none of them is an error the message carries — without
// this the turn just ends, which is what a truncated thinking step looked
// like from the interface.
func TestTruncatedByOutputLimitCoversEveryProtocol(t *testing.T) {
	for _, finish := range []string{"length", "max_tokens", "max-tokens"} {
		if !truncatedByOutputLimit(finish) {
			t.Errorf("%q should read as an output-limit truncation", finish)
		}
	}
	for _, finish := range []string{"", "stop", "tool-calls", "unknown"} {
		if truncatedByOutputLimit(finish) {
			t.Errorf("%q is not an output-limit truncation", finish)
		}
	}
}
