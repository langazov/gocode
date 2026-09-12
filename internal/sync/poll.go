package sync

import (
	"context"
	"math/rand"
	"time"
)

// The remote half of the loops: every 30 seconds (±jitter, backing off on
// failure) check the server's revision and pull when it moved. This is how
// an edit saved on gocoder.org reaches a running gocode.

// PollInterval is the steady-state check cadence.
const PollInterval = 30 * time.Second

// PollMaxBackoff caps the failure backoff.
const PollMaxBackoff = 10 * time.Minute

// PollRemote runs the pull loop until ctx is done.
func (m *Manager) PollRemote(ctx context.Context) {
	delay := PollInterval
	ticker := time.NewTicker(delay)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state, err := LoadState(m.StateDir)
			if err != nil || !state.IsEnabled() {
				continue
			}
			if state.NeedsRelogin {
				continue // paused until the next sign-in re-derives the key
			}
			pullCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err = m.Pull(pullCtx)
			cancel()
			if err != nil {
				// Back off exponentially, reset on the next success.
				delay *= 2
				if delay > PollMaxBackoff {
					delay = PollMaxBackoff
				}
				ticker.Reset(delay)
				continue
			}
			if delay != PollInterval {
				delay = PollInterval
				ticker.Reset(delay + jitter(delay))
			}
		}
	}
}

// jitter returns a small random fraction of d so a fleet of clients does
// not stampede in lockstep.
func jitter(d time.Duration) time.Duration {
	spread := d / 10
	if spread <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(spread)))
}
