//go:build windows

package flock

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// ErrHeld reports that another holder keeps the lock (a non-blocking
// attempt that found it taken).
var ErrHeld = errors.New("held by another holder")

// Lock takes an exclusive lock at path, creating the file if needed.
// The lock is released when the returned closer is closed.
//
// Windows byte-range locks are mandatory rather than advisory, which is fine
// here: every caller locks a dedicated ".lock" file that nothing reads.
func Lock(path string) (io.Closer, error) {
	return lock(path, 0)
}

// TryLock is Lock without blocking: it fails with [ErrHeld] when another
// holder has the lock.
func TryLock(path string) (io.Closer, error) {
	return lock(path, windows.LOCKFILE_FAIL_IMMEDIATELY)
}

func lock(path string, flags uint32) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	// Locks are per handle, so a second open in this process conflicts too,
	// the same as flock(2) on separate opens.
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|flags, 0, 1, 0, new(windows.Overlapped))
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, ErrHeld
		}
		return nil, err
	}
	return f, nil
}
