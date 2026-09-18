package gocoder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
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

// UploadLibraryFile uploads data as filename at path (multipart/form-data,
// matching POST /library/nodes' r.FormFile("file")/r.FormValue("path")).
// Doesn't fit do's JSON-in/JSON-out shape, so it builds the request
// directly and shares only doRequest's transport/error handling.
func (c *Client) UploadLibraryFile(ctx context.Context, bearer, path, filename string, data []byte) (*LibraryNode, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	if err := w.WriteField("path", path); err != nil {
		return nil, err
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
