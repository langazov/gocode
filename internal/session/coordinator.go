package session

import (
	"context"
	"sync"
)

// Coordinator serializes execution per key while allowing different keys to
// run concurrently, porting packages/core/src/session/run-coordinator.ts.
type Coordinator[Key comparable] struct {
	drain func(ctx context.Context, key Key, force bool) error

	// OnStatus, when set, reports the key's execution edges: true when it
	// goes from idle to owned, false when it goes back to idle. It is the
	// port of the onBusy/onIdle pair packages/core's Runner.make takes, and
	// like those it fires on the *turn* boundary, not on the steps within a
	// turn — a turn that calls a tool and comes back for another model step
	// never reports idle in between.
	//
	// Called with the coordinator's lock released, so a slow callback cannot
	// stall other sessions. Must be set before the coordinator is used.
	OnStatus func(key Key, busy bool)

	mu     sync.Mutex
	active map[Key]*entry
}

func (c *Coordinator[Key]) status(key Key, busy bool) {
	if c.OnStatus != nil {
		c.OnStatus(key, busy)
	}
}

type entry struct {
	done        chan struct{}
	err         error
	cancel      context.CancelFunc
	runCtx      context.Context
	parent      context.Context
	pendingWake bool
	stopping    bool
	settled     bool
}

func (e *entry) await() error {
	<-e.done
	return e.err
}

func NewCoordinator[Key comparable](drain func(ctx context.Context, key Key, force bool) error) *Coordinator[Key] {
	return &Coordinator[Key]{
		drain:  drain,
		active: map[Key]*entry{},
	}
}

// Active snapshots the keys with execution owned by this coordinator.
func (c *Coordinator[Key]) Active() []Key {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]Key, 0, len(c.active))
	for key := range c.active {
		keys = append(keys, key)
	}
	return keys
}

// Run starts execution while idle or joins the active execution, returning
// the drain outcome.
func (c *Coordinator[Key]) Run(ctx context.Context, key Key) error {
	for {
		c.mu.Lock()
		current, ok := c.active[key]
		if ok {
			stopping := current.stopping
			c.mu.Unlock()
			if err := current.await(); err != nil && !stopping {
				return err
			}
			if stopping {
				continue
			}
			return nil
		}
		next := newEntry(ctx)
		c.active[key] = next
		c.mu.Unlock()
		c.status(key, true)
		c.launch(ctx, key, next, true)
		return next.await()
	}
}

// Wake registers one coalesced follow-up after newly recorded work.
func (c *Coordinator[Key]) Wake(ctx context.Context, key Key) {
	c.mu.Lock()
	if current, ok := c.active[key]; ok {
		current.pendingWake = true
		c.mu.Unlock()
		return
	}
	next := newEntry(ctx)
	c.active[key] = next
	c.mu.Unlock()
	c.status(key, true)
	c.launch(ctx, key, next, false)
}

// Interrupt stops active execution and waits for its cleanup. Interrupting an
// idle key is a no-op.
func (c *Coordinator[Key]) Interrupt(key Key) {
	c.mu.Lock()
	current, ok := c.active[key]
	if !ok {
		c.mu.Unlock()
		return
	}
	current.stopping = true
	current.pendingWake = false
	cancel := current.cancel
	c.mu.Unlock()
	cancel()
	current.await()
}

func newEntry(ctx context.Context) *entry {
	runCtx, cancel := context.WithCancel(ctx)
	return &entry{
		done:   make(chan struct{}),
		cancel: cancel,
		runCtx: runCtx,
		parent: ctx,
	}
}

func (c *Coordinator[Key]) launch(_ context.Context, key Key, e *entry, force bool) {
	go func() {
		err := c.drain(e.runCtx, key, force)
		c.settle(key, e, err)
	}()
}

func (c *Coordinator[Key]) settle(key Key, e *entry, err error) {
	c.mu.Lock()
	if err == nil && !e.stopping && e.pendingWake {
		e.pendingWake = false
		c.mu.Unlock()
		c.launchIdle(key, e)
		return
	}
	if e.pendingWake {
		successor := newEntry(e.parent)
		c.active[key] = successor
		c.mu.Unlock()
		// Still owned, just by a successor entry: no idle edge, which is the
		// whole point — a queued follow-up continues the same run of work.
		c.launchIdle(key, successor)
	} else {
		delete(c.active, key)
		c.mu.Unlock()
		c.status(key, false)
	}
	e.err = err
	e.settled = true
	close(e.done)
}

func (c *Coordinator[Key]) launchIdle(key Key, e *entry) {
	c.launch(e.parent, key, e, false)
}
