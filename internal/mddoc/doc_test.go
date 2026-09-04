package mddoc

import (
	"strings"
	"testing"
)

func TestLineIndexBasics(t *testing.T) {
	text := "hello\nworld\n"
	ix := newLineIndex(text)
	if got := ix.lines(); got != 3 { // trailing empty line after final \n
		t.Fatalf("lines = %d, want 3", got)
	}
	if got := ix.positionToOffset(text, 1, 2); got != len("hello\nwo") {
		t.Fatalf("offset = %d", got)
	}
	line, ch := ix.offsetToPosition(text, len("hello\nwo"))
	if line != 1 || ch != 2 {
		t.Fatalf("line=%d ch=%d, want 1/2", line, ch)
	}
}

func TestPositionRoundTripASCII(t *testing.T) {
	text := "alpha\nbeta gamma\ndelta"
	ix := newLineIndex(text)
	for offset := 0; offset <= len(text); offset++ {
		line, ch := ix.offsetToPosition(text, offset)
		back := ix.positionToOffset(text, line, ch)
		if back != offset {
			t.Fatalf("offset %d -> (%d,%d) -> %d", offset, line, ch, back)
		}
	}
}

func TestPositionUTF16(t *testing.T) {
	// 👍 is one rune, 2 UTF-16 units, 4 bytes. : Th: e character column of
	// "world" must be 5, not 5-3=2 counted in bytes.
	text := "👍👍 world\n"
	ix := newLineIndex(text)
	if got := ix.positionToOffset(text, 0, 5); got != len("👍👍 ") {
		t.Fatalf("character=5 offset = %d, want %d", got, len("👍👍 "))
	}
	line, ch := ix.offsetToPosition(text, len("👍👍 w"))
	if line != 0 || ch != 6 {
		t.Fatalf("line=%d ch=%d, want 0/6", line, ch)
	}

	// A snowman is 3 bytes, 1 unit; an astral emoji is 4 bytes, 2 units.
	text2 := "⛄ and 🚀 tail\n"
	ix2 := newLineIndex(text2)
	if got := ix2.positionToOffset(text2, 0, 4); got != len("⛄ an") {
		t.Fatalf("mixed unit offset = %d, want %d", got, len("⛄ an"))
	}
	_, ch = ix2.offsetToPosition(text2, len("⛄ and 🚀 "))
	if ch != 9 {
		t.Fatalf("ch = %d, want 9", ch) // 1 + 4 ascii + 2 + 1 space + 1
	}
}

func TestPositionClamping(t *testing.T) {
	text := "short\n"
	ix := newLineIndex(text)
	// Past end of line clamps to line end.
	if got := ix.positionToOffset(text, 0, 100); got != len("short") {
		t.Fatalf("clamp to EOL = %d", got)
	}
	// Past end of document clamps to text end.
	if got := ix.positionToOffset(text, 50, 0); got != len(text) {
		t.Fatalf("clamp to EOF = %d", got)
	}
}

func TestParseHeadings(t *testing.T) {
	text := `# Top

## Second _idea_

body

### Deep
tail
`
	d := Parse(text)
	if len(d.Headings) != 3 {
		t.Fatalf("headings = %d, want 3", len(d.Headings))
	}
	h := d.Headings[1]
	if h.Level != 2 || h.Text != "Second _idea_" {
		t.Fatalf("h1 = %+v", h)
	}
	// GitHub slugs the rendered text, so the _emphasis_ underscores vanish.
	if h.Slug != "second-idea" {
		t.Fatalf("slug = %q", h.Slug)
	}
}

func TestParseSetextHeadings(t *testing.T) {
	d := Parse("Title\n=====\n\nbody\n\nSub\n---\n")
	if len(d.Headings) != 2 {
		t.Fatalf("headings = %d, want 2", len(d.Headings))
	}
	if d.Headings[0].Level != 1 || d.Headings[0].Text != "Title" {
		t.Fatalf("h0 = %+v", d.Headings[0])
	}
	if d.Headings[1].Level != 2 || d.Headings[1].Text != "Sub" {
		t.Fatalf("h1 = %+v", d.Headings[1])
	}
}

func TestHeadingSpansSetext(t *testing.T) {
	// The heading text is on the line above the underline; the selection
	// range must point at "Sub", not at the dashes.
	d := Parse("Sub\n---\nbody\n")
	h := d.Headings[0]
	span := d.LineSpanOf(h.StartByte, h.EndByte)
	if span.Start != 0 || span.End != 1 {
		t.Fatalf("span = %+v, want lines 0..1", span)
	}
}

func TestSlugDedupe(t *testing.T) {
	d := Parse("# Setup\n\n# Setup\n\n# Setup\n")
	want := []string{"setup", "setup-1", "setup-2"}
	for i, w := range want {
		if d.Headings[i].Slug != w {
			t.Fatalf("slug[%d] = %q, want %q", i, d.Headings[i].Slug, w)
		}
	}
}

func TestSlugGitHubExamples(t *testing.T) {
	cases := map[string]string{
		"Hello, World!":          "hello-world",
		"What's New?":            "whats-new",
		"API: Reference":         "api-reference",
		"  Spaces  Everywhere  ": "spaces--everywhere",
		"Code `blocks` too":      "code-blocks-too",
		"Ünïcödé":                "ünïcödé",
	}
	for in, want := range cases {
		if got := slugBase(plainText(in)); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseLinks(t *testing.T) {
	// A destination with spaces is not a link in CommonMark unless wrapped
	// in angle brackets, so this line parses to exactly one link per row.
	text := []byte("See [guide](guide.md#setup) and [wiki](Some-Page.md) and [top](#top) and [site](https://x.io).\n")
	d := Parse(string(text))
	if len(d.Links) != 4 {
		t.Fatalf("links = %d, want 4: %+v", len(d.Links), d.Links)
	}

	file := d.Links[0]
	if file.Kind != LinkFile || file.Destination != "guide.md#setup" {
		t.Fatalf("link0 = %+v", file)
	}
	// Destination byte range must point inside "(guide.md#setup)".
	if got := string(text[file.DestStartByte:file.DestEndByte]); got != "guide.md#setup" {
		t.Fatalf("dest range = %q", got)
	}

	wiki := d.Links[1]
	if wiki.Kind != LinkFile || wiki.Destination != "Some-Page.md" {
		t.Fatalf("link1 = %+v", wiki)
	}

	anchor := d.Links[2]
	if anchor.Kind != LinkAnchor || anchor.Destination != "#top" {
		t.Fatalf("link2 = %+v", anchor)
	}

	ext := d.Links[3]
	if !ext.Resolve(".", nil).External {
		t.Fatalf("link3 should be external: %+v", ext)
	}
}

func TestLinkDestinationRangeWithEscapedParens(t *testing.T) {
	text := []byte("[a](file\\(1\\).md)")
	d := Parse(string(text))
	if len(d.Links) != 1 {
		t.Fatalf("links = %d", len(d.Links))
	}
	l := d.Links[0]
	if got := string(text[l.DestStartByte:l.DestEndByte]); got != `file\(1\).md` {
		t.Fatalf("dest = %q", got)
	}
	if l.Resolve(".", nil).File != "file(1).md" {
		t.Fatalf("resolve = %+v", l.Resolve(".", nil))
	}
}

func TestWikiLinksInCodeFencesIgnored(t *testing.T) {
	text := "```\n[[Not A Link]]\n```\n\n[[Real Page]]\n"
	d := Parse(text)
	if len(d.Links) != 1 {
		t.Fatalf("links = %+v, want only Real Page", d.Links)
	}
	if d.Links[0].Destination != "Real Page" {
		t.Fatalf("link = %+v", d.Links[0])
	}
}

func TestFrontmatter(t *testing.T) {
	d := Parse("---\ntitle: x\n---\n\n# Body\n")
	if !d.Frontmatter.Has {
		t.Fatal("frontmatter not detected")
	}
	if d.Frontmatter.StartLine != 0 || d.Frontmatter.EndLine != 2 {
		t.Fatalf("span = %+v", d.Frontmatter)
	}
	if len(d.Headings) != 1 {
		t.Fatalf("frontmatter swallowed the body: %+v", d.Headings)
	}

	noFm := Parse("# Just a heading\n---\nnot frontmatter\n")
	if noFm.Frontmatter.Has {
		t.Fatal("setext underline misread as frontmatter")
	}
}

func TestCodeSpans(t *testing.T) {
	d := Parse("before\n\n```go\nfmt.Println()\n```\n\nafter\n")
	if len(d.CodeSpans) != 1 {
		t.Fatalf("code spans = %d", len(d.CodeSpans))
	}
	span := d.CodeSpans[0]
	if span.Start != 2 || span.End != 5 { // fence, code, fence
		t.Fatalf("span = %+v, want 2..5", span)
	}
}

func TestResolveRelative(t *testing.T) {
	d := Parse("[a](sub/page.md#frag)")
	res := d.Links[0].Resolve("notes", nil)
	if res.File != "notes/sub/page.md" || res.Anchor != "frag" {
		t.Fatalf("res = %+v", res)
	}
}

func TestResolveWiki(t *testing.T) {
	d := Parse("[[Index|the index]]")
	res := d.Links[0].Resolve(".", nil)
	if res.File != "Index.md" || !res.Wiki {
		t.Fatalf("res = %+v", res)
	}
	if d.Links[0].WikiDisplay != "the index" {
		t.Fatalf("display = %q", d.Links[0].WikiDisplay)
	}
}

func TestAnchorRange(t *testing.T) {
	d := Parse("# Alpha\n\n## Beta\n")
	start, _, ok := d.AnchorRange("beta")
	if !ok {
		t.Fatal("beta not found")
	}
	if !strings.Contains(d.Text[start:start+4], "Beta") {
		t.Fatalf("range points at %q", d.Text[start:start+4])
	}
}

func TestHeadingAt(t *testing.T) {
	d := Parse("# Rename Me\n")
	h, ok := d.HeadingAt(d.PositionOffset(0, 3))
	if !ok || h.Text != "Rename Me" {
		t.Fatalf("h=%+v ok=%v", h, ok)
	}
}
