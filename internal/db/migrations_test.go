package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMemorySchemaMatchesMigration is the guard the two-path Apply needs: a
// database created fresh (baseSchema) and one upgraded from an install that
// predates the table (the migration) must end up structurally identical.
// Without this, a divergence only shows up as a query that works on new
// installs and fails on old ones.
func TestMemorySchemaMatchesMigration(t *testing.T) {
	ctx := context.Background()

	fresh := openTemp(t)
	freshSQL := schemaSQL(t, fresh, "memory")

	// An install from before the memory table: baseSchema minus the local
	// statements, which is what applyOnly's migration path is handed.
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { legacy.Close() })
	for _, stmt := range baseSchema[:len(baseSchema)-len(memorySchema)] {
		if _, err := legacy.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.Apply(ctx); err != nil {
		t.Fatalf("migrating a pre-memory database: %v", err)
	}

	migratedSQL := schemaSQL(t, legacy, "memory")
	if len(migratedSQL) == 0 {
		t.Fatal("migration created no memory objects")
	}
	if len(freshSQL) != len(migratedSQL) {
		t.Fatalf("fresh has %d memory objects, migrated has %d:\nfresh=%v\nmigrated=%v",
			len(freshSQL), len(migratedSQL), freshSQL, migratedSQL)
	}
	for name, sql := range freshSQL {
		if migratedSQL[name] != sql {
			t.Errorf("%s differs:\nfresh:    %s\nmigrated: %s", name, sql, migratedSQL[name])
		}
	}
}

// TestMemoryMigrationIsIdempotent covers the second Apply on an already
// migrated database — the journal must suppress the replay, or the CREATE
// TABLE fails on every subsequent boot.
func TestMemoryMigrationIsIdempotent(t *testing.T) {
	database := openTemp(t)
	for i := 0; i < 3; i++ {
		if err := database.Apply(context.Background()); err != nil {
			t.Fatalf("apply %d: %v", i+2, err)
		}
	}
}

// schemaSQL returns every sqlite_master entry whose name mentions the given
// prefix, keyed by name: the table and its indexes.
func schemaSQL(t *testing.T, database *DB, prefix string) map[string]string {
	t.Helper()
	rows, err := database.Query(context.Background(),
		`SELECT name, sql FROM sqlite_master WHERE name LIKE ? || '%' AND sql IS NOT NULL`, prefix)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, sql string
		if err := rows.Scan(&name, &sql); err != nil {
			t.Fatal(err)
		}
		out[name] = sql
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
