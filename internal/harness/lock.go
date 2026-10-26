package harness

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/flock"
)

// A run directory is shared by every process that touches the task: a CLI
// run and one started over the HTTP API write the same base rollouts,
// candidate trees and summary. The service's in-memory map cannot see
// another process, so the directory itself carries an advisory lock.

// ErrLocked is returned when another holder (another process, or another
// mode of the same task and k in this one) keeps a run directory's lock.
var ErrLocked = errors.New("another run holds this task; wait for it to finish")

// runLockFile is the lock's name inside the run directory.
const runLockFile = ".lock"

// TryRunLock takes the run directory dir's advisory lock without blocking,
// creating the directory if needed, and fails with [ErrLocked] when another
// process holds it. Closing the returned closer releases the lock.
func TryRunLock(dir string) (io.Closer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lock, err := flock.TryLock(filepath.Join(dir, runLockFile))
	if err != nil {
		if errors.Is(err, flock.ErrHeld) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return lock, nil
}

// RunLockHeld reports whether another process holds dir's run lock, taking
// and releasing it to find out. A directory that does not exist is not held
// — and is left uncreated.
func RunLockHeld(dir string) bool {
	if _, err := os.Stat(dir); err != nil {
		return false
	}
	lock, err := TryRunLock(dir)
	if err != nil {
		return errors.Is(err, ErrLocked)
	}
	_ = lock.Close()
	return false
}

// errNotDir makes lock errors name the directory.
func errNotDir(dir string) error {
	return fmt.Errorf("%s is not a directory", dir)
}
