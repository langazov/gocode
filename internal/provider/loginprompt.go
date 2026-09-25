package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/browser"
)

// LoginPrompt is what a login flow needs to put in front of the user: a URL
// to open, and for a device flow the code to type there.
type LoginPrompt struct {
	URL  string
	Code string
	// Message is the human-readable instruction, already assembled.
	Message string
}

type loginPromptKey struct{}

// WithLoginPrompt returns a context that routes a login flow's user-facing
// instructions to fn instead of stdout.
//
// The flows are shared between the CLI, which prints, and the interface, which
// has to render the code inside a dialog and poll — printing there would paint
// over the frame. Carrying the sink on the context keeps the flows themselves
// free of any opinion about how they are being driven.
func WithLoginPrompt(ctx context.Context, fn func(LoginPrompt)) context.Context {
	return context.WithValue(ctx, loginPromptKey{}, fn)
}

// promptLogin delivers a prompt to the context's sink, falling back to
// stdout for the CLI. The fallback prints the URL as a clickable OSC8
// hyperlink (terminals without support show the plain URL) and opens it in
// the browser — a CLI login is a hands-on-keyboard moment, and the TS
// original's users expect the consent page to appear without a copy-paste
// round-trip. Both are best effort; the URL text is always right there.
func promptLogin(ctx context.Context, prompt LoginPrompt) {
	if fn, ok := ctx.Value(loginPromptKey{}).(func(LoginPrompt)); ok && fn != nil {
		fn(prompt)
		return
	}
	if prompt.URL != "" {
		_ = browser.Open(prompt.URL)
	}
	fmt.Print("\n" + strings.Replace(prompt.Message, prompt.URL, browser.Link(prompt.URL), 1) + "\n")
}
