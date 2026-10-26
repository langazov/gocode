//go:build windows

package flock

import (
	"errors"
	"io"
	"os"
)

// ErrHeld is never returned on Windows: locking is advisory-only or a
// no-op, so a non-blocking attempt cannot find the lock taken.
var ErrHeld = errors.New("held by another holder")

// Lock is a no-op on Windows; cache writes rely on atomic rename.
func Lock(path string) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// TryLock is Lock: the same advisory no-op, never [ErrHeld].
func TryLock(path string) (io.Closer, error) { return Lock(path) }
