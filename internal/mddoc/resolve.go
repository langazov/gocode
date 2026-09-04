package mddoc

import (
	"path"
	"strings"
)

// Resolution is one resolved link target.
type Resolution struct {
	// File is the target file path relative to the workspace root, empty for
	// a same-document anchor.
	File string
	// Anchor is the target heading slug, empty when the link has none.
	Anchor string
	// External marks a link goldmark resolved to a URL (http(s), mailto, …),
	// which is never resolved or diagnosed.
	External bool
	// Wiki marks a link that came from [[...]] syntax.
	Wiki bool
}

// Resolve resolves one link against the document's directory. wikiPath maps a
// wiki link name to a workspace file; nil means wiki links resolve to
// "<name>.md" next to the current document.
func (l Link) Resolve(dir string, wiki func(name string) (string, bool)) Resolution {
	switch l.Kind {
	case LinkAnchor:
		return Resolution{Anchor: strings.TrimPrefix(l.Destination, "#")}
	case LinkWiki:
		res := Resolution{Wiki: true}
		name := strings.TrimSpace(l.Destination)
		if wiki != nil {
			if file, ok := wiki(name); ok {
				res.File = file
				return res
			}
		}
		res.File = path.Join(dir, name+".md")
		return res
	case LinkExternal:
		return Resolution{External: true}
	default: // LinkFile
		res := Resolution{File: UnescapeDestination(l.Destination)}
		if strings.HasPrefix(res.File, "/") {
			// Absolute paths resolve from the workspace root; the caller
			// passes "" as dir for that convention.
			res.File = strings.TrimPrefix(res.File, "/")
			return res
		}
		// Split off any #fragment.
		if hash := strings.Index(res.File, "#"); hash >= 0 {
			res.File, res.Anchor = res.File[:hash], res.File[hash+1:]
		}
		if dir != "" {
			res.File = path.Join(dir, res.File)
		}
		return res
	}
}

// FindHeadingBySlug returns the heading with the given slug.
func (d *Doc) FindHeadingBySlug(slug string) (Heading, bool) {
	for _, h := range d.Headings {
		if h.Slug == slug {
			return h, true
		}
	}
	return Heading{}, false
}

// AnchorRange returns the byte range of the heading text for slug, which is
// what a definition jump needs.
func (d *Doc) AnchorRange(slug string) (start, end int, ok bool) {
	h, found := d.FindHeadingBySlug(slug)
	if !found {
		return 0, 0, false
	}
	return h.StartByte, h.EndByte, true
}
