package sync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"time"
)

// The local half of the loops: notice that a synced file changed on disk
// and push it. Polling mtime+size every couple of seconds rather than
// fsnotify keeps this dependency-free and immune to editor atomic-save
// rename dances (the new file's mtime is what we compare).

// WatchInterval is how often the watcher looks at the files.
const WatchInterval = 2 * time.Second

// snapshot is one observation of the watched files.
type snapshot map[string]string

// watchTargets returns the file paths the watcher cares about, keyed by
// bundle key for the change comparison.
func (m *Manager) watchTargets() map[string]string {
	targets := map[string]string{}
	if path, found := m.Paths.GlobalConfigPath(); found {
		targets[KeyGlobal] = path
	} else {
		// Watch the default path too: creating it is a change worth pushing.
		targets[KeyGlobal] = path
	}
	targets[KeyTheme] = m.Paths.ThemePath()
	return targets
}

// observe hashes the watched files ("" for missing ones).
func (m *Manager) observe() snapshot {
	targets := m.watchTargets()
	out := make(snapshot, len(targets))
	for key, path := range targets {
		data, err := os.ReadFile(path)
		if err != nil {
			out[key] = ""
			continue
		}
		sum := sha256.Sum256(data)
		out[key] = hex.EncodeToString(sum[:])
	}
	return out
}

// WatchLocal runs the push loop until ctx is done. It is the caller's job
// to start it only when sync is enabled and an account exists.
func (m *Manager) WatchLocal(ctx context.Context) {
	last := m.observe()
	ticker := time.NewTicker(WatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if state, err := LoadState(m.StateDir); err != nil || !state.IsEnabled() {
				continue
			}
			current := m.observe()
			changed := false
			for key, hash := range current {
				if last[key] != hash {
					changed = true
					break
				}
			}
			last = current
			if !changed {
				continue
			}
			// A pull just wrote these files; the state hashes match the new
			// content, so this push is a no-op by design (Push compares
			// content hashes before sealing). Pushing anyway is harmless —
			// the optimistic lock keeps the revision honest.
			pushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_ = m.Push(pushCtx)
			cancel()
		}
	}
}
