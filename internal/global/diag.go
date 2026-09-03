package global

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Background diagnostics.
//
// Anything that can fail on a goroutine while the program is running — an
// advisory session drain, a catalog refresh — has nowhere good to complain to.
// stderr is the wrong answer whenever the TUI is up: it owns the alternate
// screen, and a stray write lands *on top of* the rendered frame, corrupting
// it until the next full repaint. That is exactly how a "drain failed: context
// canceled" line ended up painted over the footer.
//
// So background diagnostics go to a file under Paths.Log (which the port
// already resolves and nothing used yet) instead of the terminal. Foreground
// CLI paths keep writing to stderr, where it is the right destination.

var diagMu sync.Mutex

// LogBackground appends one timestamped line to the log file. It is
// best-effort by design: a diagnostic that cannot be written must never take
// down, or block, the work it is reporting on.
func LogBackground(format string, args ...any) {
	line := time.Now().Format(time.RFC3339) + " " + fmt.Sprintf(format, args...) + "\n"

	diagMu.Lock()
	defer diagMu.Unlock()

	dir := Resolve().Log
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, "gocode.log"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.WriteString(line)
}
