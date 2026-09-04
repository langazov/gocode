// Package mddoc parses markdown documents into the structure a language
// server needs: headings with GitHub-style anchors, links with their source
// ranges, code blocks, and the frontmatter block. It is protocol-agnostic —
// positions are byte offsets plus a lineIndex for translating to and from the
// UTF-16 positions the LSP wire format uses.
package mddoc

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// LinkKind classifies one parsed link.
type LinkKind int

const (
	// LinkAnchor is an in-document heading anchor: [text](#slug).
	LinkAnchor LinkKind = iota
	// LinkFile is a relative file link, optionally with an anchor:
	// [text](other.md) or [text](other.md#section).
	LinkFile
	// LinkExternal is an absolute URL (http(s), mailto, …), which is never
	// resolved or diagnosed.
	LinkExternal
	// LinkWiki is a wiki-style link: [[Name]], [[Name|display]] or [[#slug]].
	LinkWiki
)

// Heading is one ATX or setext heading.
type Heading struct {
	Level     int
	Text      string // raw heading text, inline markup intact
	Slug      string // GitHub-style anchor, deduped across the document
	StartByte int    // byte offset of the heading text (for selection ranges)
	EndByte   int
}

// Link is one link occurrence in the document.
type Link struct {
	Kind LinkKind
	// Destination is the raw link target exactly as written: "#slug",
	// "dir/file.md#frag", "Wiki Name". Backslash escapes are not reversed;
	// run UnescapeDestination before using it as a path. Empty for an
	// external URL, which is never resolved internally.
	Destination string
	// WikiDisplay is the display part of "Name|display", empty otherwise.
	WikiDisplay string
	// StartByte/EndByte span the link's source text, EndByte exclusive.
	StartByte, EndByte int
	// DestStartByte/DestEndByte span the destination inside the source.
	// Negative when it could not be located (reference-style links); rename
	// then falls back to a textual scan.
	DestStartByte, DestEndByte int
}

// UnescapeDestination reverses the backslash escapes CommonMark allows in a
// link destination.
func UnescapeDestination(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// LineSpan is a half-open [Start, End) range of lines.
type LineSpan struct {
	Start int // inclusive
	End   int // exclusive
}

// Contains reports whether line falls inside the span.
func (s LineSpan) Contains(line int) bool { return line >= s.Start && line < s.End }

// Frontmatter describes the raw YAML frontmatter block, if any.
type Frontmatter struct {
	StartLine int // the opening "---" line
	EndLine   int // the closing "---" line
	Has       bool
}

// Span returns the frontmatter's line span.
func (f Frontmatter) Span() LineSpan {
	if !f.Has {
		return LineSpan{}
	}
	return LineSpan{Start: f.StartLine, End: f.EndLine + 1}
}

// Doc is a parsed markdown document.
type Doc struct {
	Text        string
	ix          lineIndex
	Headings    []Heading
	Links       []Link
	CodeSpans   []LineSpan // fenced and indented code blocks
	Frontmatter Frontmatter

	// wikiExcluded holds byte ranges wiki-link scanning must skip — inline
	// code spans, where [[...]] is literal text.
	wikiExcluded [][2]int
}

var md = goldmark.New(
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

// Parse parses markdown text into a Doc. Markdown has no fatal parse errors,
// so a Doc is always returned.
func Parse(content string) *Doc {
	doc := &Doc{Text: content, ix: newLineIndex(content)}
	doc.parseFrontmatter()
	doc.parseAST()
	doc.assignSlugs()
	return doc
}

func (d *Doc) parseFrontmatter() {
	lines := strings.Split(d.Text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r") != "---" {
		return
	}
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimRight(lines[i], "\r")
		if trimmed == "---" || trimmed == "..." {
			d.Frontmatter = Frontmatter{StartLine: 0, EndLine: i, Has: true}
			return
		}
	}
	// Unterminated frontmatter: treat as none, the body is plain text.
}

// parseAST walks the goldmark tree. Frontmatter lines are masked out before
// parsing: goldmark would otherwise read the closing "---" as a setext
// underline and turn frontmatter keys into headings. Masking with spaces
// keeps every later byte offset valid. Masked src is used for all AST
// segments; d.Text remains the real document.
func (d *Doc) parseAST() {
	src := []byte(d.Text)
	if d.Frontmatter.Has {
		start := d.ix.lineStart(d.Frontmatter.StartLine)
		end := d.ix.lineStart(d.Frontmatter.EndLine + 1)
		masked := make([]byte, len(src))
		copy(masked, src)
		for i := start; i < end; i++ {
			if masked[i] != '\n' {
				masked[i] = ' '
			}
		}
		src = masked
	}

	// Paragraph source ranges, scanned for wiki links after the walk: the
	// inline parser splits [[Name]] across several Text nodes, so the raw
	// source is the only reliable place to find them.
	var paragraphs []text.Segment

	root := md.Parser().Parse(text.NewReader(src))
	ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.Kind() {
		case ast.KindHeading:
			d.addHeading(n.(*ast.Heading), src)
		case ast.KindFencedCodeBlock:
			d.addCodeBlock(n.Lines(), true)
			return ast.WalkSkipChildren, nil
		case ast.KindCodeBlock:
			d.addCodeBlock(n.Lines(), false)
			return ast.WalkSkipChildren, nil
		case ast.KindHTMLBlock:
			return ast.WalkSkipChildren, nil
		case ast.KindCodeSpan:
			d.addInlineCode(n)
			return ast.WalkSkipChildren, nil
		case ast.KindLink, ast.KindImage:
			if data, ok := linkFields(n); ok {
				d.addInlineLink(data, src, n.Kind() == ast.KindImage)
			}
			return ast.WalkSkipChildren, nil
		case ast.KindParagraph:
			if n.Lines().Len() > 0 {
				paragraphs = append(paragraphs,
					text.NewSegment(n.Lines().At(0).Start, n.Lines().At(n.Lines().Len()-1).Stop))
			}
		}
		return ast.WalkContinue, nil
	})

	for _, seg := range paragraphs {
		d.scanWikiLinks(seg.Start, seg.Stop, src)
	}
}

func (d *Doc) addHeading(h *ast.Heading, src []byte) {
	segments := h.Lines()
	if segments.Len() == 0 {
		return
	}
	first := segments.At(0)
	last := segments.At(segments.Len() - 1)
	d.Headings = append(d.Headings, Heading{
		Level:     h.Level,
		Text:      strings.TrimRight(string(src[first.Start:last.Stop]), " \t"),
		StartByte: first.Start,
		EndByte:   last.Stop,
	})
}

func (d *Doc) addCodeBlock(segments *text.Segments, fenced bool) {
	if segments.Len() == 0 {
		return
	}
	first := segments.At(0)
	last := segments.At(segments.Len() - 1)
	startLine := d.lineOf(first.Start)
	// Content segments include the trailing newline, so the stop offset sits
	// at the start of the next line; the code's last line is one back.
	endLine := d.lineOf(last.Stop - 1)
	if endLine < startLine {
		endLine = startLine
	}
	if fenced {
		// The opening fence with its info string is the line above the
		// content, the closing fence the line below.
		startLine--
		endLine++
	}
	d.CodeSpans = append(d.CodeSpans, LineSpan{Start: startLine, End: endLine + 1})
}

// lineOf maps a byte offset to its line number.
func (d *Doc) lineOf(offset int) int {
	line, _ := d.ix.offsetToPosition(d.Text, offset)
	return line
}

// LineOfByte maps a byte offset to its zero-based line number.
func (d *Doc) LineOfByte(offset int) int { return d.lineOf(offset) }

// addInlineCode records the byte range of one `code span`, so wiki-link
// scanning can skip it.
func (d *Doc) addInlineCode(n ast.Node) {
	first, last := inlineChildSpan(n)
	if first < 0 {
		return
	}
	d.wikiExcluded = append(d.wikiExcluded, [2]int{first, last})
}

// inlineChildSpan returns the source span of an inline node's text children.
// Returns -1 when the node has no text children.
func inlineChildSpan(n ast.Node) (int, int) {
	first, last := -1, -1
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if t, ok := c.(*ast.Text); ok && !t.Segment.IsEmpty() {
			if first < 0 {
				first = t.Segment.Start
			}
			last = t.Segment.Stop
		}
	}
	return first, last
}

// linkData carries the fields links and images share. The two are distinct
// goldmark types embedding the same baseLink, with no common interface, so
// the extraction is by type switch.
type linkData struct {
	node ast.Node
	dest []byte
}

func linkFields(n ast.Node) (linkData, bool) {
	switch v := n.(type) {
	case *ast.Link:
		return linkData{node: v, dest: v.Destination}, true
	case *ast.Image:
		return linkData{node: v, dest: v.Destination}, true
	}
	return linkData{}, false
}

// addInlineLink records one [text](dest) link (or image). goldmark stores
// Destination as the raw source bytes — escapes and all — so the
// destination's source range is found by locating that exact text after the
// label. UnescapeDestination is applied only when the destination is used as
// a path.
func (d *Doc) addInlineLink(data linkData, src []byte, isImage bool) {
	dest := string(data.dest)
	if dest == "" {
		return
	}
	l := Link{Destination: dest, DestStartByte: -1, DestEndByte: -1}
	switch {
	case strings.HasPrefix(dest, "#"):
		l.Kind = LinkAnchor
	case strings.Contains(dest, "://"):
		l.Kind = LinkExternal
	default:
		l.Kind = LinkFile
	}

	// Source span: the label's text children plus the brackets around them
	// and the parenthesized destination after them.
	labelStart, labelEnd := inlineChildSpan(data.node)
	if labelStart >= 0 {
		open := labelStart - 1
		if isImage && open > 0 && src[open-1] == '!' {
			open--
		}
		l.StartByte = open
		l.EndByte = labelEnd
		if destStart, destEnd, ok := findDestinationRange(src, labelEnd, dest); ok {
			l.DestStartByte, l.DestEndByte = destStart, destEnd
			l.EndByte = destEnd + 1
		}
	}
	d.Links = append(d.Links, l)
}

// findDestinationRange locates the destination of an inline link in src,
// scanning forward from labelEnd for "](" and reading to the matching ")".
// Returns ok=false for reference-style links, where the pattern does not
// match the destination.
func findDestinationRange(src []byte, labelEnd int, dest string) (int, int, bool) {
	i := labelEnd
	for i < len(src)-1 {
		if src[i] == ']' && src[i+1] == '(' {
			open := i + 2
			depth := 0
			for j := open; j < len(src); j++ {
				switch src[j] {
				case '\\':
					j++ // skip the escaped character
				case '(':
					depth++
				case ')':
					if depth == 0 {
						if string(src[open:j]) == dest {
							return open, j, true
						}
						return 0, 0, false
					}
					depth--
				}
			}
			return 0, 0, false
		}
		i++
	}
	return 0, 0, false
}

// excluded reports whether the byte range overlaps an inline code span.
func (d *Doc) excluded(start, end int) bool {
	for _, r := range d.wikiExcluded {
		if start < r[1] && end > r[0] {
			return true
		}
	}
	return false
}

// scanWikiLinks finds [[Name]] and [[Name|display]] links in one paragraph's
// source range. Code fences never reach this — only paragraphs are scanned —
// and inline code spans are excluded by byte range.
func (d *Doc) scanWikiLinks(start, end int, src []byte) {
	for i := start; i+1 < end && i+1 < len(src); {
		if src[i] != '[' || src[i+1] != '[' {
			i++
			continue
		}
		closeIdx := -1
		for j := i + 2; j+1 < end && j+1 < len(src); j++ {
			if src[j] == '\n' || src[j] == '[' {
				break // wiki targets cannot span lines or nest brackets
			}
			if src[j] == ']' && src[j+1] == ']' {
				closeIdx = j
				break
			}
		}
		if closeIdx < 0 {
			i += 2
			continue
		}
		inner := string(src[i+2 : closeIdx])
		target, display := inner, ""
		if bar := strings.Index(inner, "|"); bar >= 0 {
			target, display = inner[:bar], inner[bar+1:]
		}
		kind := LinkWiki
		if strings.HasPrefix(target, "#") {
			kind = LinkAnchor // [[#slug]] targets this document
			target = target[1:]
		}
		if !d.excluded(i, closeIdx+2) {
			d.Links = append(d.Links, Link{
				Kind:          kind,
				Destination:   target,
				WikiDisplay:   display,
				StartByte:     i,
				EndByte:       closeIdx + 2,
				DestStartByte: i + 2,
				DestEndByte:   closeIdx,
			})
		}
		i = closeIdx + 2
	}
}

// Slug computes the GitHub-style anchor of heading text without dedupe
// context — the name a #link would use when the heading is unique.
func Slug(headingText string) string { return slugBase(plainText(headingText)) }

// assignSlugs computes each heading's GitHub-style anchor in document order,
// deduping repeats.
func (d *Doc) assignSlugs() {
	s := newSlugger()
	for i := range d.Headings {
		d.Headings[i].Slug = s.Slug(plainText(d.Headings[i].Text))
	}
}

// plainText strips the common inline markup characters so `**Bold** Title`
// slugs the same as its rendered text does.
func plainText(s string) string {
	if !strings.ContainsAny(s, "*_~`") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '*', '_', '~', '`':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// OffsetPosition converts a byte offset to an LSP line/character pair.
func (d *Doc) OffsetPosition(offset int) (line, character int) {
	return d.ix.offsetToPosition(d.Text, offset)
}

// PositionOffset converts an LSP position to a byte offset. Out-of-range
// positions clamp to the nearest valid offset.
func (d *Doc) PositionOffset(line, character int) int {
	return d.ix.positionToOffset(d.Text, line, character)
}

// LineCount reports the number of lines in the document.
func (d *Doc) LineCount() int { return d.ix.lines() }

// LineSpanOf returns the half-open line span of a byte range.
func (d *Doc) LineSpanOf(startByte, endByte int) LineSpan {
	return LineSpan{Start: d.lineOf(startByte), End: d.lineOf(endByte) + 1}
}

// HeadingAt reports the heading whose text contains the given byte offset,
// which is what prepareRename needs.
func (d *Doc) HeadingAt(offset int) (Heading, bool) {
	for _, h := range d.Headings {
		if offset >= h.StartByte && offset <= h.EndByte {
			return h, true
		}
	}
	return Heading{}, false
}

// CharacterToLineByte converts a UTF-16 character count within one line to a
// byte offset relative to the line start.
func (d *Doc) CharacterToLineByte(line, character int) int {
	start := d.ix.lineStart(line)
	return d.ix.positionToOffset(d.Text, line, character) - start
}

// LineText returns the document's line at the given zero-based line, without
// its terminator.
func (d *Doc) LineText(line int) string {
	start := d.ix.lineStart(line)
	end := len(d.Text)
	if line+1 < len(d.ix.starts) {
		end = d.ix.starts[line+1] - 1
	}
	if end < start || start > len(d.Text) {
		return ""
	}
	return d.Text[start:end]
}
