package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"
)

// maxReaders bounds the connection pool. WAL permits any number of concurrent
// readers alongside a single writer, so the pool exists to keep reads (the
// TUI's timeline fetches, the runner's history projection) from queueing
// behind a write.
func maxReaders() int {
	if n := runtime.NumCPU(); n > 4 {
		if n > 16 {
			return 16
		}
		return n
	}
	return 4
}

// DB wraps the SQLite connection pool.
//
// The TypeScript runtime uses a single bun:sqlite connection. This port needs
// concurrent agents (see MULTI_AGENTS.md phase 0), so instead of capping the
// pool at one it runs WAL with many reader connections and serializes writers
// explicitly through the write semaphore. That preserves SQLite's
// single-writer requirement — and the original's write ordering — without
// letting a write transaction block an unrelated read.
type DB struct {
	sql *sql.DB
	// write admits one writer at a time. Acquired by Exec and Transaction,
	// never by Query/QueryRow. Callers must not nest: no write path may run
	// another Exec or Transaction while holding it.
	write chan struct{}
}

// dsn renders a connection string carrying the PRAGMAs, so every connection
// the pool opens is configured identically. Setting them with a one-off Exec
// would only configure whichever connection happened to serve it.
func dsn(path string) string {
	pragmas := []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"busy_timeout(5000)",
		"cache_size(-64000)",
		"foreign_keys(1)",
	}
	query := make([]string, 0, len(pragmas))
	for _, pragma := range pragmas {
		query = append(query, "_pragma="+url.QueryEscape(pragma))
	}
	if path == ":memory:" {
		return "file::memory:?" + strings.Join(query, "&")
	}
	return "file:" + path + "?" + strings.Join(query, "&")
}

func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." && path != ":memory:" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	sqlDB, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	// An in-memory database is private to its connection, so a pool would
	// hand out connections pointing at different empty databases.
	if path == ":memory:" {
		sqlDB.SetMaxOpenConns(1)
	} else {
		sqlDB.SetMaxOpenConns(maxReaders())
	}
	db := &DB{sql: sqlDB, write: make(chan struct{}, 1)}
	if err := db.configure(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// configure runs the PRAGMAs that are not connection-scoped and therefore
// cannot ride on the DSN. Everything else is applied per connection by dsn().
func (d *DB) configure() error {
	if _, err := d.sql.Exec("PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		return fmt.Errorf("db: wal_checkpoint: %w", err)
	}
	return nil
}

// acquire takes the write semaphore, honoring context cancellation.
func (d *DB) acquire(ctx context.Context) error {
	select {
	case d.write <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *DB) release() { <-d.write }

func (d *DB) Close() error {
	return d.sql.Close()
}

func (d *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := d.acquire(ctx); err != nil {
		return nil, err
	}
	defer d.release()
	return d.sql.ExecContext(ctx, query, args...)
}

func (d *DB) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.sql.QueryContext(ctx, query, args...)
}

func (d *DB) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return d.sql.QueryRowContext(ctx, query, args...)
}

// Transaction runs fn inside a SQLite transaction, rolling back on error.
// Transactions are serialized against each other and against Exec, so a
// SQLITE_BUSY write conflict cannot arise from within this process.
func (d *DB) Transaction(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if err := d.acquire(ctx); err != nil {
		return err
	}
	defer d.release()
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
