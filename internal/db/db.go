package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DB wraps the SQLite connection. The TypeScript runtime uses a single
// bun:sqlite connection, so the pool is capped at one to keep PRAGMAs and
// write serialization identical.
type DB struct {
	sql *sql.DB
}

func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." && path != ":memory:" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	db := &DB{sql: sqlDB}
	if err := db.configure(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func (d *DB) configure() error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA cache_size = -64000",
		"PRAGMA foreign_keys = ON",
		"PRAGMA wal_checkpoint(PASSIVE)",
	}
	for _, pragma := range pragmas {
		if _, err := d.sql.Exec(pragma); err != nil {
			return fmt.Errorf("db: %s: %w", pragma, err)
		}
	}
	return nil
}

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.sql.ExecContext(ctx, query, args...)
}

func (d *DB) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.sql.QueryContext(ctx, query, args...)
}

func (d *DB) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return d.sql.QueryRowContext(ctx, query, args...)
}

// Transaction runs fn inside a SQLite transaction, rolling back on error.
func (d *DB) Transaction(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (d *DB) tables(ctx context.Context) ([]string, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func (d *DB) hasTable(ctx context.Context, name string) (bool, error) {
	var found string
	err := d.sql.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
