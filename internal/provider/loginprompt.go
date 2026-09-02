package provider

import (
	"context"
	"fmt"
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
// stdout for the CLI.
func promptLogin(ctx context.Context, prompt LoginPrompt) {
	if fn, ok := ctx.Value(loginPromptKey{}).(func(LoginPrompt)); ok && fn != nil {
		fn(prompt)
		return
	}
	fmt.Print("\n" + prompt.Message + "\n")
}
