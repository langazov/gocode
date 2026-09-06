package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/db"
)

// TestConcurrentWritesAcrossHandles covers what the write semaphore cannot:
// two independent connection pools on one database file, which is what a
// second gocode process is. Serializing writers inside one process says
// nothing about a writer in another one, so a read-then-write transaction can
// have its WAL snapshot invalidated between the read and the write and come
// back SQLITE_BUSY_SNAPSHOT (517) — a code busy_timeout deliberately does not
// wait out, because only a rollback and retry can refresh a stale snapshot.
//
// The shape here is Bus.commitDurable's: SELECT the aggregate's sequence, then
// INSERT at seq+1. That is the transaction that lost a tool call's settlement
// event in ses_f8d209148ffe1S8iDKNJ4tmW2i, leaving the call pending forever.
func TestConcurrentWritesAcrossHandles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	first, err := db.OpenAndMigrate(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	if _, err := first.Exec(ctx,
		`CREATE TABLE seq (aggregate TEXT PRIMARY KEY, n INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Exec(ctx, `INSERT INTO seq (aggregate, n) VALUES ('a', 0)`); err != nil {
		t.Fatal(err)
	}

	const perHandle = 60
	var wg sync.WaitGroup
	errs := make(chan error, 2*perHandle)

	for _, handle := range []*db.DB{first, second} {
		wg.Add(1)
		go func(handle *db.DB) {
			defer wg.Done()
			for range perHandle {
				err := handle.Transaction(ctx, func(tx *sql.Tx) error {
					// Read first, exactly as commitDurable does: this is what
					// takes the snapshot that the other handle can invalidate.
					var n int
					if err := tx.QueryRowContext(ctx,
						`SELECT n FROM seq WHERE aggregate = 'a'`).Scan(&n); err != nil {
						return err
					}
					_, err := tx.ExecContext(ctx,
						`UPDATE seq SET n = ? WHERE aggregate = 'a'`, n+1)
					return err
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}(handle)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("cross-process write contention leaked to the caller: %v", err)
	}
}

// TestConcurrentWritesAndReads is the phase 0 acceptance check from
// MULTI_AGENTS.md: concurrent writers must serialize without SQLITE_BUSY, and
// readers must make progress while writes are in flight rather than queueing
// behind them on a single connection.
func TestConcurrentWritesAndReads(t *testing.T) {
	ctx := context.Background()
	database, err := db.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.Exec(ctx,
		`CREATE TABLE conc (id TEXT PRIMARY KEY, n INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	const writers = 8
	const perWriter = 25

	var wg sync.WaitGroup
	errs := make(chan error, writers+2)

	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				id := fmt.Sprintf("w%d-%d", w, i)
				if _, err := database.Exec(ctx,
					`INSERT INTO conc (id, n) VALUES (?, ?)`, id, i); err != nil {
					errs <- fmt.Errorf("writer %d: %w", w, err)
					return
				}
			}
		}(w)
	}

	// A reader looping for the duration of the writes. On a single-connection
	// pool every one of these blocks behind a write transaction.
	done := make(chan struct{})
	var reads int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			var count int
			if err := database.QueryRow(ctx, `SELECT COUNT(*) FROM conc`).Scan(&count); err != nil {
				errs <- fmt.Errorf("reader: %w", err)
				return
			}
			reads++
		}
	}()

	waitWriters := make(chan struct{})
	go func() {
		defer close(waitWriters)
		for range time.Tick(time.Millisecond) {
			var count int
			if database.QueryRow(ctx, `SELECT COUNT(*) FROM conc`).Scan(&count) == nil &&
				count == writers*perWriter {
				return
			}
		}
	}()

	select {
	case <-waitWriters:
	case <-time.After(30 * time.Second):
		t.Fatal("writers did not finish within 30s")
	}
	close(done)
	wg.Wait()
	close(errs)

	for err := range errs {
		if strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked") {
			t.Fatalf("write contention leaked to the caller: %v", err)
		}
		t.Fatalf("unexpected error: %v", err)
	}

	var total int
	if err := database.QueryRow(ctx, `SELECT COUNT(*) FROM conc`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != writers*perWriter {
		t.Fatalf("rows = %d, want %d", total, writers*perWriter)
	}
	if reads == 0 {
		t.Fatal("reader made no progress while writes were in flight")
	}
	t.Logf("%d rows written by %d writers; reader completed %d reads concurrently", total, writers, reads)
}
