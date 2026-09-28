package gocoder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// library.go talks to gocoder.org's Library service (PLAN-LIBRARY.md in
// gocode-infra): a per-account folder/file tree of uploaded md/txt/pdf
// documents, converted, chunked, embedded and semantically searchable. It
// shares Client's BaseURL/bearer-key auth with every other gocoder.org
// route this package wraps (account.go, gocoder.go) — the Library routes
// take the same gk_ API key.

// Library node types (website/backend/internal/library/node.go's Type).
const (
	LibraryTypeFolder = "folder"
	LibraryTypeFile   = "file"
)

// Library node status values — the indexing pipeline's state machine
// (website/backend/internal/library/node.go's Status).
const (
	LibraryStatusUploading  = "uploading"
	LibraryStatusConverting = "converting"
	LibraryStatusChunking   = "chunking"
	LibraryStatusEmbedding  = "embedding"
	LibraryStatusReady      = "ready"
	LibraryStatusFailed     = "failed"
)

// LibraryNode is one folder or file in the account's library — the subset
// of website/backend/internal/library.Node's JSON fields a client needs.
type LibraryNode struct {
	ID         string `json:"id"`
	Path       string `json:"path"`
	ParentPath string `json:"parentPath"`
	Name       string `json:"name"`
	Type       string `json:"type"`

	SourceKind string `json:"sourceKind,omitempty"`
	SizeBytes  int64  `json:"sizeBytes,omitempty"`
	// SHA256 is the hex digest of the original uploaded bytes — what a
	// local file is compared against. Empty for nodes uploaded before
	// gocoder.org recorded it.
	SHA256 string `json:"sha256,omitempty"`
	// Description is a skill SKILL.md's frontmatter description; empty
	// for every other node.
	Description string `json:"description,omitempty"`

	Status       string `json:"status,omitempty"`
	StatusDetail string `json:"statusDetail,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// LibrarySearchHit is one ranked chunk from GET /library/search. Content is
// populated (and Snippet left empty) when the request asked for full=true;
// Snippet is a 280-char preview otherwise — see SearchLibrary, which always
// asks for full.
type LibrarySearchHit struct {
	Node        *LibraryNode `json:"node"`
	Score       float32      `json:"score"`
	StartLine   int          `json:"startLine"`
	EndLine     int          `json:"endLine"`
	HeadingPath []string     `json:"headingPath,omitempty"`
	Snippet     string       `json:"snippet,omitempty"`
	Content     string       `json:"content,omitempty"`
}

// SearchLibrary embeds query server-side and returns the top k ranked
// chunks, most similar first, optionally restricted to paths under
// pathPrefix. It always requests full=true (the complete, untruncated chunk
// text) rather than the 280-char preview the website's own search UI uses —
// a caller citing a chunk needs the whole thing, not a fragment. Against a
// gocoder.org deployment that predates the full parameter, the server
// silently ignores it and every hit comes back with only Snippet set;
// callers must treat Content=="" as "fall back to Snippet, or fetch the
// full node and slice StartLine:EndLine" rather than assuming Content is
// always present (see cmd/library-plugin's library_search handler).
func (c *Client) SearchLibrary(ctx context.Context, bearer, query, pathPrefix string, k int) ([]LibrarySearchHit, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("full", "true")
	if pathPrefix != "" {
		q.Set("path", pathPrefix)
	}
	if k > 0 {
		q.Set("k", strconv.Itoa(k))
	}
	var out struct {
		Results []LibrarySearchHit `json:"results"`
	}
	if err := c.do(ctx, http.MethodGet, "/library/search?"+q.Encode(), bearer, nil, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// ListLibraryNodes lists the immediate children of parentPath ("" = root),
// folders first then files, alphabetical within each.
func (c *Client) ListLibraryNodes(ctx context.Context, bearer, parentPath string) ([]LibraryNode, error) {
	path := "/library/nodes"
	if parentPath != "" {
		path += "?parent=" + url.QueryEscape(parentPath)
	}
	var out struct {
		Nodes []LibraryNode `json:"nodes"`
	}
	if err := c.do(ctx, http.MethodGet, path, bearer, nil, &out); err != nil {
		return nil, err
	}
	return out.Nodes, nil
}

// GetLibraryNode fetches one node by ID.
func (c *Client) GetLibraryNode(ctx context.Context, bearer, id string) (*LibraryNode, error) {
	var n LibraryNode
	if err := c.do(ctx, http.MethodGet, "/library/nodes/"+url.PathEscape(id), bearer, nil, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// GetLibraryContent fetches a file node's full converted Markdown — the
// source SearchLibrary's fallback path slices StartLine:EndLine out of when
// a hit's Content wasn't populated.
func (c *Client) GetLibraryContent(ctx context.Context, bearer, id string) (string, error) {
	data, err := c.doRaw(ctx, http.MethodGet, "/library/nodes/"+url.PathEscape(id)+"/content", bearer, nil, "")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// CreateLibraryFolder creates a folder node at path. A 409 (already exists)
// is treated as success: callers use this to ensure an ancestor folder
// exists before an upload (library_upload's mkdir -p-style walk), where
// "someone already created it" is exactly as good as "I just created it."
func (c *Client) CreateLibraryFolder(ctx context.Context, bearer, path string) error {
	err := c.do(ctx, http.MethodPost, "/library/folders", bearer, map[string]string{"path": path}, nil)
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		return nil
	}
	return err
}

// UploadOptions tunes UploadLibraryFileWith.
type UploadOptions struct {
	// Overwrite replaces an existing file at path in place (same node ID,
	// re-indexed) instead of the default: an ordinary document gets a
	// disambiguated name ("report (1).pdf"), a skill file is rejected with
	// 409.
	Overwrite bool
}

// UploadLibraryFile uploads data as filename at path with default options.
func (c *Client) UploadLibraryFile(ctx context.Context, bearer, path, filename string, data []byte) (*LibraryNode, error) {
	return c.UploadLibraryFileWith(ctx, bearer, path, filename, data, UploadOptions{})
}

// UploadLibraryFileWith uploads data as filename at path
// (multipart/form-data, matching POST /library/nodes'
// r.FormFile("file")/r.FormValue("path")/r.FormValue("overwrite")).
// Doesn't fit do's JSON-in/JSON-out shape, so it builds the request
// directly and shares only doRequest's transport/error handling.
func (c *Client) UploadLibraryFileWith(ctx context.Context, bearer, path, filename string, data []byte, opts UploadOptions) (*LibraryNode, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("path", path); err != nil {
		return nil, err
	}
	if opts.Overwrite {
		if err := w.WriteField("overwrite", "true"); err != nil {
			return nil, err
		}
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/library/nodes", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	respData, err := c.doRequest(req, bearer)
	if err != nil {
		return nil, err
	}
	var n LibraryNode
	if err := json.Unmarshal(respData, &n); err != nil {
		return nil, fmt.Errorf("gocoder.org: unexpected response: %w", err)
	}
	return &n, nil
}

// DownloadLibraryRaw fetches a file node's original bytes, unconverted —
// GetLibraryContent returns converted Markdown, which is wrong for a
// script or an asset.
func (c *Client) DownloadLibraryRaw(ctx context.Context, bearer, id string) ([]byte, error) {
	return c.doRaw(ctx, http.MethodGet, "/library/nodes/"+url.PathEscape(id)+"/raw", bearer, nil, "")
}

// DeleteLibraryNode deletes a node — recursively for a folder — along with
// its indexed chunks and vectors.
func (c *Client) DeleteLibraryNode(ctx context.Context, bearer, id string) error {
	_, err := c.doRaw(ctx, http.MethodDelete, "/library/nodes/"+url.PathEscape(id), bearer, nil, "")
	return err
}

// ListLibraryTree lists every node under parentPath ("" = the whole
// library) in one request, parentPath itself excluded.
func (c *Client) ListLibraryTree(ctx context.Context, bearer, parentPath string) ([]LibraryNode, error) {
	q := url.Values{}
	q.Set("recursive", "true")
	if parentPath != "" {
		q.Set("parent", parentPath)
	}
	var out struct {
		Nodes []LibraryNode `json:"nodes"`
	}
	if err := c.do(ctx, http.MethodGet, "/library/nodes?"+q.Encode(), bearer, nil, &out); err != nil {
		return nil, err
	}
	return out.Nodes, nil
}

// LibrarySkillFile is one file of a remote skill.
type LibrarySkillFile struct {
	Path      string    `json:"path"` // relative to the skill root
	ID        string    `json:"id"`
	SizeBytes int64     `json:"sizeBytes"`
	SHA256    string    `json:"sha256,omitempty"`
	Status    string    `json:"status,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// LibrarySkill is one skill stored under the Library's .skills/ prefix
// (GET /library/skills[/{name}]).
type LibrarySkill struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Files       []LibrarySkillFile `json:"files"`
	FileCount   int                `json:"fileCount"`
	SizeBytes   int64              `json:"sizeBytes"`
	// ContentHash is SkillContentHash over Files.
	ContentHash string    `json:"contentHash"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// Status is "ready", "indexing" or "failed", aggregated over Files.
	Status string `json:"status"`
}

// SkillContentHash hashes a skill's file set exactly as gocoder.org does
// (website/backend/internal/library.SkillContentHash in gocode-infra):
// sha256 over the files sorted by path, each contributing
// "path\x00sha256\n". Equal hashes mean identical file sets and contents,
// so a local skill folder can be compared with its remote copy without
// per-file requests.
func SkillContentHash(files []LibrarySkillFile) string {
	sorted := append([]LibrarySkillFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		h.Write([]byte(f.Path))
		h.Write([]byte{0})
		h.Write([]byte(f.SHA256))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ListLibrarySkills lists every skill in the library, by name.
func (c *Client) ListLibrarySkills(ctx context.Context, bearer string) ([]LibrarySkill, error) {
	var out struct {
		Skills []LibrarySkill `json:"skills"`
	}
	if err := c.do(ctx, http.MethodGet, "/library/skills", bearer, nil, &out); err != nil {
		return nil, err
	}
	return out.Skills, nil
}

// GetLibrarySkill fetches one skill's metadata. A skill that doesn't exist
// returns (nil, nil) — "not in the library" is an answer, not a failure,
// for every caller (store, diff, list).
func (c *Client) GetLibrarySkill(ctx context.Context, bearer, name string) (*LibrarySkill, error) {
	var s LibrarySkill
	err := c.do(ctx, http.MethodGet, "/library/skills/"+url.PathEscape(name), bearer, nil, &s)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// Skill search modes.
const (
	SkillSearchDiscover = "discover"
	SkillSearchContent  = "content"
)

// SkillSearchRequest is GET /library/skills/search's query.
type SkillSearchRequest struct {
	Query string
	// Mode is SkillSearchDiscover (default: which skill fits a task) or
	// SkillSearchContent (search inside skill files).
	Mode string
	// Skill and Role narrow content search to one skill / one role
	// (skill_md, reference, script, asset).
	Skill string
	Role  string
	// K is the number of skills returned; PerSkill caps chunks per skill
	// in content mode.
	K        int
	PerSkill int
}

// LibrarySkillChunk is one matched chunk inside a content-mode result.
type LibrarySkillChunk struct {
	File        string   `json:"file"` // relative to the skill root
	Path        string   `json:"path"` // full library path
	Role        string   `json:"role"`
	NodeID      string   `json:"nodeId"`
	StartLine   int      `json:"startLine"`
	EndLine     int      `json:"endLine"`
	HeadingPath []string `json:"headingPath,omitempty"`
	Content     string   `json:"content"`
	Score       float32  `json:"score"`
}

// LibrarySkillHit is one skill in a skill search: its description, best
// score, and — in content mode — its best-matching chunks.
type LibrarySkillHit struct {
	Skill       string              `json:"skill"`
	Description string              `json:"description,omitempty"`
	Score       float32             `json:"score"`
	Chunks      []LibrarySkillChunk `json:"chunks,omitempty"`
}

// SearchLibrarySkills runs a server-side skill search. Content mode
// always asks for grouped results (Qdrant group-by on skill), so K counts
// skills, not chunks.
func (c *Client) SearchLibrarySkills(ctx context.Context, bearer string, req SkillSearchRequest) ([]LibrarySkillHit, error) {
	q := url.Values{}
	q.Set("q", req.Query)
	if req.Mode != "" {
		q.Set("mode", req.Mode)
	}
	if req.Skill != "" {
		q.Set("skill", req.Skill)
	}
	if req.Role != "" {
		q.Set("role", req.Role)
	}
	if req.K > 0 {
		q.Set("k", strconv.Itoa(req.K))
	}
	if req.PerSkill > 0 {
		q.Set("per", strconv.Itoa(req.PerSkill))
	}
	q.Set("group", "true")
	var out struct {
		Results []LibrarySkillHit `json:"results"`
	}
	if err := c.do(ctx, http.MethodGet, "/library/skills/search?"+q.Encode(), bearer, nil, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}
