package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	// Registers the "sqlite" driver Open uses. The package's own types are
	// not referenced here — isBusy matches the result code through the coded
	// interface instead — so the import is blank.
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
//
// _txlock=immediate makes BeginTx issue BEGIN IMMEDIATE, taking the write lock
// up front rather than starting as a reader and upgrading on the first write.
// The upgrade is what fails across processes: a transaction that reads, then
// writes after another process has committed, is holding a stale WAL snapshot
// and gets SQLITE_BUSY_SNAPSHOT — which busy_timeout does not wait out, since
// waiting cannot refresh a snapshot. Taking the lock at BEGIN means there is no
// upgrade to fail, and ordinary lock contention becomes a busy_timeout wait.
func dsn(path string) string {
	pragmas := []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"busy_timeout(5000)",
		"cache_size(-64000)",
		"foreign_keys(1)",
	}
	query := make([]string, 0, len(pragmas)+1)
	for _, pragma := range pragmas {
		query = append(query, "_pragma="+url.QueryEscape(pragma))
	}
	query = append(query, "_txlock=immediate")
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

// Busy retries bound how long a write waits out contention from another
// process. The write semaphore serializes this process's writers, so anything
// that still comes back busy is a second gocode — the TUI, a `serve` daemon, a
// subagent — holding the write lock, and the only fix is to try again.
//
// Each attempt already waits busy_timeout (5s) inside SQLite before giving up,
// so these retries are for the two cases that return immediately instead:
// SQLITE_BUSY_SNAPSHOT, and a timeout that expired under sustained load.
const maxBusyRetries = 6

// busyBackoff is exponential with jitter. The jitter matters with several
// processes retrying the same write: a fixed delay marches them back into the
// same collision.
func busyBackoff(attempt int) time.Duration {
	base := 5 * time.Millisecond << attempt
	if base > 200*time.Millisecond {
		base = 200 * time.Millisecond
	}
	return base + time.Duration(rand.Int64N(int64(base/2+1)))
}

// Retryable reports whether err is SQLite refusing the call because another
// process holds the lock — a second gocode instance, in practice. It is
// transient by definition: the same call, run again, is expected to succeed,
// which is what lets a caller further up (session.Execution's drain) retry a
// unit of work this layer's own retries could not save.
func Retryable(err error) bool { return isBusy(err) }

// coded is the shape of a driver error carrying a SQLite result code, which
// is all isBusy needs of *sqlite.Error. Matching on the method rather than the
// concrete type keeps the check honest across a wrapped error and lets a test
// hand the retry a busy failure without a real lock to lose.
type coded interface{ Code() int }

// isBusy reports whether err is SQLite refusing a write because someone else
// holds the lock. The extended codes (SQLITE_BUSY_SNAPSHOT 517, _RECOVERY 261,
// _TIMEOUT 773, SQLITE_LOCKED_SHAREDCACHE 262, ...) carry the primary code in
// their low byte, so masking covers the family without naming each one.
func isBusy(err error) bool {
	var codedErr coded
	if !errors.As(err, &codedErr) {
		return false
	}
	switch codedErr.Code() & 0xFF {
	case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
		return true
	}
	return false
}

// retryBusy runs attempt until it succeeds, fails for a reason other than
// contention, or runs out of retries. Every attempt takes the write semaphore
// itself rather than holding it across the backoff, so a local writer that
// would have succeeded is not made to wait behind another process's lock.
func (d *DB) retryBusy(ctx context.Context, attempt func() error) error {
	for try := 0; ; try++ {
		err := attempt()
		if !isBusy(err) || try >= maxBusyRetries {
			return err
		}
		select {
		case <-time.After(busyBackoff(try)):
		case <-ctx.Done():
			return err
		}
	}
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
	var result sql.Result
	err := d.retryBusy(ctx, func() error {
		if err := d.acquire(ctx); err != nil {
			return err
		}
		defer d.release()
		var err error
		result, err = d.sql.ExecContext(ctx, query, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// Query runs a read. Reads never take the write semaphore — WAL admits any
// number of them alongside the single writer — but they are still retried:
// another process opening the database, or checkpointing it, can hand a reader
// SQLITE_BUSY, and a read that fails is how a drain used to die mid-turn.
//
// The retry covers preparing and starting the statement. A busy that surfaces
// later, while the caller iterates the returned rows, cannot be retried
// transparently and reaches the caller as before; QueryRow, which reads its
// single row here, is retried end to end.
func (d *DB) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	var rows *sql.Rows
	err := d.retryBusy(ctx, func() error {
		var err error
		rows, err = d.sql.QueryContext(ctx, query, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// QueryRow reads a single row. Unlike sql.DB.QueryRow it defers the query to
// Scan, so the whole read — query, step, scan — happens inside one retry and
// a busy database is waited out rather than returned.
func (d *DB) QueryRow(ctx context.Context, query string, args ...any) *Row {
	return &Row{db: d, ctx: ctx, query: query, args: args}
}

// Row is the single-row result of QueryRow. It has sql.Row's semantics —
// Scan reports sql.ErrNoRows when the query selected nothing, and every
// caller that treats a *sql.Row as "something with Scan" works unchanged.
type Row struct {
	db    *DB
	ctx   context.Context
	query string
	args  []any
}

// Scan runs the query and reads the first row into dest. A retry re-runs the
// query from the start, so whatever a failed attempt wrote into dest is
// overwritten by the attempt that succeeds.
func (r *Row) Scan(dest ...any) error {
	return r.db.retryBusy(r.ctx, func() error {
		rows, err := r.db.sql.QueryContext(r.ctx, r.query, r.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return err
			}
			return sql.ErrNoRows
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		return rows.Err()
	})
}

// Transaction runs fn inside a SQLite transaction, rolling back on error.
// Transactions are serialized against each other and against Exec, so a
// SQLITE_BUSY write conflict cannot arise from within this process. It can
// arise from another one — a second gocode against the same database file —
// and that is what the busy retry is for.
//
// fn must therefore be safe to run more than once: a retry rolls back and
// starts a fresh transaction, so anything fn read is re-read. Every caller
// derives what it writes from inside the transaction (Bus.commitDurable
// re-reads the aggregate sequence, so a retry allocates against the sequence
// the winning writer left behind), which is what makes the retry correct
// rather than merely convenient.
func (d *DB) Transaction(ctx context.Context, fn func(tx *sql.Tx) error) error {
	return d.retryBusy(ctx, func() error {
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
	})
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
