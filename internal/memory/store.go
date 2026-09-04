// Package memory persists durable instructions — standing directions the user
// wants honored in every session, not just the one that recorded them.
//
// This has no counterpart in the TypeScript tree; it is a gocode-local
// feature, so there is no upstream source to cite or stay faithful to.
//
// The package deliberately owns storage and rendering only. Injecting
// memories into a turn is the plugin's job (internal/memoryplugin), and
// exposing them to the interface is the HTTP server's, because both of those
// consumers must be able to reach the store without reaching each other.
package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/id"
)

// ScopeGlobal marks a memory that applies in every project. Any other scope
// value is a project id, which in this port is the project's worktree path
// (see session.EnsureProject).
const ScopeGlobal = "global"

// Origins record who wrote a memory, so the interface can show the user which
// ones they authored and which the agent decided to keep.
const (
	OriginUser  = "user"
	OriginAgent = "agent"
)

// ErrNotFound is returned by Get, Update and Delete for an unknown id.
var ErrNotFound = errors.New("memory: not found")

// Memory is one durable instruction.
type Memory struct {
	ID       string `json:"id"`
	Scope    string `json:"scope"`
	Content  string `json:"content"`
	Category string `json:"category,omitempty"`
	Origin   string `json:"origin"`
	// SessionID names the session that recorded this memory, when one did. It
	// is provenance only and is not a foreign key — a memory outlives the
	// session that created it by design.
	SessionID string `json:"sessionID,omitempty"`
	// Pinned survives the render budget ahead of unpinned memories.
	Pinned bool `json:"pinned"`
	// Disabled keeps a memory in the list but out of the prompt, so a user can
	// silence one without losing what it said.
	Disabled    bool  `json:"disabled"`
	TimeCreated int64 `json:"timeCreated"`
	TimeUpdated int64 `json:"timeUpdated"`
}

// Store reads and writes memories.
type Store struct {
	db *db.DB
}

// New returns a store over an open database.
func New(database *db.DB) *Store { return &Store{db: database} }

// Create records a memory, or revises the existing one with the same content
// in the same scope.
//
// The upsert is the point: a model that re-derives an instruction it already
// saved should refresh it, not duplicate it. Content is trimmed first, so
// trailing whitespace cannot smuggle a duplicate past the unique index.
func (s *Store) Create(ctx context.Context, input Memory) (Memory, error) {
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		return Memory{}, errors.New("memory: content is required")
	}
	if input.Scope == "" {
		return Memory{}, errors.New("memory: scope is required")
	}
	if input.Origin == "" {
		input.Origin = OriginUser
	}
	generated, err := id.Ascending(id.KindMemory)
	if err != nil {
		return Memory{}, err
	}
	if input.ID == "" {
		input.ID = generated
	}
	now := time.Now().UnixMilli()
	input.TimeCreated, input.TimeUpdated = now, now

	// ON CONFLICT keeps the original row's id and creation time so an id
	// already handed to the model (it is printed in the prompt block) stays
	// valid across a re-save.
	err = s.db.Transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO memory
				(id, scope, content, category, origin, session_id, pinned, disabled, time_created, time_updated)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (scope, content) DO UPDATE SET
				category = excluded.category,
				origin = excluded.origin,
				session_id = excluded.session_id,
				disabled = 0,
				time_updated = excluded.time_updated`,
			input.ID, input.Scope, input.Content, nullable(input.Category), input.Origin,
			nullable(input.SessionID), boolToInt(input.Pinned), boolToInt(input.Disabled),
			input.TimeCreated, input.TimeUpdated)
		return err
	})
	if err != nil {
		return Memory{}, err
	}
	// Read back rather than returning the input: on a conflict the surviving
	// row keeps the original id, which the caller needs.
	return s.getBy(ctx, `scope = ? AND content = ?`, input.Scope, input.Content)
}

// Patch is a partial update. A nil field is left alone, which is what lets the
// interface toggle `pinned` without resubmitting the content.
type Patch struct {
	Content  *string `json:"content,omitempty"`
	Scope    *string `json:"scope,omitempty"`
	Category *string `json:"category,omitempty"`
	Pinned   *bool   `json:"pinned,omitempty"`
	Disabled *bool   `json:"disabled,omitempty"`
}

// Update applies a patch and returns the updated memory.
func (s *Store) Update(ctx context.Context, memoryID string, patch Patch) (Memory, error) {
	sets := []string{"time_updated = ?"}
	args := []any{time.Now().UnixMilli()}
	if patch.Content != nil {
		content := strings.TrimSpace(*patch.Content)
		if content == "" {
			return Memory{}, errors.New("memory: content cannot be empty")
		}
		sets = append(sets, "content = ?")
		args = append(args, content)
	}
	if patch.Scope != nil {
		if *patch.Scope == "" {
			return Memory{}, errors.New("memory: scope cannot be empty")
		}
		sets = append(sets, "scope = ?")
		args = append(args, *patch.Scope)
	}
	if patch.Category != nil {
		sets = append(sets, "category = ?")
		args = append(args, nullable(*patch.Category))
	}
	if patch.Pinned != nil {
		sets = append(sets, "pinned = ?")
		args = append(args, boolToInt(*patch.Pinned))
	}
	if patch.Disabled != nil {
		sets = append(sets, "disabled = ?")
		args = append(args, boolToInt(*patch.Disabled))
	}
	args = append(args, memoryID)

	result, err := s.db.Exec(ctx,
		`UPDATE memory SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	if err != nil {
		// A content or scope edit can collide with an existing memory. Say so
		// in terms the caller can act on rather than leaking the index name.
		if isUniqueViolation(err) {
			return Memory{}, fmt.Errorf("memory: an identical memory already exists in that scope")
		}
		return Memory{}, err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return Memory{}, ErrNotFound
	}
	return s.Get(ctx, memoryID)
}

// Delete removes a memory permanently.
func (s *Store) Delete(ctx context.Context, memoryID string) error {
	result, err := s.db.Exec(ctx, `DELETE FROM memory WHERE id = ?`, memoryID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one memory by id.
func (s *Store) Get(ctx context.Context, memoryID string) (Memory, error) {
	return s.getBy(ctx, `id = ?`, memoryID)
}

func (s *Store) getBy(ctx context.Context, where string, args ...any) (Memory, error) {
	row := s.db.QueryRow(ctx, `SELECT `+columns+` FROM memory WHERE `+where+` LIMIT 1`, args...)
	item, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Memory{}, ErrNotFound
	}
	return item, err
}

// ListInput filters a listing.
type ListInput struct {
	// Scopes restricts the result to these scopes. Empty means every scope.
	Scopes []string
	// IncludeDisabled keeps silenced memories in the result. The interface
	// wants them; the prompt does not.
	IncludeDisabled bool
}

// List returns memories in render order: pinned first, then most recently
// updated. The interface and the prompt therefore agree on precedence, so what
// the user sees at the top of the dialog is what survives the budget.
func (s *Store) List(ctx context.Context, input ListInput) ([]Memory, error) {
	query := `SELECT ` + columns + ` FROM memory`
	var conditions []string
	var args []any
	if len(input.Scopes) > 0 {
		conditions = append(conditions, `scope IN (`+placeholders(len(input.Scopes))+`)`)
		for _, scope := range input.Scopes {
			args = append(args, scope)
		}
	}
	if !input.IncludeDisabled {
		conditions = append(conditions, `disabled = 0`)
	}
	if len(conditions) > 0 {
		query += ` WHERE ` + strings.Join(conditions, " AND ")
	}
	query += ` ORDER BY pinned DESC, time_updated DESC, id DESC`

	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		item, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Active returns the memories that apply to a project right now: its own plus
// the global ones, silenced entries excluded. This is what the prompt block is
// built from.
func (s *Store) Active(ctx context.Context, projectID string) ([]Memory, error) {
	scopes := []string{ScopeGlobal}
	if projectID != "" && projectID != ScopeGlobal {
		scopes = append(scopes, projectID)
	}
	return s.List(ctx, ListInput{Scopes: scopes})
}

const columns = `id, scope, content, category, origin, session_id, pinned, disabled, time_created, time_updated`

// scanner is the shared shape of *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanRow(src scanner) (Memory, error) {
	var item Memory
	var category, sessionID sql.NullString
	var pinned, disabled int
	err := src.Scan(&item.ID, &item.Scope, &item.Content, &category, &item.Origin,
		&sessionID, &pinned, &disabled, &item.TimeCreated, &item.TimeUpdated)
	if err != nil {
		return Memory{}, err
	}
	item.Category = category.String
	item.SessionID = sessionID.String
	item.Pinned = pinned != 0
	item.Disabled = disabled != 0
	return item, nil
}

// nullable stores an empty optional string as NULL, so the unique index and
// any future filtering see one representation of "unset" rather than two.
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// isUniqueViolation reports whether err is a unique-constraint failure. The
// pure-Go driver reports these as a message rather than a typed error, so this
// matches on the text SQLite itself produces.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToUpper(err.Error()), "UNIQUE CONSTRAINT FAILED")
}
