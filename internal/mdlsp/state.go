package mdlsp

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/lspprotocol"
	"github.com/langazov/gocode-go/internal/mddoc"
)

// document is one open markdown file as the actor sees it.
type document struct {
	uri     string
	path    string // absolute filesystem path
	version int
	doc     *mddoc.Doc
}

// workspaceFile is one markdown file of the workspace, indexed on demand.
type workspaceFile struct {
	path  string
	mtime int64
	doc   *mddoc.Doc
}

// state is the full mutable server state. It lives entirely on the actor
// goroutine; nothing here needs a lock.
type state struct {
	root   string
	notify func(method string, params any)

	// docs holds open documents by URI.
	docs map[string]*document

	// index holds known workspace markdown files by root-relative path.
	index        map[string]*workspaceFile
	indexScanned bool
}

// initialize records the workspace root.
func (st *state) initialize(root string) {
	st.root = root
}

// docDir returns the root-relative directory of a document.
func (st *state) docDir(d *document) string {
	if st.root == "" {
		return ""
	}
	rel, err := filepath.Rel(st.root, filepath.Dir(d.path))
	if err != nil || rel == "." {
		return ""
	}
	return rel
}

// didOpen parses a newly opened document.
func (st *state) didOpen(p didOpenParams) {
	path, ok := lspprotocol.PathFromURI(p.TextDocument.URI)
	if !ok {
		return
	}
	st.docs[p.TextDocument.URI] = &document{
		uri:     p.TextDocument.URI,
		path:    path,
		version: p.TextDocument.Version,
		doc:     mddoc.Parse(p.TextDocument.Text),
	}
	st.publishDiagnostics(p.TextDocument.URI)
}

// didChange replaces the document text. The server declares full sync, so
// every change carries the whole text.
func (st *state) didChange(p didChangeParams) {
	doc, ok := st.docs[p.TextDocument.URI]
	if !ok || len(p.ContentChanges) == 0 {
		return
	}
	text := p.ContentChanges[len(p.ContentChanges)-1].Text
	doc.doc = mddoc.Parse(text)
	doc.version = p.TextDocument.Version
	st.publishDiagnostics(p.TextDocument.URI)
}

// didSave refreshes the workspace index with the saved state.
func (st *state) didSave(uri string) {
	st.ensureIndex()
	if doc, ok := st.docs[uri]; ok {
		st.indexRel(doc.path, doc.doc)
	}
}

// didClose forgets an open document. The on-disk file, if any, stays indexed.
func (st *state) didClose(uri string) {
	delete(st.docs, uri)
	st.notifyDiagnostics(uri, nil)
}

// ---- workspace index ----

// ensureIndex scans the workspace once per server lifetime, then relies on
// didSave and didOpen/didClose to keep entries fresh.
func (st *state) ensureIndex() {
	if st.indexScanned || st.root == "" {
		return
	}
	st.indexScanned = true
	st.index = map[string]*workspaceFile{}
	_ = filepath.WalkDir(st.root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable subtree: skip, never fail the walk
		}
		if d.IsDir() {
			name := d.Name()
			if name != "." && strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".md") {
			st.indexRel(path, nil)
		}
		return nil
	})
}

// indexRel records a file's path in the index. A non-nil doc seeds the cache;
// otherwise the file is parsed lazily on first use.
func (st *state) indexRel(path string, doc *mddoc.Doc) {
	st.ensureIndex()
	rel, err := filepath.Rel(st.root, path)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	entry, ok := st.index[rel]
	if !ok {
		entry = &workspaceFile{path: path}
		st.index[rel] = entry
	}
	if doc != nil {
		entry.doc = doc
	}
}

// parseWorkspace loads (or reuses) the parsed form of one indexed file.
func (st *state) parseWorkspace(rel string) *mddoc.Doc {
	st.ensureIndex()
	entry, ok := st.index[rel]
	if !ok {
		return nil
	}
	if entry.doc != nil {
		return entry.doc
	}
	raw, err := os.ReadFile(entry.path)
	if err != nil {
		return nil
	}
	entry.doc = mddoc.Parse(string(raw))
	return entry.doc
}

// openDoc resolves a URI to an open document, falling back to an indexed
// workspace file. The fallback is what makes definition jumps work into
// documents the editor never opened.
func (st *state) openDoc(uri string) (*document, bool) {
	if doc, ok := st.docs[uri]; ok {
		return doc, true
	}
	path, ok := lspprotocol.PathFromURI(uri)
	if !ok || st.root == "" {
		return nil, false
	}
	rel, err := filepath.Rel(st.root, path)
	if err != nil {
		return nil, false
	}
	parsed := st.parseWorkspace(filepath.ToSlash(rel))
	if parsed == nil {
		return nil, false
	}
	doc := &document{uri: uri, path: path, doc: parsed}
	st.docs[uri] = doc
	return doc, true
}

// docURI is the canonical URI for a root-relative path.
func (st *state) docURI(rel string) string {
	return lspprotocol.URIFromPath(filepath.Join(st.root, filepath.FromSlash(rel)))
}

// wikiLookup resolves a wiki-link name to a workspace path, trying the
// basename both with and without an .md suffix.
func (st *state) wikiLookup(name string) (string, bool) {
	st.ensureIndex()
	candidates := []string{filepath.ToSlash(name)}
	if !strings.HasSuffix(name, ".md") {
		candidates = append(candidates, filepath.ToSlash(name)+".md")
	} else {
		candidates = append(candidates, strings.TrimSuffix(name, ".md"))
	}
	for _, candidate := range candidates {
		if _, ok := st.index[candidate]; ok {
			return candidate, true
		}
	}
	// Basename search: [[Setup]] finds docs/setup.md when no setup.md exists.
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(name), ".md"))
	var found string
	for rel := range st.index {
		if strings.ToLower(strings.TrimSuffix(filepath.Base(rel), ".md")) == base {
			if found != "" {
				return "", false // ambiguous: no confident answer
			}
			found = rel
		}
	}
	return found, found != ""
}

// ---- diagnostics ----

// publishDiagnostics computes and publishes the diagnostics of one document.
func (st *state) publishDiagnostics(uri string) {
	doc, ok := st.docs[uri]
	if !ok {
		return
	}
	st.notifyDiagnostics(uri, st.diagnosticsFor(doc))
}

func (st *state) notifyDiagnostics(uri string, diags []lspprotocol.Diagnostic) {
	if st.notify == nil {
		return
	}
	if diags == nil {
		diags = []lspprotocol.Diagnostic{}
	}
	st.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI:         uri,
		Diagnostics: diags,
	})
}

type publishDiagnosticsParams struct {
	URI         string                   `json:"uri"`
	Diagnostics []lspprotocol.Diagnostic `json:"diagnostics"`
}

// diagnosticsFor finds links that do not resolve: broken same-document
// anchors and references to workspace files that do not exist. External URLs
// are never diagnosed, and links inside code are not links at all.
func (st *state) diagnosticsFor(doc *document) []lspprotocol.Diagnostic {
	var out []lspprotocol.Diagnostic
	dir := st.docDir(doc)
	for _, link := range doc.doc.Links {
		res := link.Resolve(dir, st.wikiLookup)
		if res.External {
			continue
		}
		diag := st.checkLink(doc, link, res)
		if diag != nil {
			out = append(out, *diag)
		}
	}
	return out
}

// checkLink returns a diagnostic for one unresolvable link, nil when fine.
func (st *state) checkLink(doc *document, link mddoc.Link, res mddoc.Resolution) *lspprotocol.Diagnostic {
	if res.File == "" {
		if res.Anchor != "" {
			if _, ok := doc.doc.FindHeadingBySlug(res.Anchor); !ok {
				return st.linkDiagnostic(doc, link, "no heading named \""+res.Anchor+"\" in this document")
			}
		}
		return nil
	}
	target := st.parseWorkspace(res.File)
	if target == nil {
		if st.workspaceHasFile(res.File) {
			return nil // exists but is not markdown-indexed; leave it alone
		}
		return st.linkDiagnostic(doc, link, "file \""+res.File+"\" not found in workspace")
	}
	if res.Anchor != "" {
		if _, ok := target.FindHeadingBySlug(res.Anchor); !ok {
			return st.linkDiagnostic(doc, link, "no heading named \""+res.Anchor+"\" in "+res.File)
		}
	}
	return nil
}

// workspaceHasFile reports whether rel is a real file the index chose not to
// parse (non-markdown).
func (st *state) workspaceHasFile(rel string) bool {
	if st.root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(st.root, filepath.FromSlash(rel)))
	return err == nil
}

func (st *state) linkDiagnostic(doc *document, link mddoc.Link, message string) *lspprotocol.Diagnostic {
	line, char := doc.doc.OffsetPosition(link.StartByte)
	_, endChar := doc.doc.OffsetPosition(link.EndByte)
	return &lspprotocol.Diagnostic{
		Range: lspprotocol.Range{
			Start: lspprotocol.Position{Line: line, Character: char},
			End:   lspprotocol.Position{Line: line, Character: endChar},
		},
		Severity: lspprotocol.SeverityWarning,
		Source:   "mdlsp",
		Message:  message,
	}
}

// ---- symbols and folding (implemented in symbols.go) ----
