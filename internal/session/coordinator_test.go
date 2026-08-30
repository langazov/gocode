package session

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func waitFor(t *testing.T, cond func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}

func TestRunSerializesSameKey(t *testing.T) {
	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	var total atomic.Int32
	release := make(chan struct{})
	drain := func(ctx context.Context, key string, force bool) error {
		total.Add(1)
		current := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if current <= old || maxSeen.CompareAndSwap(old, current) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return nil
	}
	coordinator := NewCoordinator(drain)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := coordinator.Run(ctx, "ses_1"); err != nil {
				t.Error(err)
			}
		}()
	}
	waitFor(t, func() bool { return total.Load() == 1 }, "first drain should start")
	close(release)
	wg.Wait()
	if maxSeen.Load() != 1 {
		t.Fatalf("drains for the same key must not overlap, saw %d in flight", maxSeen.Load())
	}
	if total.Load() != 1 {
		t.Fatalf("joiners must not start extra drains, got %d", total.Load())
	}
}

func TestRunPropagatesDrainError(t *testing.T) {
	boom := errors.New("drain failed")
	coordinator := NewCoordinator(func(ctx context.Context, key string, force bool) error {
		return boom
	})
	if err := coordinator.Run(context.Background(), "ses_1"); !errors.Is(err, boom) {
		t.Fatalf("expected drain error, got %v", err)
	}
	if active := coordinator.Active(); len(active) != 0 {
		t.Fatalf("failed key must be removed, got %v", active)
	}
}

func TestDifferentKeysRunConcurrently(t *testing.T) {
	release := make(chan struct{})
	var inFlight atomic.Int32
	var maxSeen atomic.Int32
	drain := func(ctx context.Context, key string, force bool) error {
		current := inFlight.Add(1)
		for {
			old := maxSeen.Load()
			if current <= old || maxSeen.CompareAndSwap(old, current) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return nil
	}
	coordinator := NewCoordinator(drain)
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, key := range []string{"ses_1", "ses_2"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			coordinator.Run(ctx, key)
		}(key)
	}
	waitFor(t, func() bool { return maxSeen.Load() == 2 }, "different keys should drain concurrently")
	close(release)
	wg.Wait()
}

func TestWakeWhileIdleStartsDrain(t *testing.T) {
	var forced atomic.Bool
	var started atomic.Bool
	release := make(chan struct{})
	drain := func(ctx context.Context, key string, force bool) error {
		started.Store(true)
		forced.Store(force)
		<-release
		return nil
	}
	coordinator := NewCoordinator(drain)
	coordinator.Wake(context.Background(), "ses_1")
	waitFor(t, started.Load, "wake should start a drain")
	if forced.Load() {
		t.Fatal("wake drain must not be forced")
	}
	close(release)
	waitFor(t, func() bool { return len(coordinator.Active()) == 0 }, "key should settle")
}

func TestWakeCoalescesDuringActiveDrain(t *testing.T) {
	var total atomic.Int32
	release := make(chan struct{})
	done := make(chan struct{})
	drain := func(ctx context.Context, key string, force bool) error {
		n := total.Add(1)
		if n == 1 {
			<-release
		}
		return nil
	}
	coordinator := NewCoordinator(drain)
	go func() {
		coordinator.Run(context.Background(), "ses_1")
		close(done)
	}()
	waitFor(t, func() bool { return total.Load() == 1 }, "run drain should start")
	coordinator.Wake(context.Background(), "ses_1")
	coordinator.Wake(context.Background(), "ses_1")
	coordinator.Wake(context.Background(), "ses_1")
	close(release)
	<-done
	waitFor(t, func() bool { return len(coordinator.Active()) == 0 }, "coalesced wakes should settle")
	if got := total.Load(); got != 2 {
		t.Fatalf("coalesced wakes should trigger exactly one follow-up drain, got %d", got)
	}
}

func TestForceFlagOnRun(t *testing.T) {
	var forced atomic.Bool
	drain := func(ctx context.Context, key string, force bool) error {
		forced.Store(force)
		return nil
	}
	coordinator := NewCoordinator(drain)
	if err := coordinator.Run(context.Background(), "ses_1"); err != nil {
		t.Fatal(err)
	}
	if !forced.Load() {
		t.Fatal("explicit run must force a provider attempt")
	}
}

func TestInterruptStopsActiveDrain(t *testing.T) {
	started := make(chan struct{})
	var sawCancel atomic.Bool
	drain := func(ctx context.Context, key string, force bool) error {
		close(started)
		<-ctx.Done()
		sawCancel.Store(true)
		return ctx.Err()
	}
	coordinator := NewCoordinator(drain)
	go coordinator.Run(context.Background(), "ses_1")
	<-started
	coordinator.Interrupt("ses_1")
	waitFor(t, sawCancel.Load, "interrupt should cancel the drain context")
	waitFor(t, func() bool { return len(coordinator.Active()) == 0 }, "interrupted key should settle")
}

func TestInterruptIdleIsNoop(t *testing.T) {
	coordinator := NewCoordinator(func(ctx context.Context, key string, force bool) error { return nil })
	coordinator.Interrupt("ses_missing")
}

func TestFailedDrainWithPendingWakeStartsSuccessor(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	drain := func(ctx context.Context, key string, force bool) error {
		n := attempts.Add(1)
		if n == 1 {
			<-release
			return errors.New("first attempt failed")
		}
		return nil
	}
	coordinator := NewCoordinator(drain)
	runDone := make(chan error, 1)
	go func() { runDone <- coordinator.Run(context.Background(), "ses_1") }()
	waitFor(t, func() bool { return attempts.Load() == 1 }, "first drain should start")
	coordinator.Wake(context.Background(), "ses_1")
	close(release)
	if err := <-runDone; err == nil {
		t.Fatal("run should report the first drain failure")
	}
	waitFor(t, func() bool { return attempts.Load() == 2 }, "pending wake should start successor drain")
	waitFor(t, func() bool { return len(coordinator.Active()) == 0 }, "successor should settle")
}
