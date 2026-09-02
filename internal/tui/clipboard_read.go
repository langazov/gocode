package tui

import (
	"context"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Clipboard reads, the counterpart to clipboard.go's writes.
//
// This ports the `prompt.paste` command in
// packages/tui/src/component/prompt/index.tsx, which reads the clipboard
// directly rather than waiting for a paste event.
//
// Bracketed paste covers the normal case: a terminal turns its own paste
// shortcut into a PasteMsg, which paste.go handles. This is for the terminals
// and multiplexers that do not, where the key reaches the application instead
// and would otherwise do nothing.

// pasteCommand mirrors clipboard.go's copyCommand, and is a pure function of
// the platform for the same reason: its choice is testable without the tools
// installed. Order matches the write side — Wayland, then X11's xclip, then
// xsel.
func pasteCommand(goos string, wayland bool, has func(name string) bool) []string {
	switch {
	case goos == "darwin" && has("pbpaste"):
		return []string{"pbpaste"}
	case goos == "linux" && wayland && has("wl-paste"):
		return []string{"wl-paste", "--no-newline"}
	case goos == "linux" && has("xclip"):
		return []string{"xclip", "-selection", "clipboard", "-o"}
	case goos == "linux" && has("xsel"):
		return []string{"xsel", "--clipboard", "--output"}
	case goos == "windows" && has("powershell.exe"):
		return []string{
			"powershell.exe", "-NonInteractive", "-NoProfile", "-Command",
			"[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; Get-Clipboard -Raw",
		}
	}
	return nil
}

// readSystemClipboard returns the clipboard's text. attempted reports whether
// a tool was found at all, so the caller can tell "no clipboard tool here"
// from "the tool failed".
func readSystemClipboard() (text string, attempted bool, err error) {
	argv := pasteCommand(runtime.GOOS, waylandSession(), func(name string) bool {
		_, err := exec.LookPath(name)
		return err == nil
	})
	if argv == nil {
		return "", false, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).Output()
	if err != nil {
		return "", true, err
	}
	text = string(out)
	if runtime.GOOS == "windows" {
		// Get-Clipboard -Raw still ends the value with the shell's newline.
		text = strings.TrimSuffix(text, "\r\n")
	}
	return text, true, nil
}

// pasteFromClipboard handles the explicit paste key, feeding what it finds
// through the same path a bracketed paste takes so the size rules, attachment
// detection and line-ending normalization all apply identically.
func (a *App) pasteFromClipboard() tea.Cmd {
	text, attempted, err := readSystemClipboard()
	switch {
	case !attempted:
		return a.showToast("No clipboard tool available for reading", true)
	case err != nil:
		return a.showToast("Paste failed: "+err.Error(), true)
	case text == "":
		return nil
	}
	return a.handlePaste(tea.PasteMsg{Content: text})
}
