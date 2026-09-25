// Package browser opens URLs with the OS's default handler and wraps plain
// text in the OSC8 hyperlink escape so a terminal makes it clickable.
//
// Both halves existed before, triplicated: internal/mcp's openBrowser,
// cmd_web.go's openBrowser and tui/link.go's openURL were the same three
// switches on runtime.GOOS, and tui/link.go's renderLink already wrapped text
// in OSC8 for terminals that support it. This package is those two ideas with
// one home, so a login flow printing "open this URL" can do both without
// importing the TUI.
package browser

import (
	"os/exec"
	"runtime"

	"github.com/charmbracelet/x/ansi"
)

// Open launches the OS's default handler for url. Best effort, matching the
// `open` npm package's `.catch(() => {})` in the TS original: an auth flow
// that cannot spawn a browser still has the URL printed next to it, so the
// failure is deliberately not fatal. Errors are returned for callers that
// want them (and for tests) but usually swallowed.
func Open(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// Link returns text wrapped in an OSC8 hyperlink escape targeting url, so
// terminals with hyperlink support (iTerm2, WezTerm, kitty, Windows Terminal,
// ...) make it ctrl/cmd-clickable natively. Terminals without support render
// the escape as nothing, leaving the plain text — which is why url is also
// the default text: the link never hides what it points at.
func Link(url string) string {
	if url == "" {
		return ""
	}
	return ansi.SetHyperlink(url) + url + ansi.ResetHyperlink()
}
