package modelsdev

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSavedContextLimits(t *testing.T) {
	ctx := context.Background()
	base := Catalog{"openai": {ID: "openai", Models: map[string]Model{
		"known": {ID: "known", Name: "Known", Limit: Limit{Context: 128000, Output: 16000}},
	}}}
	s := NewWithCatalog(base)
	s.Filepath = filepath.Join(t.TempDir(), "models.json")
	live := map[string]Model{
		"known": {ID: "known", Limit: Limit{Context: 500000}},
		"new":   {ID: "new", Name: "New", Limit: Limit{Context: 1000000}},
	}
	if err := s.SaveContextLimits(ctx, "openai", live); err != nil {
		t.Fatal(err)
	}
	check := func(catalog Catalog) {
		t.Helper()
		known := catalog["openai"].Models["known"]
		if known.Limit.Context != 500000 || known.Limit.Output != 16000 || known.Name != "Known" {
			t.Fatalf("known metadata: %+v", known)
		}
		if catalog["openai"].Models["new"].Limit.Context != 1000000 {
			t.Fatal("new live model not retained")
		}
	}
	catalog, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(catalog)
	if base["openai"].Models["known"].Limit.Context != 128000 {
		t.Fatal("previously returned catalog mutated")
	}
	// A missing wire field must not erase a previously retrieved limit.
	if err := s.SaveContextLimits(ctx, "openai", map[string]Model{"known": {ID: "known"}}); err != nil {
		t.Fatal(err)
	}
	catalog, _ = s.Get(ctx)
	check(catalog)
	// Simulate a restart or background catalog replacement with stale limits.
	data, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Filepath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	restarted := NewWithCatalog(nil)
	restarted.Filepath = s.Filepath
	restarted.invalidate()
	catalog, err = restarted.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(catalog)
}
