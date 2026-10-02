package browser

import (
	"strings"
	"testing"
)

// Link must keep the URL visible as its own text — the fallback for
// terminals without OSC8 support — while wrapping it in the hyperlink
// escape terminals with support turn into a ctrl/cmd-clickable link.
func TestLinkWrapsURLInOSC8(t *testing.T) {
	url := "https://example.com/oauth/authorize?client_id=abc"
	got := Link(url)
	if !strings.HasPrefix(got, "\x1b]8;;"+url+"\a") {
		t.Fatalf("link does not open an OSC8 hyperlink targeting the URL: %q", got)
	}
	if !strings.HasSuffix(got, url+"\x1b]8;;\a") {
		t.Fatalf("link does not close the hyperlink after the plain URL: %q", got)
	}
	// The visible text between the escapes is the URL itself, so a terminal
	// that ignores OSC8 still shows something copyable.
	if strings.Count(got, url) != 2 {
		t.Fatalf("link text is not the URL itself: %q", got)
	}
}

func TestLinkEmpty(t *testing.T) {
	if got := Link(""); got != "" {
		t.Fatalf("Link(\"\") = %q, want empty", got)
	}
}
