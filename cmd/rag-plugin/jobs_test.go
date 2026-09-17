package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/rag"
)

// blockingRun returns a runFunc that blocks until release is closed, then
// returns result/err. It also reports one progress call immediately, so
// tests can observe a job actually reached "running" before racing ahead.
func blockingRun(release <-chan struct{}, result rag.IndexSummary, err error) runFunc {
	return func(ctx context.Context, progress func(string, int, int)) (rag.IndexSummary, error) {
		progress("walking", 0, 0)
		select {
		case <-release:
		case <-ctx.Done():
			return rag.IndexSummary{}, ctx.Err()
		}
		return result, err
	}
}

func TestIndexJobsStartRunsToCompletion(t *testing.T) {
	jobs := newIndexJobs()
	release := make(chan struct{})
	want := rag.IndexSummary{FilesScanned: 3, ChunksAdded: 5}

	job, coalesced := jobs.start(context.Background(), "", blockingRun(release, want, nil))
	if coalesced {
		t.Fatal("first start should not be coalesced")
	}
	if !job.running() {
		t.Fatal("job should be running immediately after start")
	}

	close(release)
	job.wait(context.Background(), time.Second)

	v := job.view()
	if v.State != jobSucceeded {
		t.Fatalf("state = %q, want %q", v.State, jobSucceeded)
	}
	if v.Summary != want {
		t.Errorf("summary = %+v, want %+v", v.Summary, want)
	}
	if v.Ended.Before(v.Started) {
		t.Errorf("ended (%v) before started (%v)", v.Ended, v.Started)
	}
}

func TestIndexJobsCoalescesSameScope(t *testing.T) {
	jobs := newIndexJobs()
	release := make(chan struct{})
	defer close(release)

	job1, coalesced1 := jobs.start(context.Background(), "src", blockingRun(release, rag.IndexSummary{}, nil))
	if coalesced1 {
		t.Fatal("first start for a scope should not be coalesced")
	}
	job2, coalesced2 := jobs.start(context.Background(), "src", blockingRun(release, rag.IndexSummary{}, nil))
	if !coalesced2 {
		t.Fatal("a second start for the same running scope should be coalesced")
	}
	if job1 != job2 {
		t.Error("coalesced start should return the same job")
	}
}

func TestIndexJobsDoesNotCoalesceDifferentScopes(t *testing.T) {
	jobs := newIndexJobs()
	release := make(chan struct{})
	defer close(release)

	job1, _ := jobs.start(context.Background(), "src", blockingRun(release, rag.IndexSummary{}, nil))
	job2, coalesced := jobs.start(context.Background(), "docs", blockingRun(release, rag.IndexSummary{}, nil))
	if coalesced {
		t.Fatal("different scopes must not coalesce")
	}
	if job1.id == job2.id {
		t.Error("different scopes got the same job id")
	}
}

func TestIndexJobsCoalescesOnlyWhileRunning(t *testing.T) {
	jobs := newIndexJobs()
	release := make(chan struct{})
	job1, _ := jobs.start(context.Background(), "src", blockingRun(release, rag.IndexSummary{}, nil))
	close(release)
	job1.wait(context.Background(), time.Second)

	job2, coalesced := jobs.start(context.Background(), "src", blockingRun(make(chan struct{}), rag.IndexSummary{}, nil))
	if coalesced {
		t.Fatal("a finished job must not coalesce a new request for the same scope")
	}
	if job1.id == job2.id {
		t.Error("expected a fresh job once the previous one for this scope finished")
	}
}

func TestIndexJobsReportsFailure(t *testing.T) {
	jobs := newIndexJobs()
	release := make(chan struct{})
	wantErr := errors.New("embed: rate limited")

	job, _ := jobs.start(context.Background(), "", blockingRun(release, rag.IndexSummary{}, wantErr))
	close(release)
	job.wait(context.Background(), time.Second)

	v := job.view()
	if v.State != jobFailed {
		t.Fatalf("state = %q, want %q", v.State, jobFailed)
	}
	if v.Err == nil || v.Err.Error() != wantErr.Error() {
		t.Errorf("err = %v, want %v", v.Err, wantErr)
	}
}

func TestIndexJobsCancelStopsARunningJob(t *testing.T) {
	jobs := newIndexJobs()
	job, _ := jobs.start(context.Background(), "", func(ctx context.Context, progress func(string, int, int)) (rag.IndexSummary, error) {
		<-ctx.Done() // a real runFunc is expected to respect ctx, same as chunk.Walk/embed.Client do
		return rag.IndexSummary{}, ctx.Err()
	})

	if !job.requestCancel() {
		t.Fatal("requestCancel on a running job should succeed")
	}
	job.wait(context.Background(), time.Second)

	v := job.view()
	if v.State != jobCancelled {
		t.Fatalf("state = %q, want %q", v.State, jobCancelled)
	}
	if !v.CancelRequested {
		t.Error("view should reflect that cancellation was requested")
	}

	if job.requestCancel() {
		t.Error("requestCancel on an already-finished job should return false")
	}
}

func TestIndexJobsCancelDoesNotOutliveParentContext(t *testing.T) {
	// The parent passed to start (shutdownCtx in production) bounds every
	// job; canceling it must stop a running job the same way requestCancel
	// does, without anyone having called requestCancel.
	jobs := newIndexJobs()
	parent, cancelParent := context.WithCancel(context.Background())
	job, _ := jobs.start(parent, "", func(ctx context.Context, progress func(string, int, int)) (rag.IndexSummary, error) {
		<-ctx.Done()
		return rag.IndexSummary{}, ctx.Err()
	})

	cancelParent()
	job.wait(context.Background(), time.Second)

	v := job.view()
	if v.State != jobFailed {
		t.Fatalf("state = %q, want %q (parent cancellation, not requestCancel)", v.State, jobFailed)
	}
	if v.CancelRequested {
		t.Error("CancelRequested should be false: nobody called requestCancel")
	}
}

func TestIndexJobsWaitReturnsOnTimeoutWithoutStoppingTheJob(t *testing.T) {
	jobs := newIndexJobs()
	release := make(chan struct{})
	defer close(release)
	job, _ := jobs.start(context.Background(), "", blockingRun(release, rag.IndexSummary{}, nil))

	start := time.Now()
	job.wait(context.Background(), 20*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("wait took %v, want it to return promptly at its own timeout", elapsed)
	}
	if !job.running() {
		t.Error("a wait timeout must not cancel the job — only requestCancel or the parent context should")
	}
}

func TestIndexJobsProgressVisibleWhileRunning(t *testing.T) {
	jobs := newIndexJobs()
	started := make(chan struct{})
	release := make(chan struct{})
	job, _ := jobs.start(context.Background(), "", func(ctx context.Context, progress func(string, int, int)) (rag.IndexSummary, error) {
		progress("embedding", 3, 10)
		close(started)
		<-release
		return rag.IndexSummary{}, nil
	})
	<-started

	v := job.view()
	if v.Stage != "embedding" || v.Done != 3 || v.Total != 10 {
		t.Errorf("progress not visible mid-run: %+v", v)
	}
	close(release)
	job.wait(context.Background(), time.Second)
}

func TestIndexJobsGetUnknownID(t *testing.T) {
	jobs := newIndexJobs()
	if _, ok := jobs.get("does-not-exist"); ok {
		t.Error("get should report false for an unknown id")
	}
}

func TestIndexJobsPruneKeepsWithinBound(t *testing.T) {
	// Pruning runs inside start (see pruneLocked's call site), so the job a
	// given start call itself just created is never a candidate for that
	// same prune pass — it's still running at that point. One extra start
	// after the loop is what triggers the prune that accounts for the
	// previous iteration's job finishing.
	jobs := newIndexJobs()
	for i := 0; i < maxFinishedIndexJobs+10; i++ {
		release := make(chan struct{})
		close(release)
		job, _ := jobs.start(context.Background(), "", blockingRun(release, rag.IndexSummary{}, nil))
		job.wait(context.Background(), time.Second)
	}
	release := make(chan struct{})
	close(release)
	last, _ := jobs.start(context.Background(), "", blockingRun(release, rag.IndexSummary{}, nil))
	last.wait(context.Background(), time.Second)

	jobs.mu.Lock()
	kept := len(jobs.order)
	jobs.mu.Unlock()
	if kept > maxFinishedIndexJobs+1 {
		t.Errorf("kept %d finished jobs, want at most %d", kept, maxFinishedIndexJobs+1)
	}
}

func TestIndexJobsConcurrentStartsForSameScopeCoalesceExactlyOnce(t *testing.T) {
	jobs := newIndexJobs()
	release := make(chan struct{})
	defer close(release)

	const n = 20
	ids := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			job, _ := jobs.start(context.Background(), "src", blockingRun(release, rag.IndexSummary{}, nil))
			ids[i] = job.id
		}(i)
	}
	wg.Wait()

	first := ids[0]
	for i, id := range ids {
		if id != first {
			t.Fatalf("goroutine %d got job id %q, want every concurrent start for the same scope to land on %q", i, id, first)
		}
	}
}
