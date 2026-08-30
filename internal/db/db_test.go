package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/anomalyco/opencode-go/internal/installation"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := OpenAndMigrate(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestApplyFreshSchema(t *testing.T) {
	database := openTemp(t)
	ctx := context.Background()
	for _, table := range []string{"session", "project", "credential", "event", "migration", "todo", "workspace"} {
		ok, err := database.hasTable(ctx, table)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("expected table %s", table)
		}
	}
}

func TestApplyIdempotent(t *testing.T) {
	database := openTemp(t)
	if err := database.Apply(context.Background()); err != nil {
		t.Fatalf("second apply should be a no-op: %v", err)
	}
}

func TestApplyRejectsNonEmptyForeignSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(context.Background(), `CREATE TABLE unrelated (id text)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Apply(context.Background()); err == nil {
		t.Fatal("expected error for non-empty database without session table")
	}
}

func TestLegacyDrizzleJournalSeeding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if _, err := database.Exec(ctx, `CREATE TABLE session (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `CREATE TABLE __drizzle_migrations (name text, created_at integer)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx,
		`INSERT INTO __drizzle_migrations (name, created_at) VALUES ('20260127222353_familiar_lady_ursula', 1738000000000)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	completed, err := database.completedMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !completed["20260127222353_familiar_lady_ursula"] {
		t.Fatalf("expected legacy migration seeded, got %v", completed)
	}
}

func TestPathFlagAndChannel(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("OPENCODE_DB", "")
	t.Setenv("OPENCODE_DISABLE_CHANNEL_DB", "")

	installation.Channel = "latest"
	if got := Path(); got != filepath.Join(data, "opencode", "opencode.db") {
		t.Fatalf("unexpected path for latest channel: %s", got)
	}

	installation.Channel = "dev/feature"
	if got := Path(); got != filepath.Join(data, "opencode", "opencode-dev-feature.db") {
		t.Fatalf("unexpected sanitized channel path: %s", got)
	}
	installation.Channel = "local"

	t.Setenv("OPENCODE_DB", "custom.db")
	if got := Path(); got != filepath.Join(data, "opencode", "custom.db") {
		t.Fatalf("unexpected OPENCODE_DB relative path: %s", got)
	}
	t.Setenv("OPENCODE_DB", ":memory:")
	if got := Path(); got != ":memory:" {
		t.Fatalf("expected :memory:, got %s", got)
	}
	t.Setenv("OPENCODE_DB", "/abs/path.db")
	if got := Path(); got != "/abs/path.db" {
		t.Fatalf("expected absolute passthrough, got %s", got)
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "test.db")
	database, err := OpenAndMigrate(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected db file: %v", err)
	}
}
