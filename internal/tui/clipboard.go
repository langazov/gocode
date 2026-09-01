package tui

// Clipboard writes, ported from packages/tui/src/clipboard.ts.
//
// Upstream's `write()` does two things, and this port only ever did the first:
//
//	export async function write(text: string) {
//	  writeOsc52(text)
//	  const method = await getCopyMethod()
//	  await method(text)
//	}
//
// OSC52 asks the *terminal* to set the clipboard, and plenty of terminals
// either do not implement it (macOS Terminal.app) or gate it behind a setting
// (iTerm2, tmux's `set-clipboard`). Whenever it is refused the escape is
// silently dropped — there is no reply to wait for — so a port that relies on
// it alone reports "Copied to clipboard" and copies nothing. The native
// command is what makes the copy actually land; OSC52 is what makes it work
// over SSH, where no native clipboard exists.

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// copyCommand is clipboard.ts's copyCommand, kept as a pure function of the
// platform so its selection is testable without the tools installed.
//
// Order matters and is upstream's: on Linux, Wayland's wl-copy before X11's
// xclip before xsel. nil means no native tool is available and OSC52 is the
// only route left.
func copyCommand(goos string, wayland bool, has func(name string) bool) []string {
	switch {
	case goos == "darwin" && has("osascript"):
		return []string{"osascript"}
	case goos == "linux" && wayland && has("wl-copy"):
		return []string{"wl-copy"}
	case goos == "linux" && has("xclip"):
		return []string{"xclip", "-selection", "clipboard"}
	case goos == "linux" && has("xsel"):
		return []string{"xsel", "--clipboard", "--input"}
	case goos == "windows" && has("powershell.exe"):
		return []string{
			"powershell.exe", "-NonInteractive", "-NoProfile", "-Command",
			"[Console]::InputEncoding = [System.Text.Encoding]::UTF8; " +
				"Set-Clipboard -Value ([Console]::In.ReadToEnd())",
		}
	}
	return nil
}

// appleScriptClipboard is the osascript form upstream uses on darwin. The tool
// takes the text as part of the script rather than on stdin, so backslashes
// and quotes have to be escaped into the AppleScript string literal.
func appleScriptClipboard(text string) []string {
	escaped := strings.ReplaceAll(text, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return []string{"osascript", "-e", `set the clipboard to "` + escaped + `"`}
}

// clipboardTimeout bounds a hung helper. Nothing here is worth blocking a
// command goroutine on indefinitely.
const clipboardTimeout = 5 * time.Second

// writeSystemClipboard runs the platform's clipboard tool. It reports whether
// a tool was found and, if one was, whether it succeeded — so a caller can
// tell "no native clipboard here, OSC52 is all we have" (fine, and the normal
// case over SSH) from "the native clipboard is broken" (worth saying).
func writeSystemClipboard(text string) (attempted bool, err error) {
	argv := copyCommand(runtime.GOOS, waylandSession(), func(name string) bool {
		_, err := exec.LookPath(name)
		return err == nil
	})
	if argv == nil {
		return false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()

	if argv[0] == "osascript" {
		argv = appleScriptClipboard(text)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		return true, cmd.Run()
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader(text)
	return true, cmd.Run()
}

// waylandSession mirrors upstream's `Boolean(process.env.WAYLAND_DISPLAY)`.
func waylandSession() bool { return os.Getenv("WAYLAND_DISPLAY") != "" }

// copyToClipboard is the port of clipboard.ts's write(): OSC52 for the
// terminal (through Bubble Tea's renderer rather than a raw stdout write —
// see mouse.go's copySelectionCmd) *and* the native clipboard tool, plus the
// toast the caller wants. notice is the success message.
func copyToClipboard(a *App, text, notice string) tea.Cmd {
	return tea.Batch(
		tea.SetClipboard(text),
		func() tea.Msg {
			attempted, err := writeSystemClipboard(text)
			if attempted && err != nil {
				return statusMsg{text: "copy failed: " + err.Error()}
			}
			return nil
		},
		a.showToast(notice, false),
	)
}
