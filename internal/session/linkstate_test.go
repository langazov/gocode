package session

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// stubLink replaces the interface-table read for the duration of a test.
func stubLink(t *testing.T, up func() bool) {
	t.Helper()
	was := linkIsUp
	linkIsUp = up
	t.Cleanup(func() { linkIsUp = was })
}

// The shortcut that makes watching link state worth anything: a machine that
// rejoins Wi-Fi one second into a long backoff retries immediately instead of
// sitting out the rest of it.
func TestWaitEndsEarlyWhenTheLinkReturns(t *testing.T) {
	restored := make(chan struct{})
	stubLink(t, func() bool {
		select {
		case <-restored:
			return true
		default:
			return false
		}
	})
	// Shorter than the default second, so the test does not spend one.
	pollWas := linkPollIntervalVar
	linkPollIntervalVar = 5 * time.Millisecond
	t.Cleanup(func() { linkPollIntervalVar = pollWas })

	time.AfterFunc(20*time.Millisecond, func() { close(restored) })
	start := time.Now()
	if err := waitBeforeRetry(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s for a link that came back in 20ms", elapsed)
	}
}

// The flap the old "sample once at the start" rule missed: the link is up
// when the backoff begins — the request died of something else — and only
// then does the interface drop and come back. That return is still a reason
// to retry at once.
func TestWaitEndsEarlyWhenTheLinkFlapsMidBackoff(t *testing.T) {
	var state atomic.Int32 // 0 up, 1 down, 2 up again
	state.Store(0)
	stubLink(t, func() bool { return state.Load() != 1 })
	pollWas := linkPollIntervalVar
	linkPollIntervalVar = 5 * time.Millisecond
	t.Cleanup(func() { linkPollIntervalVar = pollWas })

	go func() {
		time.Sleep(20 * time.Millisecond)
		state.Store(1) // the interface goes away mid-backoff
		time.Sleep(20 * time.Millisecond)
		state.Store(2) // ...and comes back
	}()

	start := time.Now()
	if err := waitBeforeRetry(context.Background(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("waited %s through a flap that resolved in 40ms", elapsed)
	}
}

// A link that is up for the whole backoff says nothing new — the fault is
// past the first hop — so the timer governs. Retrying early here would just
// hammer a provider that is down.
func TestWaitRunsItsCourseWhileTheLinkIsUp(t *testing.T) {
	stubLink(t, func() bool { return true })
	start := time.Now()
	if err := waitBeforeRetry(context.Background(), 60*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("returned after %s, want the full backoff", elapsed)
	}
}

// An interrupt ends the wait wherever it is, link poll included.
func TestWaitStopsOnCancellation(t *testing.T) {
	stubLink(t, func() bool { return false })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	if err := waitBeforeRetry(ctx, time.Hour); err == nil {
		t.Fatal("expected the cancellation reported")
	}
}

// linkUp reads the real interface table. The machine running this has one, so
// the assertion is about the read succeeding and the loopback rule holding,
// not about the answer.
func TestLinkUpIgnoresLoopback(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("no interface table to read: %v", err)
	}
	usable := 0
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback == 0 && iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagRunning != 0 {
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok {
					ip := ipNet.IP
					if !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() {
						usable++
					}
				}
			}
		}
	}
	if got := linkUp(); got != (usable > 0) {
		t.Fatalf("linkUp() = %v with %d usable interface addresses", got, usable)
	}
}
