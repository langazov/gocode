package session

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDeleteUnderRemovesOnlyThatTree(t *testing.T) {
	_, database := setup(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "runs", "task")
	sibling := root + "-other" // shares the prefix as a string, not as a path
	for _, dir := range []string{root, filepath.Join(root, "k1", "base", "workspaces", "base1"), sibling} {
		projectID, err := EnsureProject(ctx, database, dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(ctx, `
			INSERT INTO session (id, project_id, slug, directory, title, version, time_created, time_updated)
			VALUES (?, ?, 's', ?, 't', '1', 0, 0)`, "ses_"+filepath.Base(dir), projectID, dir); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := DeleteUnder(ctx, database, root)
	if err != nil || deleted != 2 {
		t.Fatalf("deleted %d, %v; want the two sessions under %s", deleted, err, root)
	}
	var sessions, projects int
	database.QueryRow(ctx, `SELECT count(*) FROM session WHERE directory = ?`, sibling).Scan(&sessions)
	database.QueryRow(ctx, `SELECT count(*) FROM project WHERE worktree = ?`, sibling).Scan(&projects)
	if sessions != 1 || projects != 1 {
		t.Fatalf("the sibling directory lost its session or project: %d %d", sessions, projects)
	}
	database.QueryRow(ctx, `SELECT count(*) FROM session WHERE id = 'ses_1'`).Scan(&sessions)
	if sessions != 1 {
		t.Fatal("an unrelated session was deleted")
	}
	if _, err := DeleteUnder(ctx, database, "/"); err == nil {
		t.Fatal("deleting under the root was allowed")
	}
}
