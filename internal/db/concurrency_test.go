package db_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
)

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
