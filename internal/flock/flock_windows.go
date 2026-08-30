//go:build windows

package flock

import (
	"io"
	"os"
)

// Lock is a no-op on Windows; cache writes rely on atomic rename.
func Lock(path string) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return f, nil
}
