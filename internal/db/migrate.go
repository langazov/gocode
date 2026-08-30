package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Migration struct {
	ID string
	Up func(ctx context.Context, tx *sql.Tx) error
}

// migrations is the ordered migration registry. Porting the full TypeScript
// list (for upgrades of existing installs) happens incrementally; fresh
// installs get baseSchema directly and mark every registered migration done.
var migrations []Migration

// RegisterMigration appends a migration; it panics on out-of-order or
// duplicate IDs to keep the journal deterministic.
func RegisterMigration(m Migration) {
	if len(migrations) > 0 && migrations[len(migrations)-1].ID >= m.ID {
		panic(fmt.Sprintf("db: migration %s out of order", m.ID))
	}
	migrations = append(migrations, m)
}

// Apply mirrors DatabaseMigration.apply: fresh databases get the generated
// schema plus a fully seeded journal; existing databases replay pending
// migrations.
func (d *DB) Apply(ctx context.Context) error {
	tables, err := d.tables(ctx)
	if err != nil {
		return err
	}
	for _, table := range tables {
		if table == "session" {
			return d.applyOnly(ctx, migrations)
		}
	}
	if len(tables) > 0 {
		return errors.New("Database is not empty and has no session table")
	}
	return d.Transaction(ctx, func(tx *sql.Tx) error {
		for _, stmt := range baseSchema {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("db: schema: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`CREATE TABLE migration (id TEXT PRIMARY KEY, time_completed INTEGER NOT NULL)`); err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		for _, migration := range migrations {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO migration (id, time_completed) VALUES (?, ?)`, migration.ID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) applyOnly(ctx context.Context, input []Migration) error {
	if _, err := d.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS migration (id TEXT PRIMARY KEY, time_completed INTEGER NOT NULL)`); err != nil {
		return err
	}
	completed, err := d.completedMigrations(ctx)
	if err != nil {
		return err
	}
	if len(completed) == 0 {
		if err := d.seedLegacyJournal(ctx, input); err != nil {
			return err
		}
		completed, err = d.completedMigrations(ctx)
		if err != nil {
			return err
		}
	}
	for _, migration := range input {
		if completed[migration.ID] {
			continue
		}
		err := d.Transaction(ctx, func(tx *sql.Tx) error {
			if err := migration.Up(ctx, tx); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO migration (id, time_completed) VALUES (?, ?)`,
				migration.ID, time.Now().UnixMilli())
			return err
		})
		if err != nil {
			return fmt.Errorf("db: migration %s: %w", migration.ID, err)
		}
	}
	return nil
}

func (d *DB) completedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := d.Query(ctx, `SELECT id FROM migration`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	completed := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		completed[id] = true
	}
	return completed, rows.Err()
}

// seedLegacyJournal imports journals written by Drizzle so TypeScript-era
// migrations are not replayed, matching the two legacy table shapes.
func (d *DB) seedLegacyJournal(ctx context.Context, input []Migration) error {
	exists, err := d.hasTable(ctx, "__drizzle_migrations")
	if err != nil || !exists {
		return err
	}
	named, err := d.tableHasColumn(ctx, "__drizzle_migrations", "name")
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if named {
		_, err := d.Exec(ctx, `
			INSERT OR IGNORE INTO migration (id, time_completed)
			SELECT name, ? FROM __drizzle_migrations WHERE name IS NOT NULL`, now)
		return err
	}
	rows, err := d.Query(ctx, `
		SELECT created_at, strftime('%Y%m%d%H%M%S', created_at / 1000, 'unixepoch') AS prefix
		FROM __drizzle_migrations WHERE created_at IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type entry struct {
		createdAt int64
		prefix    string
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.createdAt, &e.prefix); err != nil {
			return err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range entries {
		var match *Migration
		for i := range input {
			if strings.HasPrefix(input[i].ID, e.prefix+"_") {
				match = &input[i]
				break
			}
		}
		if match == nil {
			return fmt.Errorf("Legacy migration timestamp %d does not match any known migration", e.createdAt)
		}
		if _, err := d.Exec(ctx,
			`INSERT OR IGNORE INTO migration (id, time_completed) VALUES (?, ?)`, match.ID, now); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) tableHasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := d.Query(ctx, fmt.Sprintf(`SELECT name FROM pragma_table_info('%s')`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
