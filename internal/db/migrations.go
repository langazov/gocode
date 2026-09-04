package db

import (
	"context"
	"database/sql"
)

// The gocode-local migration registry.
//
// [DB.Apply] has two paths and they do not overlap: a fresh database gets
// baseSchema and then has every registered migration stamped as already
// completed, while an existing one replays only the pending migrations. So a
// new table needs an entry in both lists, and the two must agree exactly.
//
// Splicing the same []string into baseSchema and running it from the
// migration is what makes them agree by construction rather than by review.

// memorySchema is the durable-memory table: standing instructions that
// outlive the session that recorded them.
//
// Two omissions are deliberate, not oversights:
//
//   - No foreign key on session_id. Every other session-keyed table in
//     baseSchema cascades on delete, which is right for session state and
//     exactly wrong here — deleting the session that happened to record a
//     memory must not delete the memory. The column is provenance only.
//   - No foreign key on scope. It holds either a project id or the literal
//     "global", and no one column can reference a table for half its values.
//
// The unique index on (scope, content) is what makes a write idempotent: a
// model re-saving an instruction it already saved updates that row instead of
// growing a pile of near-duplicates the user then has to prune by hand.
var memorySchema = []string{
	`CREATE TABLE memory (
		id text PRIMARY KEY,
		scope text NOT NULL,
		content text NOT NULL,
		category text,
		origin text NOT NULL,
		session_id text,
		pinned integer DEFAULT 0 NOT NULL,
		disabled integer DEFAULT 0 NOT NULL,
		time_created integer NOT NULL,
		time_updated integer NOT NULL
	)`,
	`CREATE INDEX memory_scope_idx ON memory (scope, disabled, pinned, time_updated)`,
	`CREATE UNIQUE INDEX memory_scope_content_idx ON memory (scope, content)`,
}

func init() {
	RegisterMigration(Migration{
		ID: "20260904000000_memory",
		Up: func(ctx context.Context, tx *sql.Tx) error {
			for _, stmt := range memorySchema {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return err
				}
			}
			return nil
		},
	})
}
