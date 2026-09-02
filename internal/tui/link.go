package tui

import (
	"os/exec"
	"regexp"
	"runtime"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// urlPattern finds a bare http(s) URL in plain text, used to linkify a
// toast message the way dialog-retry-action.tsx / dialog-provider.tsx show
// an explicit action URL next to a message.
var urlPattern = regexp.MustCompile(`https?://[^\s)\]]+`)

// This file ports ui/link.tsx: a clickable hyperlink. TS renders a <text>
// with an onMouseUp handler that shells out to the `open` npm package; this
// port renders the same text wrapped in an OSC8 hyperlink escape (so
// terminals that support it — iTerm2, WezTerm, kitty, Windows Terminal, ...
// — make it natively ctrl/cmd-clickable) and separately reproduces "clicking
// anywhere on the link opens it" by recording where each rendered link
// landed on screen (linkHit) for handleClick (mouse.go) to consult, since
// Bubble Tea has no per-widget mouse-event primitive like opentui's onMouseUp.

// renderLink ports the Link component's render: styled text (fg/underline,
// matching TS's fg prop) wrapped in an OSC8 hyperlink escape. text defaults
// to href, matching `props.children ?? props.href`.
func renderLink(href, text string, style lipgloss.Style) string {
	if text == "" {
		text = href
	}
	rendered := style.Underline(true).Render(text)
	return ansi.SetHyperlink(href) + rendered + ansi.ResetHyperlink()
}

// linkHit is a clickable href region on the current frame, in absolute
// screen cells — recorded by whatever view code renders a link (currently
// just the toast panel, see feature.go) using the same plain-text layout
// math that produced what's on screen, the same technique dialogs.go's
// overlayHits uses for dialog rows/actions.
type linkHit struct {
	row              int
	colStart, colEnd int
	href             string
}

// linkAt returns the href under (row, col), or "" if none — handleClick's
// equivalent of opentui dispatching onMouseUp to the Link under the cursor.
func (a *App) linkAt(row, col int) string {
	for _, hit := range a.linkHits {
		if hit.row == row && col >= hit.colStart && col < hit.colEnd {
			return hit.href
		}
	}
	return ""
}

// openURL opens href with the OS's default handler, the Go equivalent of
// the `open` npm package Link.tsx's onMouseUp calls. Errors are intentionally
// swallowed by callers, matching `open(props.href).catch(() => {})`. A var,
// not a func, so tests can substitute it rather than actually launching a
// browser.
var openURL = func(href string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", href).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", href).Start()
	default:
		return exec.Command("xdg-open", href).Start()
	}
}
