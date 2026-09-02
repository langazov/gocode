package tui

import (
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// copyCommand's selection, transcribed from clipboard.ts's own table.
func TestCopyCommandPicksThePlatformTool(t *testing.T) {
	all := func(string) bool { return true }
	none := func(string) bool { return false }
	only := func(names ...string) func(string) bool {
		return func(name string) bool { return slices.Contains(names, name) }
	}

	for _, tc := range []struct {
		name    string
		goos    string
		wayland bool
		has     func(string) bool
		want    []string
	}{
		{"darwin uses osascript", "darwin", false, all, []string{"osascript"}},
		{"wayland prefers wl-copy", "linux", true, all, []string{"wl-copy"}},
		{"x11 uses xclip", "linux", false, all, []string{"xclip", "-selection", "clipboard"}},
		{"xsel is the xclip fallback", "linux", false, only("xsel"), []string{"xsel", "--clipboard", "--input"}},
		{"wayland without wl-copy falls back to x11", "linux", true, only("xclip"),
			[]string{"xclip", "-selection", "clipboard"}},
		{"nothing installed has no native route", "linux", false, none, nil},
		{"darwin without osascript has none", "darwin", false, none, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := copyCommand(tc.goos, tc.wayland, tc.has)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("copyCommand = %v, want %v", got, tc.want)
			}
		})
	}

	if got := copyCommand("windows", false, all); len(got) == 0 || got[0] != "powershell.exe" {
		t.Fatalf("windows should use powershell, got %v", got)
	}
}

// osascript takes the text inside the script rather than on stdin, so the
// AppleScript string literal has to be escaped or any quote in a copied
// message breaks the script.
func TestAppleScriptEscapesQuotesAndBackslashes(t *testing.T) {
	argv := appleScriptClipboard(`say "hi" \ there`)
	script := argv[len(argv)-1]
	if !strings.Contains(script, `\"hi\"`) {
		t.Fatalf("quotes should be escaped: %s", script)
	}
	if !strings.Contains(script, `\\ there`) {
		t.Fatalf("backslashes should be escaped: %s", script)
	}
}

// The real thing: text handed to writeSystemClipboard comes back out of the
// system clipboard. This is what OSC52 alone never achieved on a terminal that
// does not implement it.
func TestClipboardRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the round trip needs a platform-specific paste command")
	}
	if _, err := exec.LookPath("pbpaste"); err != nil {
		t.Skip("pbpaste unavailable")
	}

	// Quotes, backslashes, newlines and tabs all have to survive the
	// AppleScript literal.
	want := "quote \" backslash \\ newline\nsecond line\ttab"
	attempted, err := writeSystemClipboard(want)
	if !attempted {
		t.Skip("no native clipboard tool here")
	}
	if err != nil {
		t.Fatalf("writeSystemClipboard: %v", err)
	}

	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != want {
		t.Fatalf("clipboard holds %q, want %q", string(out), want)
	}
}
