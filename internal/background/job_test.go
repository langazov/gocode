package background

import (
	"context"
	"errors"
	"testing"
	"time"
)

func waitClosed(t *testing.T, ch <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(within):
		t.Fatalf("%s did not happen within %v", what, within)
	}
}

func TestJobCompletes(t *testing.T) {
	registry := NewRegistry()
	release := make(chan struct{})
	registry.Start(context.Background(), StartInput{
		ID: "job_1", Type: "task", Title: "work",
		Run: func(ctx context.Context) (string, error) {
			<-release
			return "result", nil
		},
	})
	if info, _ := registry.Get("job_1"); info.Status != StatusRunning {
		t.Fatalf("status = %q, want running", info.Status)
	}
	close(release)
	waitClosed(t, registry.Done("job_1"), 5*time.Second, "job settle")

	info, ok := registry.Get("job_1")
	if !ok || info.Status != StatusCompleted || info.Output != "result" {
		t.Fatalf("unexpected settled job: %+v", info)
	}
}

func TestJobRecordsFailure(t *testing.T) {
	registry := NewRegistry()
	registry.Start(context.Background(), StartInput{
		ID: "job_err", Run: func(ctx context.Context) (string, error) {
			return "", errors.New("boom")
		},
	})
	waitClosed(t, registry.Done("job_err"), 5*time.Second, "job settle")
	info, _ := registry.Get("job_err")
	if info.Status != StatusError || info.Err != "boom" {
		t.Fatalf("unexpected job: %+v", info)
	}
}

// TestPromoteDetachesRunningJob is the phase 3 promotion path: a foreground
// job can be pushed to the background while it runs, unblocking whoever was
// waiting on it without stopping the work.
func TestPromoteDetachesRunningJob(t *testing.T) {
	registry := NewRegistry()
	release := make(chan struct{})
	registry.Start(context.Background(), StartInput{
		ID: "job_p", Type: "task", Title: "long work",
		Metadata: map[string]any{"parentSessionID": "ses_parent"},
		Run: func(ctx context.Context) (string, error) {
			<-release
			return "eventually", nil
		},
	})

	select {
	case <-registry.Promoted("job_p"):
		t.Fatal("job was already promoted before anyone asked")
	case <-time.After(50 * time.Millisecond):
	}

	if !registry.Promote("job_p") {
		t.Fatal("promote reported no running job")
	}
	waitClosed(t, registry.Promoted("job_p"), 5*time.Second, "promotion")

	// The work keeps running and still settles normally.
	close(release)
	waitClosed(t, registry.Done("job_p"), 5*time.Second, "job settle")
	info, _ := registry.Get("job_p")
	if info.Status != StatusCompleted || info.Output != "eventually" {
		t.Fatalf("promoted job did not finish its work: %+v", info)
	}
	if !info.Background {
		t.Fatal("promoted job is not marked as background")
	}
}

func TestPromoteSessionPromotesOnlyItsOwn(t *testing.T) {
	registry := NewRegistry()
	block := make(chan struct{})
	defer close(block)
	for _, spec := range []struct{ id, parent string }{
		{"a", "ses_1"}, {"b", "ses_1"}, {"c", "ses_2"},
	} {
		registry.Start(context.Background(), StartInput{
			ID: spec.id, Metadata: map[string]any{"parentSessionID": spec.parent},
			Run: func(ctx context.Context) (string, error) { <-block; return "", nil },
		})
	}
	if got := registry.PromoteSession("ses_1"); got != 2 {
		t.Fatalf("promoted %d jobs, want 2", got)
	}
	if info, _ := registry.Get("c"); info.Background {
		t.Fatal("a job belonging to another session was promoted")
	}
}

func TestCancelStopsJob(t *testing.T) {
	registry := NewRegistry()
	registry.Start(context.Background(), StartInput{
		ID: "job_c", Run: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	})
	registry.Cancel("job_c")
	waitClosed(t, registry.Done("job_c"), 5*time.Second, "cancelled job settle")
	info, _ := registry.Get("job_c")
	if info.Status != StatusCancelled {
		t.Fatalf("status = %q, want cancelled", info.Status)
	}
}

// TestStartIsIdempotentForRunningJob covers the resume path: re-registering a
// running job must not launch the work twice.
func TestStartIsIdempotentForRunningJob(t *testing.T) {
	registry := NewRegistry()
	block := make(chan struct{})
	defer close(block)
	runs := make(chan struct{}, 4)
	start := func() {
		registry.Start(context.Background(), StartInput{
			ID: "job_dup", Run: func(ctx context.Context) (string, error) {
				runs <- struct{}{}
				<-block
				return "", nil
			},
		})
	}
	start()
	start()
	time.Sleep(50 * time.Millisecond)
	if len(runs) != 1 {
		t.Fatalf("Run invoked %d times, want 1", len(runs))
	}
}

// TestJobOutlivesCallerContext is what makes promotion meaningful: the job
// must not die when the tool call that started it returns.
func TestJobOutlivesCallerContext(t *testing.T) {
	registry := NewRegistry()
	callerCtx, cancelCaller := context.WithCancel(context.Background())
	release := make(chan struct{})
	registry.Start(callerCtx, StartInput{
		ID: "job_detached", Background: true,
		Run: func(ctx context.Context) (string, error) {
			select {
			case <-release:
				return "survived", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		},
	})
	cancelCaller() // the originating tool call returns

	select {
	case <-registry.Done("job_detached"):
		t.Fatal("job died with its caller's context")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	waitClosed(t, registry.Done("job_detached"), 5*time.Second, "job settle")
	if info, _ := registry.Get("job_detached"); info.Output != "survived" {
		t.Fatalf("unexpected result: %+v", info)
	}
}
