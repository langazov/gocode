package builtins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/tool"
)

func newRoot(t *testing.T) (Resolver, string) {
	t.Helper()
	root := t.TempDir()
	return Resolver{Root: root}, root
}

func TestReadFile(t *testing.T) {
	resolver, root := newRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\nthree"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := NewReadTool(resolver)
	out, err := read.Execute(context.Background(), map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	want := "1: one\n2: two\n3: three"
	if out != want {
		t.Fatalf("expected %q, got %q", want, out)
	}
}

func TestReadOffsetLimit(t *testing.T) {
	resolver, root := newRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\nb\nc\nd"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := NewReadTool(resolver)
	out, err := read.Execute(context.Background(), map[string]any{"path": "a.txt", "offset": float64(2), "limit": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if out != "2: b\n3: c" {
		t.Fatalf("unexpected paged output: %q", out)
	}
}

func TestReadDirectory(t *testing.T) {
	resolver, root := newRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := NewReadTool(resolver)
	out, err := read.Execute(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "f.txt") || !strings.Contains(out, "sub/") {
		t.Fatalf("expected directory listing, got %q", out)
	}
}

func TestResolverRejectsEscape(t *testing.T) {
	resolver, _ := newRoot(t)
	read := NewReadTool(resolver)
	if _, err := read.Execute(context.Background(), map[string]any{"path": "../outside.txt"}); err == nil {
		t.Fatal("expected path escape rejection")
	}
}

func TestWriteCreatesAndOverwrites(t *testing.T) {
	resolver, root := newRoot(t)
	write := NewWriteTool(resolver)
	out, err := write.Execute(context.Background(), map[string]any{"path": "nested/dir/new.txt", "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Created file successfully") {
		t.Fatalf("expected created message, got %q", out)
	}
	data, err := os.ReadFile(filepath.Join(root, "nested", "dir", "new.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("unexpected file content: %q err=%v", data, err)
	}
	out, err = write.Execute(context.Background(), map[string]any{"path": "nested/dir/new.txt", "content": "again"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Wrote file successfully") {
		t.Fatalf("expected wrote message, got %q", out)
	}
}

func TestEditSingleReplacement(t *testing.T) {
	resolver, root := newRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("foo bar foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(resolver)
	_, err := edit.Execute(context.Background(), map[string]any{
		"path":      "a.txt",
		"oldString": "bar",
		"newString": "baz",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "foo baz foo" {
		t.Fatalf("unexpected edit result: %q", data)
	}
}

func TestEditMultipleMatchesRequireFlag(t *testing.T) {
	resolver, root := newRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("foo foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(resolver)
	_, err := edit.Execute(context.Background(), map[string]any{"path": "a.txt", "oldString": "foo", "newString": "bar"})
	if err == nil || !strings.Contains(err.Error(), "multiple exact matches") {
		t.Fatalf("expected multiple-match error, got %v", err)
	}
	_, err = edit.Execute(context.Background(), map[string]any{"path": "a.txt", "oldString": "foo", "newString": "bar", "replaceAll": true})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "bar bar" {
		t.Fatalf("unexpected replaceAll result: %q", data)
	}
}

func TestEditGuards(t *testing.T) {
	resolver, root := newRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(resolver)
	if _, err := edit.Execute(context.Background(), map[string]any{"path": "a.txt", "oldString": "x", "newString": "x"}); err == nil {
		t.Fatal("expected identical-string error")
	}
	if _, err := edit.Execute(context.Background(), map[string]any{"path": "a.txt", "oldString": "", "newString": "y"}); err == nil {
		t.Fatal("expected empty-oldString error")
	}
	if _, err := edit.Execute(context.Background(), map[string]any{"path": "a.txt", "oldString": "missing", "newString": "y"}); err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestEditPreservesCRLF(t *testing.T) {
	resolver, root := newRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("foo\r\nbar\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	edit := NewEditTool(resolver)
	_, err := edit.Execute(context.Background(), map[string]any{"path": "a.txt", "oldString": "foo\nbar", "newString": "baz\nqux"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(data) != "baz\r\nqux\r\n" {
		t.Fatalf("expected CRLF preserved, got %q", data)
	}
}

func TestGlob(t *testing.T) {
	resolver, root := newRoot(t)
	for _, rel := range []string{"src/a.ts", "src/b.tsx", "src/deep/c.ts", "README.md"} {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	glob := NewGlobTool(resolver)
	out, err := glob.Execute(context.Background(), map[string]any{"pattern": "**/*.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "src/a.ts") || !strings.Contains(out, "src/deep/c.ts") {
		t.Fatalf("expected ts matches, got %q", out)
	}
	if strings.Contains(out, "b.tsx") || strings.Contains(out, "README.md") {
		t.Fatalf("unexpected matches: %q", out)
	}
	out, err = glob.Execute(context.Background(), map[string]any{"pattern": "nothing-here.*"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "No files found" {
		t.Fatalf("expected no files, got %q", out)
	}
}

func TestGrep(t *testing.T) {
	resolver, root := newRoot(t)
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\nfunc hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	grep := NewGrepTool(resolver)
	out, err := grep.Execute(context.Background(), map[string]any{"pattern": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Found 2 matches") || !strings.Contains(out, "a.go") || !strings.Contains(out, "b.txt") {
		t.Fatalf("unexpected grep output: %q", out)
	}
	out, err = grep.Execute(context.Background(), map[string]any{"pattern": "hello", "include": "*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "b.txt") || !strings.Contains(out, "a.go") {
		t.Fatalf("include filter failed: %q", out)
	}
}

func TestBash(t *testing.T) {
	resolver, root := newRoot(t)
	bash := NewBashTool(resolver)
	out, err := bash.Execute(context.Background(), map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Fatalf("unexpected bash output: %q", out)
	}
	pwd := "pwd"
	if runtime.GOOS == "windows" {
		pwd = "cd"
	}
	out, err = bash.Execute(context.Background(), map[string]any{"command": pwd, "workdir": "."})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := filepath.EvalSymlinks(root)
	got, _ := filepath.EvalSymlinks(strings.TrimSpace(out))
	if got != resolved {
		t.Fatalf("expected workdir %s, got %s", resolved, got)
	}
	_, err = bash.Execute(context.Background(), map[string]any{"command": "exit 3"})
	if err == nil || !strings.Contains(err.Error(), "code 3") {
		t.Fatalf("expected exit code 3 error, got %v", err)
	}
}

func TestBashTimeout(t *testing.T) {
	resolver, _ := newRoot(t)
	bash := NewBashTool(resolver)
	_, err := bash.Execute(context.Background(), map[string]any{"command": "sleep 5", "timeout": float64(200)})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestRegisterAll(t *testing.T) {
	registry := tool.NewRegistry()
	Register(registry, t.TempDir(), nil)
	names := registry.Names()
	for _, want := range []string{"read", "write", "edit", "glob", "grep", "bash"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected tool %s registered, got %v", want, names)
		}
	}
	for _, name := range names {
		if name == "todowrite" {
			t.Fatal("todowrite must not register without a database")
		}
	}
}

func TestRegisterTodoWithDB(t *testing.T) {
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	registry := tool.NewRegistry()
	Register(registry, t.TempDir(), database)
	if _, ok := registry.Get("todowrite"); !ok {
		t.Fatal("expected todowrite registered with a database")
	}
}

func TestTodoWriteAndReplace(t *testing.T) {
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(context.Background(), `
		INSERT INTO project (id, worktree, sandboxes, time_created, time_updated)
		VALUES ('prj_1', '/tmp', '[]', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(context.Background(), `
		INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
		VALUES ('ses_1', 'prj_1', 'test', '/tmp', 'Test', '1', 0, 0)`); err != nil {
		t.Fatal(err)
	}

	todo := NewTodoTool(database)
	exec := tool.ExecContext{SessionID: "ses_1"}
	out, err := todo.ExecuteWithContext(context.Background(), map[string]any{
		"todos": []any{
			map[string]any{"content": "first", "status": "pending", "priority": "high"},
			map[string]any{"content": "second", "status": "completed", "priority": "low"},
		},
	}, exec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Fatalf("unexpected output: %s", out)
	}

	var count int
	if err := database.QueryRow(context.Background(), `SELECT COUNT(*) FROM todo WHERE session_id = 'ses_1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected 2 todos, got %d", count)
	}

	if _, err := todo.ExecuteWithContext(context.Background(), map[string]any{
		"todos": []any{map[string]any{"content": "only", "status": "pending", "priority": "medium"}},
	}, exec); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(context.Background(), `SELECT COUNT(*) FROM todo WHERE session_id = 'ses_1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected replace to leave 1 todo, got %d", count)
	}

	if _, err := todo.Execute(context.Background(), map[string]any{"todos": []any{}}); err == nil {
		t.Fatal("expected context-less Execute to fail")
	}
}

func TestStripHTML(t *testing.T) {
	input := `<html><head><title>T</title><style>body{}</style></head><body><h1>Hello</h1><script>evil()</script><p>World &amp; friends</p></body></html>`
	got := stripHTML(input)
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "World & friends") {
		t.Fatalf("expected stripped text, got %q", got)
	}
	if strings.Contains(got, "evil") || strings.Contains(got, "body{}") || strings.Contains(got, "<") {
		t.Fatalf("script/style/tags should be removed, got %q", got)
	}
}

func TestWebFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body><p>Hi there</p></body></html>"))
	}))
	defer srv.Close()

	fetch := NewWebFetchTool()
	out, err := fetch.Execute(context.Background(), map[string]any{"url": srv.URL, "format": "text"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Hi there") || strings.Contains(out, "<p>") {
		t.Fatalf("expected stripped body, got %q", out)
	}

	out, err = fetch.Execute(context.Background(), map[string]any{"url": srv.URL, "format": "html"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "<p>Hi there</p>") {
		t.Fatalf("expected raw html, got %q", out)
	}

	if _, err := fetch.Execute(context.Background(), map[string]any{"url": "ftp://nope"}); err == nil {
		t.Fatal("expected scheme rejection")
	}
}
