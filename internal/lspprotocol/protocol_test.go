package lspprotocol

import (
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestURIRoundTrip pins the property every caller depends on: a path handed to
// URIFromPath comes back unchanged from PathFromURI. The paths are built with
// filepath.Join so this exercises the host's real separator — on Windows that
// is the drive-letter case, which is where hand-rolled "file://" + path
// concatenation produces a URI that url.Parse rejects outright.
func TestURIRoundTrip(t *testing.T) {
	root := filepath.Join(tempRoot(), "workspace", "TestX", "001")
	for _, rel := range []string{
		"doc.md",
		filepath.Join("notes", "guide.md"),
		"name with spaces.md",
		"punctuation#hash.md",
	} {
		path := filepath.Join(root, rel)
		uri := URIFromPath(path)
		if !strings.HasPrefix(uri, "file:///") {
			t.Errorf("URIFromPath(%q) = %q, want a file:/// URI", path, uri)
		}
		if _, err := url.Parse(uri); err != nil {
			t.Errorf("URIFromPath(%q) = %q, which url.Parse rejects: %v", path, uri, err)
		}
		got, ok := PathFromURI(uri)
		if !ok {
			t.Errorf("PathFromURI(%q) returned ok=false", uri)
			continue
		}
		if got != path {
			t.Errorf("round trip: %q -> %q -> %q", path, uri, got)
		}
	}
}

// tempRoot is a platform-shaped absolute directory, so the Windows branch of
// URIFromPath is exercised on Windows and the POSIX one everywhere else.
func tempRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\Users\runner\AppData\Local\Temp`
	}
	return "/tmp"
}

func TestPathFromURIRejectsNonFileURI(t *testing.T) {
	for _, uri := range []string{"http://example.com/x.md", "untitled:Untitled-1", ""} {
		if got, ok := PathFromURI(uri); ok {
			t.Errorf("PathFromURI(%q) = %q, true; want ok=false", uri, got)
		}
	}
}
