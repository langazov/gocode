//go:build !windows

package flock

import (
	"io"
	"os"
	"syscall"
)

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
