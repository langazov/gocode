package harness

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestRunLockBlocksASecondHolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "answer", "k1")
	lock, err := TryRunLock(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer lock.Close()
	if _, err := TryRunLock(dir); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock = %v, want ErrLocked", err)
	}
	if !RunLockHeld(dir) {
		t.Fatal("RunLockHeld denied a held lock")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if RunLockHeld(filepath.Join(t.TempDir(), "other", "k1")) {
		t.Fatal("RunLockHeld claimed an untouched directory")
	}
}
