package gocoder

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchLibraryRequestsFullAndParsesHits(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		if r.Header.Get("Authorization") != "Bearer gk_x" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"node":{"id":"n1","path":"docs/notes.md","type":"file"},"score":0.9,"startLine":1,"endLine":3,"content":"full chunk text"}]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	hits, err := client.SearchLibrary(t.Context(), "gk_x", "findable", "docs", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotPath, "q=findable") || !strings.Contains(gotPath, "full=true") ||
		!strings.Contains(gotPath, "path=docs") || !strings.Contains(gotPath, "k=5") {
		t.Fatalf("request path = %q, missing expected query params", gotPath)
	}
	if len(hits) != 1 || hits[0].Content != "full chunk text" || hits[0].Node.ID != "n1" {
		t.Fatalf("hits = %+v", hits)
	}
}

func TestListLibraryNodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("parent") != "docs" {
			t.Errorf("parent = %q", r.URL.Query().Get("parent"))
		}
		_, _ = w.Write([]byte(`{"nodes":[{"id":"n1","path":"docs/notes.md","type":"file"}]}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	nodes, err := client.ListLibraryNodes(t.Context(), "gk_x", "docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].ID != "n1" {
		t.Fatalf("nodes = %+v", nodes)
	}
}

func TestGetLibraryContentReturnsRawBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/nodes/n1/content" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Notes\n\nhello\n"))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	content, err := client.GetLibraryContent(t.Context(), "gk_x", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if content != "# Notes\n\nhello\n" {
		t.Fatalf("content = %q", content)
	}
}

func TestCreateLibraryFolderTreats409AsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["path"] != "docs" {
			t.Fatalf("path = %q", body["path"])
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"conflict","message":"a node already exists at that path"}}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	if err := client.CreateLibraryFolder(t.Context(), "gk_x", "docs"); err != nil {
		t.Fatalf("409 should be treated as success: %v", err)
	}
}

func TestCreateLibraryFolderPropagatesOtherErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"boom"}}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	err := client.CreateLibraryFolder(t.Context(), "gk_x", "docs")
	var apiErr *APIError
	if err == nil || !errors.As(err, &apiErr) || apiErr.Status != http.StatusInternalServerError {
		t.Fatalf("err = %v", err)
	}
}

func TestUploadLibraryFile(t *testing.T) {
	var gotPath, gotFilename, gotContentType string
	var gotContent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		gotPath = r.FormValue("path")
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		gotFilename = header.Filename
		gotContentType = r.Header.Get("Content-Type")
		gotContent = make([]byte, header.Size)
		_, _ = file.Read(gotContent)

		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"n1","path":"docs/notes.md","type":"file","status":"uploading"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	n, err := client.UploadLibraryFile(t.Context(), "gk_x", "docs/notes.md", "notes.md", []byte("hello world"))
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "n1" || n.Status != LibraryStatusUploading {
		t.Fatalf("node = %+v", n)
	}
	if gotPath != "docs/notes.md" || gotFilename != "notes.md" || string(gotContent) != "hello world" {
		t.Fatalf("path=%q filename=%q content=%q", gotPath, gotFilename, gotContent)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Fatalf("content-type = %q", gotContentType)
	}
}
