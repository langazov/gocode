package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/db"
)

// importCommand mirrors ImportCommand in cli/cmd/import.ts ("import <file>").
// It round-trips the Go-native export shape written by exportCommand
// (session info + raw stored message rows). Importing a TS opencode export
// (the richer v1 message/part JSON) or a share URL is not supported yet —
// that needs the full v1-to-Go message schema translation the export side
// also skips (see exportCommand's doc comment).
func importCommand() *clix.Command {
	return &clix.Command{
		Name:        "import",
		Describe:    "import session data from JSON file or URL",
		Positionals: []clix.Positional{{Name: "file", Required: true, Describe: "path to JSON file or share URL"}},
		Run:         runImport,
	}
}

func runImport(a *clix.Args) error {
	file := a.Pos["file"]
	if len(file) > 4 && (file[:4] == "http") {
		return notImplemented("opencode import <url> (share import)")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("File not found: %s", file)
	}
	var data exportData
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("Invalid JSON in %s: %w", file, err)
	}

	ctx := context.Background()
	database, err := db.OpenDefault(ctx)
	if err != nil {
		return err
	}
	defer database.Close()

	projectID, err := ensureImportProject(ctx, database, data.Info.Directory)
	if err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	if _, err := database.Exec(ctx, `
		INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
		VALUES (?, ?, ?, ?, ?, '1', ?, ?)
		ON CONFLICT(id) DO UPDATE SET project_id=excluded.project_id, directory=excluded.directory, title=excluded.title`,
		data.Info.ID, projectID, data.Info.ID, data.Info.Directory, data.Info.Title, now, now); err != nil {
		return err
	}

	for _, msg := range data.Messages {
		if _, err := database.Exec(ctx, `
			INSERT INTO session_message (id, session_id, type, seq, data, time_created, time_updated)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO NOTHING`,
			msg.ID, data.Info.ID, msg.Type, msg.Seq, string(msg.Data), msg.TimeCreated, now); err != nil {
			return err
		}
	}

	fmt.Printf("Imported session: %s\n", data.Info.ID)
	return nil
}

// ensureImportProject looks up a project by worktree/directory, creating one
// if none exists, mirroring Service.ensureProject's adoption semantics.
func ensureImportProject(ctx context.Context, database *db.DB, directory string) (string, error) {
	var id string
	row := database.QueryRow(ctx, `SELECT id FROM project WHERE worktree = ? LIMIT 1`, directory)
	if err := row.Scan(&id); err == nil {
		return id, nil
	}
	id = "prj_" + fmt.Sprint(time.Now().UnixNano())
	now := time.Now().UnixMilli()
	if _, err := database.Exec(ctx, `
		INSERT INTO project (id, worktree, sandboxes, time_created, time_updated)
		VALUES (?, ?, '[]', ?, ?)`, id, directory, now, now); err != nil {
		return "", err
	}
	return id, nil
}
