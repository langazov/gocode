package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/global"
)

// A background diagnostic must never reach the terminal: while the TUI is up
// it owns the alternate screen, and a write lands on top of the rendered
// frame. This is the regression where "session ... drain failed: context
// canceled" was painted over the footer.
func TestDrainErrorsGoToTheLogFileNotTheTerminal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	stderr, stdout := captureTerminal(t)
	logDrainError("ses_1", errors.New("provider exploded"))
	if written := stderr() + stdout(); written != "" {
		t.Fatalf("a background diagnostic must not write to the terminal, got %q", written)
	}

	logged := readLog(t, global.Resolve().Log)
	if !strings.Contains(logged, "ses_1") || !strings.Contains(logged, "provider exploded") {
		t.Fatalf("the failure should reach the log file, got %q", logged)
	}
}

// An interrupted turn returns the cancellation the user's own escape produced.
// That is not a failure and must not be reported at all.
func TestDrainCancellationIsNotReported(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	logDrainError("ses_1", context.Canceled)
	logDrainError("ses_1", context.DeadlineExceeded)
	logDrainError("ses_1", nil)

	if logged := readLog(t, global.Resolve().Log); logged != "" {
		t.Fatalf("an interruption is not a drain failure, got %q", logged)
	}
}

func TestLogBackgroundAppends(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	global.LogBackground("first %d", 1)
	global.LogBackground("second %d", 2)

	logged := readLog(t, global.Resolve().Log)
	if !strings.Contains(logged, "first 1") || !strings.Contains(logged, "second 2") {
		t.Fatalf("both lines should be present, got %q", logged)
	}
	if lines := strings.Count(strings.TrimSpace(logged), "\n") + 1; lines != 2 {
		t.Fatalf("expected 2 log lines, got %d in %q", lines, logged)
	}
}

// captureTerminal redirects os.Stderr/os.Stdout for the duration of the test,
// returning readers for whatever was written to each.
func captureTerminal(t *testing.T) (stderr, stdout func() string) {
	t.Helper()
	return capturePipe(t, &os.Stderr), capturePipe(t, &os.Stdout)
}

func capturePipe(t *testing.T, target **os.File) func() string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := *target
	*target = write
	t.Cleanup(func() { *target = original })

	return func() string {
		write.Close()
		buf := make([]byte, 4096)
		n, _ := read.Read(buf)
		read.Close()
		return string(buf[:n])
	}
}

func readLog(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "gocode.log"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
