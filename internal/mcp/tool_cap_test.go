package mcp

import (
	"strings"
	"testing"
)

// An MCP server is foreign code with no output discipline of its own. The
// adapter is where its results enter gocode: a megabyte payload must never
// reach the session, because it is persisted, replayed to the model, and
// re-rendered on every repaint. The adapter rejects it with an error naming
// the size and the limit so the model can narrow the call.

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{3 * 1024 * 1024, "3.0 MiB"},
	}
	for _, c := range cases {
		if got := formatBytes(c.n); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestToolOutputTooLargeErrorMessage(t *testing.T) {
	err := &toolOutputTooLargeError{tool: "kubernetes-describe-pod", bytes: 5 * 1024 * 1024}
	msg := err.Error()
	// The message is the model's tool result: it must name the tool, quote
	// both sizes, and say what to do about it.
	for _, want := range []string{
		"kubernetes-describe-pod",
		"5.0 MiB",
		"512.0 KiB",
		"too large",
		"Narrow the call",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}
