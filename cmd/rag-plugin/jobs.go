// Background indexing jobs for the rag_index tool, the same pattern
// go-codegraph's MCP server uses for its own index_repository/
// get_index_status/cancel_index tools: a request starts (or joins) a job
// that keeps running after the tool call returns, so a large first index
// never has to hold the plugin's single-threaded request loop hostage for
// minutes at a time. See runFunc's doc comment for why the job's context is
// rooted at the plugin's shutdown context rather than the request's own.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/langazov/gocode-go/internal/rag"
)

// Job states reported to callers.
const (
	jobRunning   = "running"
	jobSucceeded = "succeeded"
	jobFailed    = "failed"
	jobCancelled = "cancelled"
)

// maxFinishedIndexJobs bounds retained finished jobs. rag-plugin indexes one
// project's worktree (plus, rarely, a few scoped subtrees within it) per
// process, nothing like go-codegraph's many-repositories-per-server case, so
// this stays small — it only needs to outlive a poller's next status check.
const maxFinishedIndexJobs = 20

// indexJobs tracks indexing runs started through rag_index, so a caller can
// index asynchronously, poll progress, and cancel. One registry per plugin
// process.
type indexJobs struct {
	mu    sync.Mutex
	jobs  map[string]*indexJob
	order []string
}

func newIndexJobs() *indexJobs {
	return &indexJobs{jobs: map[string]*indexJob{}}
}

type indexJob struct {
	id, scope string // scope: the IndexOptions.Scope this job covers ("" = whole project)

	mu              sync.Mutex
	state           string
	started, ended  time.Time
	stage           string
	done, total     int
	summary         rag.IndexSummary
	err             error
	cancel          context.CancelFunc
	cancelRequested bool
	finished        chan struct{}
}

// runFunc does the actual indexing work, reporting progress as it goes.
// parent, not the tool call's own request context, bounds how long it can
// run — a client that stops waiting (wait:false, or a timeout) must not
// abort indexing that's already underway, only the request that started it.
type runFunc func(ctx context.Context, progress func(stage string, done, total int)) (rag.IndexSummary, error)

// start launches run under parent (the plugin's shutdown context). A
// running job already covering the same scope is returned instead of
// starting a second one (coalesced=true): two concurrent indexes of the
// same tree would each independently decide the same unembedded chunks
// need embedding and pay for them twice.
func (r *indexJobs) start(parent context.Context, scope string, run runFunc) (job *indexJob, coalesced bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.order {
		j := r.jobs[id]
		if j.scope == scope && j.running() {
			return j, true
		}
	}
	ctx, cancel := context.WithCancel(parent)
	job = &indexJob{
		id: newJobID(), scope: scope, state: jobRunning,
		started: time.Now().UTC(), cancel: cancel, finished: make(chan struct{}),
	}
	r.jobs[job.id] = job
	r.order = append(r.order, job.id)
	r.pruneLocked()
	go func() {
		summary, err := run(ctx, job.progress)
		job.finish(summary, err)
		cancel()
	}()
	return job, false
}

func (r *indexJobs) pruneLocked() {
	finished := 0
	for _, id := range r.order {
		if !r.jobs[id].running() {
			finished++
		}
	}
	kept := r.order[:0]
	for _, id := range r.order {
		if finished > maxFinishedIndexJobs && !r.jobs[id].running() {
			delete(r.jobs, id)
			finished--
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}

// get returns a job by ID.
func (r *indexJobs) get(id string) (*indexJob, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	return j, ok
}

func (j *indexJob) running() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state == jobRunning
}

func (j *indexJob) progress(stage string, done, total int) {
	j.mu.Lock()
	j.stage, j.done, j.total = stage, done, total
	j.mu.Unlock()
}

func (j *indexJob) finish(summary rag.IndexSummary, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.summary, j.err, j.ended = summary, err, time.Now().UTC()
	switch {
	case j.cancelRequested:
		j.state = jobCancelled
	case err != nil:
		j.state = jobFailed
	default:
		j.state = jobSucceeded
	}
	close(j.finished)
}

// requestCancel cancels a running job; false when already finished.
func (j *indexJob) requestCancel() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state != jobRunning {
		return false
	}
	j.cancelRequested = true
	j.cancel()
	return true
}

// wait blocks until the job finishes, timeout elapses, or ctx is done.
func (j *indexJob) wait(ctx context.Context, timeout time.Duration) {
	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-j.finished:
	case <-timer:
	case <-ctx.Done():
	}
}

// indexJobView is an immutable copy of a job's state.
type indexJobView struct {
	ID, State, Scope string
	Started, Ended   time.Time
	Stage            string
	Done, Total      int
	Summary          rag.IndexSummary
	Err              error
	CancelRequested  bool
}

func (j *indexJob) view() indexJobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	return indexJobView{
		ID: j.id, State: j.state, Scope: j.scope, Started: j.started, Ended: j.ended,
		Stage: j.stage, Done: j.done, Total: j.total,
		Summary: j.summary, Err: j.err, CancelRequested: j.cancelRequested,
	}
}

func newJobID() string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return "idx_" + hex.EncodeToString(b)
}
