// Package background runs detached work — today, subagent sessions launched
// with task(background: true) — and lets a caller either wait for a result or
// hand the job off to run on its own.
//
// It ports the parts of packages/core/src/background-job.ts this port needs,
// with goroutines and channels in place of Effect fibers.
package background

import (
	"context"
	"sort"
	"sync"
	"time"
)

type Status string

const (
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusError     Status = "error"
	StatusCancelled Status = "cancelled"
)

// Info is an immutable snapshot of a job.
type Info struct {
	ID        string
	Type      string
	Title     string
	Status    Status
	Metadata  map[string]any
	StartedAt int64
	// Output is the job's result text, set once Status is completed.
	Output string
	// Err is the failure message, set once Status is error.
	Err string
	// Background reports whether the job was promoted to run detached.
	Background bool
}

// StartInput describes one job to run.
type StartInput struct {
	// ID keys the job. Reusing a running job's ID is a no-op that returns the
	// existing job, which is how a resumed subagent avoids double-starting.
	ID       string
	Type     string
	Title    string
	Metadata map[string]any
	// Run performs the work. It returns the result text or an error.
	Run func(ctx context.Context) (string, error)
	// Background starts the job already detached, skipping the foreground
	// wait entirely.
	Background bool
}

type job struct {
	info   Info
	done   chan struct{} // closed once the job settles
	demote chan struct{} // closed when the job is promoted to background
	cancel context.CancelFunc
	once   sync.Once
}

// Registry owns the process's background jobs.
type Registry struct {
	mu   sync.Mutex
	jobs map[string]*job
}

func NewRegistry() *Registry {
	return &Registry{jobs: map[string]*job{}}
}

// Start launches a job on its own goroutine and returns its initial snapshot.
// If a job with the same ID is already running, the existing one is returned
// and Run is not invoked.
func (r *Registry) Start(ctx context.Context, input StartInput) Info {
	r.mu.Lock()
	if existing, ok := r.jobs[input.ID]; ok && existing.info.Status == StatusRunning {
		info := existing.info
		r.mu.Unlock()
		return info
	}
	// The job outlives the caller's context: a foreground task call that is
	// later promoted must keep running after its tool call returns.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	item := &job{
		info: Info{
			ID:         input.ID,
			Type:       input.Type,
			Title:      input.Title,
			Status:     StatusRunning,
			Metadata:   input.Metadata,
			StartedAt:  time.Now().UnixMilli(),
			Background: input.Background,
		},
		done:   make(chan struct{}),
		demote: make(chan struct{}),
		cancel: cancel,
	}
	if input.Background {
		close(item.demote)
	}
	r.jobs[input.ID] = item
	info := item.info
	r.mu.Unlock()

	go func() {
		output, err := input.Run(runCtx)
		r.settle(input.ID, output, err)
	}()
	return info
}

func (r *Registry) settle(id, output string, err error) {
	r.mu.Lock()
	item, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return
	}
	switch {
	case err != nil && runCancelled(err):
		item.info.Status = StatusCancelled
		item.info.Err = err.Error()
	case err != nil:
		item.info.Status = StatusError
		item.info.Err = err.Error()
	default:
		item.info.Status = StatusCompleted
		item.info.Output = output
	}
	item.cancel()
	r.mu.Unlock()
	item.once.Do(func() { close(item.done) })
}

func runCancelled(err error) bool {
	return err == context.Canceled
}

// Done returns a channel closed when the job settles. A nil channel (unknown
// job) blocks forever, which callers must guard with a Get first.
func (r *Registry) Done(id string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item, ok := r.jobs[id]; ok {
		return item.done
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

// Promoted returns a channel closed when the job is pushed to the background.
func (r *Registry) Promoted(id string) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if item, ok := r.jobs[id]; ok {
		return item.demote
	}
	return make(chan struct{}) // never fires for an unknown job
}

// Promote detaches a running job so its originating tool call can return
// immediately. Returns false if the job is unknown or already settled.
func (r *Registry) Promote(id string) bool {
	r.mu.Lock()
	item, ok := r.jobs[id]
	if !ok || item.info.Status != StatusRunning || item.info.Background {
		r.mu.Unlock()
		return false
	}
	item.info.Background = true
	demote := item.demote
	r.mu.Unlock()
	select {
	case <-demote: // already closed
	default:
		close(demote)
	}
	return true
}

// Cancel stops a running job.
func (r *Registry) Cancel(id string) {
	r.mu.Lock()
	item, ok := r.jobs[id]
	r.mu.Unlock()
	if ok {
		item.cancel()
	}
}

// Get returns a job snapshot.
func (r *Registry) Get(id string) (Info, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.jobs[id]
	if !ok {
		return Info{}, false
	}
	return item.info, true
}

// List returns every job, oldest first.
func (r *Registry) List() []Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Info, 0, len(r.jobs))
	for _, item := range r.jobs {
		out = append(out, item.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

// PromoteSession promotes every running foreground job spawned by a parent
// session, returning how many were promoted. This is the "push my subagents
// to the background" gesture.
func (r *Registry) PromoteSession(parentSessionID string) int {
	r.mu.Lock()
	var ids []string
	for id, item := range r.jobs {
		if item.info.Status != StatusRunning || item.info.Background {
			continue
		}
		if parent, _ := item.info.Metadata["parentSessionID"].(string); parent == parentSessionID {
			ids = append(ids, id)
		}
	}
	r.mu.Unlock()
	promoted := 0
	for _, id := range ids {
		if r.Promote(id) {
			promoted++
		}
	}
	return promoted
}
