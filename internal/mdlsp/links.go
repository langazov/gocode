package mdlsp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/lspprotocol"
	"github.com/langazov/gocode-go/internal/mddoc"
)

// referenceParams is textDocument/references' shape.
type referenceParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position lspprotocol.Position `json:"position"`
	Context  struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	} `json:"context"`
}

// renameParams is textDocument/rename's shape.
type renameParams struct {
	TextDocument struct {
		URI string `json:"uri"`
	} `json:"textDocument"`
	Position lspprotocol.Position `json:"position"`
	NewName  string               `json:"newName"`
}

// definition answers textDocument/definition: the heading an anchor points
// at, or the first heading of the file a file link points at.
func (s *Server) definition(ctx context.Context, raw json.RawMessage) (any, error) {
	var p positionParams
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		return st.definitionAt(doc, p.Position)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (st *state) definitionAt(doc *document, pos lspprotocol.Position) any {
	offset := doc.doc.PositionOffset(pos.Line, pos.Character)
	link, ok := st.linkAt(doc, offset)
	if !ok {
		return []lspprotocol.Location{}
	}
	res := link.Resolve(st.docDir(doc), st.wikiLookup)
	if res.External {
		return []lspprotocol.Location{}
	}
	if res.File == "" {
		// Same-document anchor.
		if start, _, ok := doc.doc.AnchorRange(res.Anchor); ok {
			return []lspprotocol.Location{locationOf(doc, start)}
		}
		return []lspprotocol.Location{}
	}
	target := st.parseWorkspace(res.File)
	if target == nil {
		return []lspprotocol.Location{}
	}
	targetURI := st.docURI(res.File)
	if res.Anchor != "" {
		if start, _, ok := target.AnchorRange(res.Anchor); ok {
			return []lspprotocol.Location{st.locationIn(targetURI, target, start)}
		}
		return []lspprotocol.Location{}
	}
	// No anchor: jump to the file's first heading, or the top.
	if len(target.Headings) > 0 {
		return []lspprotocol.Location{st.locationIn(targetURI, target, target.Headings[0].StartByte)}
	}
	return []lspprotocol.Location{{URI: targetURI, Range: lspprotocol.Range{}}}
}

// locationOf builds a Location inside doc from a byte offset.
func locationOf(doc *document, offset int) lspprotocol.Location {
	line, char := doc.doc.OffsetPosition(offset)
	return lspprotocol.Location{
		URI:   doc.uri,
		Range: lspprotocol.Range{Start: lspprotocol.Position{Line: line, Character: char}},
	}
}

// locationIn builds a Location for a parsed document that may not be open in
// the editor.
func (st *state) locationIn(uri string, parsed *mddocDoc, offset int) lspprotocol.Location {
	line, char := parsed.OffsetPosition(offset)
	return lspprotocol.Location{
		URI:   uri,
		Range: lspprotocol.Range{Start: lspprotocol.Position{Line: line, Character: char}},
	}
}

// linkAt returns the link whose source span contains offset.
func (st *state) linkAt(doc *document, offset int) (mddoc.Link, bool) {
	for _, link := range doc.doc.Links {
		if offset >= link.StartByte && offset < link.EndByte {
			return link, true
		}
	}
	return mddoc.Link{}, false
}

// references answers textDocument/references: every link in the workspace
// that resolves to the heading (or file) at the cursor.
func (s *Server) references(ctx context.Context, raw json.RawMessage) (any, error) {
	var p referenceParams
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		refs := st.referencesAt(doc, p.Position, p.Context.IncludeDeclaration)
		if refs == nil {
			refs = []lspprotocol.Location{}
		}
		return refs
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// referencesAt finds inbound links for the anchor or document under the
// cursor. With the cursor on plain text, no links can reference it.
func (st *state) referencesAt(doc *document, pos lspprotocol.Position, includeDecl bool) []lspprotocol.Location {
	offset := doc.doc.PositionOffset(pos.Line, pos.Character)
	heading, onHeading := doc.doc.HeadingAt(offset)
	if !onHeading {
		return nil
	}
	var out []lspprotocol.Location
	rel := st.relOf(doc)
	for _, open := range st.openDocs() {
		for _, link := range open.doc.Links {
			res := link.Resolve(st.docDir(open), st.wikiLookup)
			if st.linkTargets(link, res, rel, heading.Slug) {
				out = append(out, st.locationIn(open.uri, open.doc, link.StartByte))
			}
		}
	}
	_ = includeDecl
	return out
}

// openDocs snapshots the open documents. Called on the actor, so the map is
// stable during iteration.
func (st *state) openDocs() []*document {
	out := make([]*document, 0, len(st.docs))
	for _, doc := range st.docs {
		out = append(out, doc)
	}
	return out
}

// relOf returns a document's root-relative slash path.
func (st *state) relOf(doc *document) string {
	if st.root == "" {
		return doc.path
	}
	rel, err := filepath.Rel(st.root, doc.path)
	if err != nil {
		return doc.path
	}
	return filepath.ToSlash(rel)
}

// linkTargets reports whether a link resolves to the given file+slug pair —
// the "references this heading or file" test for the references query.
func (st *state) linkTargets(link mddoc.Link, res mddoc.Resolution, fileRel, slug string) bool {
	if res.External {
		return false
	}
	if res.File == "" {
		// Anchor in the same document.
		return slug != "" && res.Anchor == slug
	}
	return res.File == fileRel && (res.Anchor == "" || res.Anchor == slug)
}

// linkNamesHeading reports whether a link spells out the heading name — what
// rename may rewrite. Same-document anchors and explicit file#anchor links
// do; bare file links and wiki links naming the file do not.
func linkNamesHeading(res mddoc.Resolution, fileRel, slug string) bool {
	if res.External || slug == "" {
		return false
	}
	if res.File == "" {
		return res.Anchor == slug
	}
	return res.File == fileRel && res.Anchor == slug
}

// documentLink answers textDocument/documentLink: every link rendered as a
// clickable span, external URLs included.
func (s *Server) documentLink(ctx context.Context, raw json.RawMessage) (any, error) {
	var p requestPositional
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		links := st.documentLinks(doc)
		if links == nil {
			links = []lspprotocol.DocumentLink{}
		}
		return links
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (st *state) documentLinks(doc *document) []lspprotocol.DocumentLink {
	var out []lspprotocol.DocumentLink
	dir := st.docDir(doc)
	for _, link := range doc.doc.Links {
		if link.EndByte <= link.StartByte {
			continue
		}
		res := link.Resolve(dir, st.wikiLookup)
		start := toPosition(doc.doc, link.StartByte)
		end := toPosition(doc.doc, link.EndByte)
		target := ""
		switch {
		case res.External:
			target = link.Destination
		case res.File == "":
			target = "#" + res.Anchor // in-page anchor
		default:
			target = st.docURI(res.File)
			if res.Anchor != "" {
				target += "#" + res.Anchor
			}
		}
		out = append(out, lspprotocol.DocumentLink{
			Range:  lspprotocol.Range{Start: start, End: end},
			Target: target,
		})
	}
	return out
}

// completion answers textDocument/completion. The context decides what to
// offer: after "[[" wiki names, after "(#"/"#" heading anchors, after "(" in
// a link workspace files, after "/" path segments.
func (s *Server) completion(ctx context.Context, raw json.RawMessage) (any, error) {
	var p positionParams
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		return st.completions(doc, p.Position)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (st *state) completions(doc *document, pos lspprotocol.Position) any {
	line := doc.doc.LineText(pos.Line)
	// pos.Character counts UTF-16 units across the whole line; convert to a
	// byte index within the line text before slicing.
	byteIdx := doc.doc.CharacterToLineByte(pos.Line, pos.Character)
	if byteIdx > len(line) {
		byteIdx = len(line)
	}
	prefix := line[:byteIdx]

	// Wiki link: the nearest unclosed [[.
	if idx := strings.LastIndex(prefix, "[["); idx >= 0 && !strings.Contains(prefix[idx:], "]]") {
		context := prefix[idx+2:]
		items := st.wikiCompletions(context)
		if items != nil {
			return items
		}
	}
	// Heading anchor: "(" followed by "#fragment".
	if idx := strings.LastIndex(prefix, "("); idx >= 0 {
		tail := prefix[idx+1:]
		if strings.HasPrefix(tail, "#") {
			return st.anchorCompletions(doc, strings.TrimPrefix(tail, "#"))
		}
	}
	// File path: "(" followed by a path fragment.
	if idx := strings.LastIndex(prefix, "("); idx >= 0 {
		fragment := prefix[idx+1:]
		if !strings.Contains(fragment, ")") && !strings.HasPrefix(fragment, "#") {
			return st.fileCompletions(doc, fragment)
		}
	}
	// A bare "#fragment" outside parens: anchors too.
	if idx := strings.LastIndex(prefix, "#"); idx >= 0 {
		fragment := prefix[idx+1:]
		if !strings.ContainsAny(fragment, " \t)(") {
			return st.anchorCompletions(doc, fragment)
		}
	}
	return nil
}

// wikiCompletions lists workspace markdown files as wiki-link candidates.
func (st *state) wikiCompletions(prefix string) []lspprotocol.CompletionItem {
	st.ensureIndex()
	if st.index == nil {
		return nil
	}
	var out []lspprotocol.CompletionItem
	seen := map[string]bool{}
	for rel := range st.index {
		base := strings.TrimSuffix(filepath.Base(rel), ".md")
		if seen[base] || !strings.HasPrefix(strings.ToLower(base), strings.ToLower(prefix)) {
			continue
		}
		seen[base] = true
		out = append(out, lspprotocol.CompletionItem{
			Label:  base,
			Kind:   lspprotocol.CompletionKindFile,
			Detail: rel,
		})
	}
	return out
}

// anchorCompletions lists this document's headings as "#slug" candidates.
func (st *state) anchorCompletions(doc *document, prefix string) []lspprotocol.CompletionItem {
	var out []lspprotocol.CompletionItem
	for _, h := range doc.doc.Headings {
		if !strings.HasPrefix(strings.ToLower(h.Slug), strings.ToLower(prefix)) {
			continue
		}
		out = append(out, lspprotocol.CompletionItem{
			Label:      "#" + h.Slug,
			Kind:       lspprotocol.CompletionKindRef,
			Detail:     h.Text,
			InsertText: h.Slug,
			FilterText: h.Slug,
			SortText:   h.Slug,
		})
	}
	return out
}

// fileCompletions lists workspace files matching a path fragment typed after
// "(".
func (st *state) fileCompletions(doc *document, fragment string) []lspprotocol.CompletionItem {
	st.ensureIndex()
	if st.index == nil {
		return nil
	}
	dir := st.docDir(doc)
	var out []lspprotocol.CompletionItem
	for rel := range st.index {
		if rel == st.relOf(doc) {
			continue
		}
		// Offer paths relative to the current document, which is how the
		// link resolves.
		offer := rel
		if dir != "" && strings.HasPrefix(rel, dir+"/") {
			offer = strings.TrimPrefix(rel, dir+"/")
		}
		if !strings.HasPrefix(strings.ToLower(offer), strings.ToLower(fragment)) {
			continue
		}
		out = append(out, lspprotocol.CompletionItem{
			Label:      offer,
			Kind:       lspprotocol.CompletionKindFile,
			InsertText: offer,
		})
	}
	return out
}

// prepareRename answers textDocument/prepareRename: renaming is offered only
// on a heading, and only the heading text may change.
func (s *Server) prepareRename(ctx context.Context, raw json.RawMessage) (any, error) {
	var p positionParams
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		offset := doc.doc.PositionOffset(p.Position.Line, p.Position.Character)
		h, ok := doc.doc.HeadingAt(offset)
		if !ok {
			return nil
		}
		start := toPosition(doc.doc, h.StartByte)
		end := toPosition(doc.doc, h.EndByte)
		return lspprotocol.Range{Start: start, End: end}
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// rename answers textDocument/rename for headings: it rewrites the heading
// text and every inbound link in the workspace, both markdown-style anchors
// and wiki-style references.
func (s *Server) rename(ctx context.Context, raw json.RawMessage) (any, error) {
	var p renameParams
	json.Unmarshal(raw, &p)
	v, err := s.callSync(ctx, func(st *state) any {
		doc, ok := st.openDoc(p.TextDocument.URI)
		if !ok {
			return nil
		}
		return st.renameAt(doc, p.Position, p.NewName)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (st *state) renameAt(doc *document, pos lspprotocol.Position, newName string) any {
	offset := doc.doc.PositionOffset(pos.Line, pos.Character)
	heading, ok := doc.doc.HeadingAt(offset)
	if !ok || strings.TrimSpace(newName) == "" {
		return nil
	}
	changes := map[string][]lspprotocol.TextEdit{}

	// The heading itself: replace its text span.
	changes[doc.uri] = append(changes[doc.uri], lspprotocol.TextEdit{
		Range: lspprotocol.Range{
			Start: toPosition(doc.doc, heading.StartByte),
			End:   toPosition(doc.doc, heading.EndByte),
		},
		NewText: newName,
	})

	// Inbound links across every open document.
	rel := st.relOf(doc)
	for _, open := range st.openDocs() {
		edits := st.inboundLinkEdits(open, rel, heading.Slug, newName)
		if len(edits) > 0 {
			changes[open.uri] = append(changes[open.uri], edits...)
		}
	}
	return lspprotocol.WorkspaceEdit{Changes: changes}
}

// inboundLinkEdits builds the edits that retarget links in one document that
// point at (fileRel, oldSlug).
func (st *state) inboundLinkEdits(doc *document, fileRel, oldSlug, newName string) []lspprotocol.TextEdit {
	var out []lspprotocol.TextEdit
	dir := st.docDir(doc)
	for _, link := range doc.doc.Links {
		res := link.Resolve(dir, st.wikiLookup)
		if !linkNamesHeading(res, fileRel, oldSlug) {
			continue
		}
		// The replaced text depends on link kind: the whole wiki target, or
		// the destination of a markdown anchor.
		var rng lspprotocol.Range
		newText := ""
		switch link.Kind {
		case mddoc.LinkWiki:
			rng = lspprotocol.Range{
				Start: toPosition(doc.doc, link.DestStartByte),
				End:   toPosition(doc.doc, link.DestEndByte),
			}
			if link.WikiDisplay != "" {
				newText = newName + "|" + link.WikiDisplay
			} else {
				newText = newName
			}
		case mddoc.LinkAnchor:
			rng = lspprotocol.Range{
				Start: toPosition(doc.doc, link.DestStartByte),
				End:   toPosition(doc.doc, link.DestEndByte),
			}
			newText = "#" + mddoc.Slug(newName)
		default:
			// File links without anchor do not carry the heading name; those
			// with one get the new anchor.
			if link.DestStartByte < 0 {
				continue
			}
			rng = lspprotocol.Range{
				Start: toPosition(doc.doc, link.DestStartByte),
				End:   toPosition(doc.doc, link.DestEndByte),
			}
			if res.Anchor != "" {
				newText = res.File + "#" + mddoc.Slug(newName)
			} else {
				continue
			}
		}
		out = append(out, lspprotocol.TextEdit{Range: rng, NewText: newText})
	}
	return out
}
