package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/langazov/gocode-go/internal/installation"
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
	t.Setenv("GOCODE_DB", "")
	t.Setenv("GOCODE_DISABLE_CHANNEL_DB", "")

	installation.Channel = "latest"
	if got := Path(); got != filepath.Join(data, "gocode", "gocode.db") {
		t.Fatalf("unexpected path for latest channel: %s", got)
	}

	installation.Channel = "dev/feature"
	if got := Path(); got != filepath.Join(data, "gocode", "gocode-dev-feature.db") {
		t.Fatalf("unexpected sanitized channel path: %s", got)
	}
	installation.Channel = "local"

	t.Setenv("GOCODE_DB", "custom.db")
	if got := Path(); got != filepath.Join(data, "gocode", "custom.db") {
		t.Fatalf("unexpected GOCODE_DB relative path: %s", got)
	}
	t.Setenv("GOCODE_DB", ":memory:")
	if got := Path(); got != ":memory:" {
		t.Fatalf("expected :memory:, got %s", got)
	}
	absPath := filepath.Join(t.TempDir(), "abs", "path.db")
	t.Setenv("GOCODE_DB", absPath)
	if got := Path(); got != absPath {
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

// QueryRow defers its query to Scan so the whole read can be retried. That
// must not change what a caller sees, so the sql.Row contract is asserted
// directly: a hit scans, a miss is sql.ErrNoRows, and a bad query still errors.
func TestQueryRowMatchesSQLRowSemantics(t *testing.T) {
	ctx := context.Background()
	database, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(ctx, `CREATE TABLE item (id TEXT PRIMARY KEY, n INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO item VALUES ('a', 7)`); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := database.QueryRow(ctx, `SELECT n FROM item WHERE id = ?`, "a").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("expected 7, got %d", n)
	}
	err = database.QueryRow(ctx, `SELECT n FROM item WHERE id = ?`, "missing").Scan(&n)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
	if err := database.QueryRow(ctx, `SELECT nope FROM item`).Scan(&n); err == nil {
		t.Fatal("expected an error for an invalid query")
	}
}

// Retryable is what lets a caller above the storage layer — session.Execution's
// drain — tell "another gocode has the lock, try again" apart from a real
// failure. The extended codes carry the primary one in their low byte.
func TestRetryableCoversTheBusyFamily(t *testing.T) {
	for _, tc := range []struct {
		code int
		want bool
	}{
		{5, true},     // SQLITE_BUSY
		{6, true},     // SQLITE_LOCKED
		{517, true},   // SQLITE_BUSY_SNAPSHOT
		{773, true},   // SQLITE_BUSY_TIMEOUT
		{262, true},   // SQLITE_LOCKED_SHAREDCACHE
		{1, false},    // SQLITE_ERROR
		{11, false},   // SQLITE_CORRUPT
		{2067, false}, // SQLITE_CONSTRAINT_UNIQUE
	} {
		err := fmt.Errorf("wrapped: %w", &codedTestError{code: tc.code})
		if got := Retryable(err); got != tc.want {
			t.Errorf("Retryable(code %d) = %v, want %v", tc.code, got, tc.want)
		}
	}
	if Retryable(nil) {
		t.Error("nil is not retryable")
	}
	if Retryable(errors.New("plain")) {
		t.Error("an error with no result code is not retryable")
	}
}

type codedTestError struct{ code int }

func (e *codedTestError) Error() string { return "database is locked" }
func (e *codedTestError) Code() int     { return e.code }
