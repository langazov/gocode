package gocoder

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestUploadLibraryFileWithOverwriteSendsField(t *testing.T) {
	var gotOverwrite string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		gotOverwrite = r.FormValue("overwrite")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"id":"n1","path":".skills/s/SKILL.md","type":"file","sha256":"abc"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	n, err := client.UploadLibraryFileWith(t.Context(), "gk_x", ".skills/s/SKILL.md", "SKILL.md", []byte("x"), UploadOptions{Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if gotOverwrite != "true" || n.SHA256 != "abc" {
		t.Fatalf("overwrite=%q node=%+v", gotOverwrite, n)
	}
	if _, err := client.UploadLibraryFile(t.Context(), "gk_x", "a.md", "a.md", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if gotOverwrite != "" {
		t.Fatalf("default upload sent overwrite=%q", gotOverwrite)
	}
}

func TestDownloadLibraryRawReturnsBytesBeyondOneMegabyte(t *testing.T) {
	big := strings.Repeat("\x00\x01binary", 300_000) // > 1MB, the old response cap
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/library/nodes/n1/raw" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()

	data, err := NewClient(srv.URL).DownloadLibraryRaw(t.Context(), "gk_x", "n1")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != big {
		t.Fatalf("got %d bytes, want %d", len(data), len(big))
	}
}

func TestDeleteLibraryNode(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := NewClient(srv.URL).DeleteLibraryNode(t.Context(), "gk_x", "n1"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodDelete || gotPath != "/library/nodes/n1" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
}

func TestListLibraryTreeIsRecursive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("recursive") != "true" || r.URL.Query().Get("parent") != ".skills/s" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"nodes":[{"id":"n1","path":".skills/s/scripts/run.sh","type":"file"}]}`))
	}))
	defer srv.Close()

	nodes, err := NewClient(srv.URL).ListLibraryTree(t.Context(), "gk_x", ".skills/s")
	if err != nil || len(nodes) != 1 || nodes[0].Path != ".skills/s/scripts/run.sh" {
		t.Fatalf("nodes=%+v err=%v", nodes, err)
	}
}

func TestLibrarySkillMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/library/skills":
			_, _ = w.Write([]byte(`{"skills":[{"name":"s","description":"d","fileCount":1,"files":[{"path":"SKILL.md","id":"n1","sha256":"1"}],"contentHash":"h","status":"ready"}]}`))
		case "/library/skills/s":
			_, _ = w.Write([]byte(`{"name":"s","files":[{"path":"SKILL.md","id":"n1","sha256":"1"}],"contentHash":"h"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"skill not found"}}`))
		}
	}))
	defer srv.Close()
	client := NewClient(srv.URL)

	skills, err := client.ListLibrarySkills(t.Context(), "gk_x")
	if err != nil || len(skills) != 1 || skills[0].Description != "d" || skills[0].Files[0].SHA256 != "1" {
		t.Fatalf("skills=%+v err=%v", skills, err)
	}
	s, err := client.GetLibrarySkill(t.Context(), "gk_x", "s")
	if err != nil || s == nil || s.ContentHash != "h" {
		t.Fatalf("skill=%+v err=%v", s, err)
	}
	missing, err := client.GetLibrarySkill(t.Context(), "gk_x", "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing skill = %+v, %v; want nil, nil", missing, err)
	}
}

func TestSearchLibrarySkills(t *testing.T) {
	var query url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query()
		_, _ = w.Write([]byte(`{"mode":"content","results":[{"skill":"s","description":"d","score":0.9,"chunks":[{"file":"scripts/run.sh","path":".skills/s/scripts/run.sh","role":"script","startLine":1,"endLine":2,"content":"echo hi"}]}]}`))
	}))
	defer srv.Close()

	hits, err := NewClient(srv.URL).SearchLibrarySkills(t.Context(), "gk_x", SkillSearchRequest{
		Query: "run", Mode: SkillSearchContent, Skill: "s", Role: "script", K: 3, PerSkill: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"q": "run", "mode": "content", "skill": "s", "role": "script", "k": "3", "per": "2", "group": "true"} {
		if query.Get(key) != want {
			t.Errorf("%s = %q, want %q", key, query.Get(key), want)
		}
	}
	if len(hits) != 1 || hits[0].Chunks[0].Content != "echo hi" {
		t.Fatalf("hits = %+v", hits)
	}
}

// TestSkillContentHashMatchesServer pins the digest gocode-infra's
// library.SkillContentHash produces for the same input (its
// TestSkillRoleAndHash) — the two implementations must never drift.
func TestSkillContentHashMatchesServer(t *testing.T) {
	got := SkillContentHash([]LibrarySkillFile{{Path: "b", SHA256: "2"}, {Path: "a", SHA256: "1"}})
	if got != "1a04f75bd0704a1e3a5609aa6cef325ce65e2ccde2c36252d0e2fc2ddcb5762d" {
		t.Fatalf("hash = %q", got)
	}
}
