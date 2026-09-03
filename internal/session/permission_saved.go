package session

import (
	"context"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/id"
	"github.com/langazov/gocode-go/internal/permission"
)

// SavedPermissions persists "always" replies to the permission table, porting
// packages/core/src/permission/saved.ts.
//
// Without it "Allow always" is indistinguishable from "Allow once": the engine
// resolves the pending request either way, but nothing is written, so the next
// identical request asks again. The grant is scoped to the project rather than
// the session, matching the TypeScript store, so approving a directory once
// covers every future session in the same worktree.
type SavedPermissions struct {
	DB *db.DB
	// Directory is the worktree the grants belong to. It is resolved to a
	// project ID lazily, because the engine is constructed before the first
	// session exists to create that row.
	Directory string

	mu        sync.Mutex
	projectID string
	// cached holds the last read of the table. Reads happen on every
	// permission evaluation — several per tool call — and this process is the
	// only writer, so the query result is cached and invalidated on Add
	// rather than re-run each time.
	cached  permission.Ruleset
	loaded  bool
	idGen   func() (string, error)
	nowFunc func() int64
}

var _ permission.SavedStore = (*SavedPermissions)(nil)

// NewSavedPermissions returns a store for grants made in directory.
func NewSavedPermissions(database *db.DB, directory string) *SavedPermissions {
	return &SavedPermissions{DB: database, Directory: directory}
}

func (s *SavedPermissions) newID() (string, error) {
	if s.idGen != nil {
		return s.idGen()
	}
	return id.Ascending(id.KindPermission)
}

func (s *SavedPermissions) now() int64 {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now().UnixMilli()
}

// project resolves and memoizes the project row for Directory. Callers hold
// s.mu.
func (s *SavedPermissions) project(ctx context.Context) (string, error) {
	if s.projectID != "" {
		return s.projectID, nil
	}
	projectID, err := EnsureProject(ctx, s.DB, s.Directory)
	if err != nil {
		return "", err
	}
	s.projectID = projectID
	return projectID, nil
}

// Add stores one allow rule per resource. Re-approving something already
// granted is a no-op rather than an error, which is what makes the unique
// index on (project, action, resource) load-bearing: two concurrent tool
// calls can be approved with the same rule.
func (s *SavedPermissions) Add(action string, resources []string) error {
	if len(resources) == 0 {
		return nil
	}
	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()
	projectID, err := s.project(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for _, resource := range resources {
		rowID, err := s.newID()
		if err != nil {
			return err
		}
		if _, err := s.DB.Exec(ctx, `
			INSERT INTO permission (id, project_id, action, resource, time_created, time_updated)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (project_id, action, resource) DO NOTHING`,
			rowID, projectID, action, resource, now, now); err != nil {
			return err
		}
	}
	s.loaded = false
	s.cached = nil
	return nil
}

// List returns every saved grant for the project as an allow rule.
func (s *SavedPermissions) List() (permission.Ruleset, error) {
	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return s.cached, nil
	}
	projectID, err := s.project(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(ctx,
		`SELECT action, resource FROM permission WHERE project_id = ? ORDER BY time_created`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out permission.Ruleset
	for rows.Next() {
		var action, resource string
		if err := rows.Scan(&action, &resource); err != nil {
			return nil, err
		}
		out = append(out, permission.Rule{Action: action, Resource: resource, Effect: permission.Allow})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	s.cached = out
	s.loaded = true
	return out, nil
}

// Forget drops every saved grant for the project, backing a "revoke what I
// approved" affordance.
func (s *SavedPermissions) Forget() error {
	ctx := context.Background()
	s.mu.Lock()
	defer s.mu.Unlock()
	projectID, err := s.project(ctx)
	if err != nil {
		return err
	}
	if _, err := s.DB.Exec(ctx, `DELETE FROM permission WHERE project_id = ?`, projectID); err != nil {
		return err
	}
	s.loaded = false
	s.cached = nil
	return nil
}
