package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/skill"
)

func TestRescanSkillsPicksUpNewSkill(t *testing.T) {
	root := t.TempDir()
	srv := &Server{Skills: skill.Discover(root)}
	mux := srv.Mux()

	dir := filepath.Join(root, "skills", "fresh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: fresh\ndescription: d\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/skill/rescan", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("rescan = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Added   []string `json:"added"`
		Removed []string `json:"removed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Added) != 1 || body.Added[0] != "fresh" || len(body.Removed) != 0 {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if _, ok := srv.Skills.Get("fresh"); !ok {
		t.Fatal("registry not updated in place")
	}
}

func TestExternalSkillsJoinListBelowDiskSkills(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "shared")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: shared\ndescription: on disk\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Skills: skill.Discover(root)}
	mux := srv.Mux()

	body := `{"skills":[{"name":"go-dev","description":"remote","content":"body","location":"library: .skills/go-dev"},{"name":"shared","description":"remote copy"}]}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/skill/external/library-plugin", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("put = %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/skill", nil))
	var listed []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, item := range listed {
		byName[item["name"].(string)] = item
	}
	if byName["go-dev"]["source"] != "library-plugin" {
		t.Fatalf("external skill missing or unmarked: %v", byName["go-dev"])
	}
	if byName["shared"]["description"] != "on disk" {
		t.Fatalf("disk skill was shadowed by the external one: %v", byName["shared"])
	}

	// A rescan keeps external skills; clearing the source drops them.
	srv.Skills.Rescan()
	if _, ok := srv.Skills.Get("go-dev"); !ok {
		t.Fatal("rescan dropped the external skill")
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/skill/external/library-plugin", nil))
	if _, ok := srv.Skills.Get("go-dev"); ok || rec.Code != http.StatusNoContent {
		t.Fatalf("clear = %d, skill still present: %v", rec.Code, ok)
	}
}
