package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
