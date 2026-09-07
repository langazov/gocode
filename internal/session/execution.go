package session

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/langazov/gocode-go/internal/db"
)

// SessionRunner drains eligible durable work for one session. It mirrors
// SessionRunner.Interface; explicit runs perform one provider attempt even
// when no work is eligible.
type SessionRunner interface {
	Run(ctx context.Context, input RunInput) error
}

type RunInput struct {
	SessionID string
	Force     bool
}

// SessionLookup resolves whether a session exists before draining.
type SessionLookup interface {
	Exists(ctx context.Context, sessionID string) (bool, error)
}

// Execution routes Session-ID keyed execution to a process-local runner,
// porting session/execution/local.ts. It is process-global and Session-ID
// based; no layer takes a Session ID directly.
type Execution struct {
	coordinator *Coordinator[string]
	// ErrorLogger, when set, observes advisory wake drain failures that would
	// otherwise be discarded, matching the local drain's tapCause logging.
	ErrorLogger func(sessionID string, err error)
	// OnStatus, when set, reports when a session starts running and when it
	// goes idle again. See Coordinator.OnStatus for the boundary it fires on;
	// wire it to PublishRunStatus to put those edges on the event bus.
	OnStatus func(sessionID string, busy bool)
}

// drainRetries bounds how many times a drain is re-entered after losing to
// another process's database lock. The db layer already retries the individual
// statement; this is the outer net for the case that beat it — a second gocode
// instance booting into the same database while a turn is running, which is
// what "starting a second instance stopped the first one's task" was.
//
// A turn is durable, so re-entering the drain is the same recovery a restart
// performs: pending inputs are re-read, tools left mid-flight are failed, and
// the turn continues from what actually committed.
const drainRetries = 3

// drainBackoff spaces the retries far enough apart to outlast another
// process's boot — the migration check and project write a new instance does
// before it settles.
func drainBackoff(attempt int) time.Duration {
	return time.Duration(attempt+1) * 250 * time.Millisecond
}

func NewExecution(lookup SessionLookup, runner SessionRunner) *Execution {
	execution := &Execution{}
	drain := func(ctx context.Context, sessionID string, force bool) error {
		exists, err := lookup.Exists(ctx, sessionID)
		if err != nil {
			return err
		}
		if !exists {
			return notFound(sessionID)
		}
		for attempt := 0; ; attempt++ {
			err = runner.Run(ctx, RunInput{SessionID: sessionID, Force: force})
			if !db.Retryable(err) || attempt >= drainRetries {
				break
			}
			select {
			case <-time.After(drainBackoff(attempt)):
			case <-ctx.Done():
				return err
			}
		}
		if err != nil && execution.ErrorLogger != nil {
			execution.ErrorLogger(sessionID, err)
		}
		return err
	}
	execution.coordinator = NewCoordinator(drain)
	execution.coordinator.OnStatus = func(sessionID string, busy bool) {
		if execution.OnStatus != nil {
			execution.OnStatus(sessionID, busy)
		}
	}
	return execution
}

// Active snapshots session IDs with execution owned by this process.
func (e *Execution) Active() []string {
	return e.coordinator.Active()
}

// Resume starts execution while idle or joins the active execution.
func (e *Execution) Resume(ctx context.Context, sessionID string) error {
	return e.coordinator.Run(ctx, sessionID)
}

// Wake registers newly recorded work. Repeated wakeups coalesce. Wakes are
// advisory background drains, so they detach from the caller's context and run
// to completion rather than being canceled with the request that recorded the
// work.
func (e *Execution) Wake(ctx context.Context, sessionID string) {
	e.coordinator.Wake(context.WithoutCancel(ctx), sessionID)
}

// Interrupt stops active work owned by this process. Idle interruption is a
// no-op.
func (e *Execution) Interrupt(sessionID string) {
	e.coordinator.Interrupt(sessionID)
}

// NoopExecution is the compatibility layer for callers that only need durable
// Session recording, mirroring noopLayer.
func NoopExecution() *Execution {
	return &Execution{coordinator: NewCoordinator(func(ctx context.Context, key string, force bool) error {
		return nil
	})}
}

// DBSessionLookup resolves session existence against the storage layer.
type DBSessionLookup struct {
	DB *db.DB
}

func (l *DBSessionLookup) Exists(ctx context.Context, sessionID string) (bool, error) {
	var id string
	err := l.DB.QueryRow(ctx, `SELECT id FROM session WHERE id = ?`, sessionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
