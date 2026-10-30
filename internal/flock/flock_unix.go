//go:build !windows

package flock

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// ErrHeld reports that another holder keeps the lock (a non-blocking
// attempt that found it taken).
var ErrHeld = errors.New("held by another holder")

// Lock takes an exclusive advisory lock at path, creating the file if needed.
// The lock is released when the returned closer is closed.
func Lock(path string) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// TryLock is Lock without blocking: it fails with [ErrHeld] when another
// process holds the lock.
func TryLock(path string) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return f, nil
}
