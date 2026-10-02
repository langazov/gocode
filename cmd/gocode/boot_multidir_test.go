package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/modelsdev"
	"github.com/langazov/gocode-go/internal/session"
)

// TestBootStackWithServesTwoDirectories boots two runtimes in one process over
// a shared database, the way gocode acp serves sessions from several working
// directories, and checks each one is rooted in its own directory rather than
// in the process working directory.
func TestBootStackWithServesTwoDirectories(t *testing.T) {
	testCatalog(t)
	ctx := context.Background()

	database, err := db.OpenDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	catalog := modelsdev.New()

	project := func(model string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".gocode"), 0o755); err != nil {
			t.Fatal(err)
		}
		config := `{"model": "` + model + `"}`
		if err := os.WriteFile(filepath.Join(dir, ".gocode", "gocode.json"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	first := project("anthropic/claude-first")
	second := project("anthropic/claude-second")

	boot := func(dir string) *stack {
		t.Helper()
		s, err := bootStackWith(ctx, bootOptions{Directory: dir, Database: database, Catalog: catalog})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	a, b := boot(first), boot(second)

	if a.ModelID != "claude-first" || b.ModelID != "claude-second" {
		t.Fatalf("each stack must read its own project config, got %q and %q", a.ModelID, b.ModelID)
	}
	if a.Workdir() != first || b.Workdir() != second {
		t.Fatalf("workdirs: got %q and %q", a.Workdir(), b.Workdir())
	}
	if a.Database != database || b.Database != database {
		t.Fatal("stacks must share the borrowed database handle")
	}

	// Sessions created through either stack land in the shared database.
	sa, err := a.Service.Create(ctx, sessionCreate(first))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.Service.Get(ctx, sa.ID); err != nil || got == nil || got.Directory != first {
		t.Fatalf("session from stack A not visible through stack B: %v %+v", err, got)
	}

	// Closing a borrowed-database stack leaves the handle usable.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Service.Get(ctx, sa.ID); err != nil {
		t.Fatalf("shared database closed by a borrowing stack: %v", err)
	}
}

func sessionCreate(dir string) session.CreateInput {
	return session.CreateInput{Directory: dir}
}
