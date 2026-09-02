package tui

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// a 1x1 PNG, so the test attaches a real file rather than an invented one.
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
}

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestPasteImagePathAttaches: pasting a path to an image attaches the file,
// which is what a file manager or screenshot tool puts on the clipboard.
func TestPasteImagePathAttaches(t *testing.T) {
	app := typingApp(t, 100, 40)
	path := writeTempFile(t, "shot.png", pngBytes)

	app.Update(tea.PasteMsg{Content: path})

	if got := app.input.Value(); !strings.Contains(got, "[Image 1]") {
		t.Errorf("prompt is %q, want an [Image 1] placeholder", got)
	}
	if strings.Contains(app.input.Value(), path) {
		t.Errorf("the path should be replaced by the placeholder, not inserted: %q", app.input.Value())
	}

	files := app.takeAttachments()
	if len(files) != 1 {
		t.Fatalf("recorded %d attachments, want 1", len(files))
	}
	if files[0].Mime != "image/png" {
		t.Errorf("mime = %q, want image/png", files[0].Mime)
	}
	if files[0].Name != "shot.png" {
		t.Errorf("name = %q, want shot.png", files[0].Name)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	if files[0].URI != want {
		t.Errorf("uri = %q, want the file's bytes as a data URI", files[0].URI)
	}
}

// TestPasteNumbersAttachmentsPerKind ports the per-kind counter: two images
// read as [Image 1] and [Image 2], with a PDF numbered separately.
func TestPasteNumbersAttachmentsPerKind(t *testing.T) {
	app := typingApp(t, 100, 40)
	dir := t.TempDir()
	for _, name := range []string{"a.png", "b.png"} {
		path := filepath.Join(dir, name)
		os.WriteFile(path, pngBytes, 0o644)
		app.Update(tea.PasteMsg{Content: path})
	}
	pdf := filepath.Join(dir, "doc.pdf")
	os.WriteFile(pdf, []byte("%PDF-1.4"), 0o644)
	app.Update(tea.PasteMsg{Content: pdf})

	value := app.input.Value()
	for _, want := range []string{"[Image 1]", "[Image 2]", "[PDF 1]"} {
		if !strings.Contains(value, want) {
			t.Errorf("prompt %q is missing %q", value, want)
		}
	}
	if got := len(app.takeAttachments()); got != 3 {
		t.Errorf("recorded %d attachments, want 3", got)
	}
}

// TestPasteSVGGoesInAsText: SVG is markup, and the original inlines it so the
// model can read it without vision.
func TestPasteSVGGoesInAsText(t *testing.T) {
	app := typingApp(t, 100, 40)
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="1"/></svg>`
	path := writeTempFile(t, "icon.svg", []byte(svg))

	app.Update(tea.PasteMsg{Content: path})

	if got := app.input.Value(); !strings.Contains(got, "[SVG: icon.svg]") {
		t.Errorf("prompt is %q, want an SVG placeholder", got)
	}
	if got := len(app.attachments); got != 0 {
		t.Errorf("SVG should be text, not an attachment; got %d attachments", got)
	}
	expanded := expandPastes(app.input.Value(), app.takePastes())
	if !strings.Contains(expanded, "<circle") {
		t.Errorf("submitted text should carry the SVG markup: %q", expanded)
	}
}

// TestPasteUnknownFileTypeIsText: a path this port has no mime for is just
// text, and must not be swallowed.
func TestPasteUnknownFileTypeIsText(t *testing.T) {
	app := typingApp(t, 100, 40)
	path := writeTempFile(t, "notes.txt", []byte("hello"))

	app.Update(tea.PasteMsg{Content: path})

	if got := app.input.Value(); got != path {
		t.Errorf("prompt is %q, want the path inserted as text", got)
	}
	if len(app.attachments) != 0 {
		t.Error("a .txt path must not attach")
	}
}

// TestPasteMissingFileIsText: a path that looks like an image but does not
// exist is text, not a silent failure.
func TestPasteMissingFileIsText(t *testing.T) {
	app := typingApp(t, 100, 40)
	path := filepath.Join(t.TempDir(), "gone.png")

	app.Update(tea.PasteMsg{Content: path})

	if got := app.input.Value(); got != path {
		t.Errorf("prompt is %q, want the path as text", got)
	}
}

func TestPastedFilepath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix path shapes")
	}
	cases := map[string]string{
		"/tmp/a.png":            "/tmp/a.png",
		`"/tmp/a.png"`:          "/tmp/a.png",
		"'/tmp/a.png'":          "/tmp/a.png",
		`/tmp/with\ space.png`:  "/tmp/with space.png",
		"file:///tmp/a.png":     "/tmp/a.png",
		"file:///tmp/a%20b.png": "/tmp/a b.png",
		"  /tmp/a.png  ":        "/tmp/a.png",
	}
	for input, want := range cases {
		if got := pastedFilepath(input); got != want {
			t.Errorf("pastedFilepath(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestPasteCommandSelection mirrors the write side's test: the tool chosen is
// a pure function of the platform.
func TestPasteCommandSelection(t *testing.T) {
	all := func(string) bool { return true }
	none := func(string) bool { return false }

	if got := pasteCommand("darwin", false, all); got[0] != "pbpaste" {
		t.Errorf("darwin = %v, want pbpaste", got)
	}
	if got := pasteCommand("linux", true, all); got[0] != "wl-paste" {
		t.Errorf("wayland = %v, want wl-paste", got)
	}
	if got := pasteCommand("linux", false, all); got[0] != "xclip" {
		t.Errorf("x11 = %v, want xclip first", got)
	}
	if got := pasteCommand("linux", false, func(n string) bool { return n == "xsel" }); got[0] != "xsel" {
		t.Errorf("xsel fallback = %v", got)
	}
	if got := pasteCommand("linux", false, none); got != nil {
		t.Errorf("no tools = %v, want nil", got)
	}
	if got := pasteCommand("windows", false, all); got[0] != "powershell.exe" {
		t.Errorf("windows = %v", got)
	}
}
