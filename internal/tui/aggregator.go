package tui

import (
	"context"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// DefaultFrame is how often the aggregator emits a snapshot. One frame is the
// coarsest useful granularity for a terminal, and it bounds how much work a
// burst of agent traffic can push onto the main goroutine.
const DefaultFrame = 16 * time.Millisecond

// ToolState is the live status of one tool call within a session.
type ToolState struct {
	CallID string
	Name   string
	Status string // pending | completed | error
}

// SessionNode is the aggregator's live model of one session. Subagent sessions
// get their own node, keyed by their own session ID, so a child's deltas can
// never land in its parent's text.
type SessionNode struct {
	ID    string
	Busy  bool
	Tools map[string]ToolState
	// Text holds streamed assistant text per assistant message ID. It is a
	// liveness hint only: the bus drops events under pressure, so the
	// authoritative timeline always comes from a Messages fetch.
	Text map[string]*strings.Builder
	// Agent is the session's agent as of the last switch seen on the stream,
	// empty until one arrives. Plan mode switches the agent server-side from
	// inside a turn, so this is the only way the interface learns about a
	// change it did not initiate itself.
	Agent string
	// Asks counts question and permission requests raised or settled on this
	// session. It is a change signal, not a quantity: the pending requests
	// themselves are fetched over HTTP, and this only says when to go look.
	// A turn parked on an unanswered ask makes no further events at all, so
	// without it the interface waits out its 10s tick before showing the
	// prompt that is holding the session up.
	Asks int
	// Queued counts prompts admitted to, or promoted out of, this session's
	// inbox. Like Asks it is a change signal rather than a quantity: waiting
	// prompts are fetched over HTTP, and this only says when to go look.
	Queued int
}

func newSessionNode(id string) *SessionNode {
	return &SessionNode{ID: id, Tools: map[string]ToolState{}, Text: map[string]*strings.Builder{}}
}

// clone deep-copies a node so a published snapshot is never mutated by later
// events. Snapshots cross a goroutine boundary; sharing the live maps would be
// a data race.
func (n *SessionNode) clone() *SessionNode {
	out := &SessionNode{
		ID:     n.ID,
		Busy:   n.Busy,
		Agent:  n.Agent,
		Asks:   n.Asks,
		Queued: n.Queued,

		Tools: make(map[string]ToolState, len(n.Tools)),
		Text:  make(map[string]*strings.Builder, len(n.Text)),
	}
	for k, v := range n.Tools {
		out.Tools[k] = v
	}
	for k, v := range n.Text {
		builder := &strings.Builder{}
		builder.WriteString(v.String())
		out.Text[k] = builder
	}
	return out
}

// Snapshot is the only value the main goroutine ever receives from the event
// pipeline. It is immutable once sent.
type Snapshot struct {
	// Sessions holds one node per session seen so far.
	Sessions map[string]*SessionNode
	// Dirty names the sessions whose durable timeline changed since the last
	// snapshot, so the UI knows which (if any) to refetch.
	Dirty map[string]bool
	// Dropped counts events the server-side subscription discarded. Non-zero
	// means streamed text is incomplete — the refetch is what makes it whole.
	Dropped int
}

// tree is the aggregator's mutable state. It lives on the aggregator
// goroutine and is never touched by the main goroutine.
type tree struct {
	sessions map[string]*SessionNode
	dirty    map[string]bool
}

func newTree() *tree {
	return &tree{sessions: map[string]*SessionNode{}, dirty: map[string]bool{}}
}

func (t *tree) node(sessionID string) *SessionNode {
	node, ok := t.sessions[sessionID]
	if !ok {
		node = newSessionNode(sessionID)
		t.sessions[sessionID] = node
	}
	return node
}

// apply folds one event into the tree, reporting whether anything changed.
// Every branch routes by event.Session: this is what keeps a subagent's
// output out of its parent's view.
func (t *tree) apply(e client.Event) bool {
	sessionID := e.Session
	if sessionID == "" {
		return false
	}
	node := t.node(sessionID)
	switch e.Type {
	case "session.next.run.started":
		node.Busy = true
		return true
	case "session.next.run.ended":
		node.Busy = false
		node.Text = map[string]*strings.Builder{}
		t.dirty[sessionID] = true
		return true
	case "session.next.step.started":
		// A step start still implies the turn is running: it is the earliest
		// signal for a client that connected mid-turn and so never saw
		// run.started. Only run.ended clears Busy.
		node.Busy = true
		return true
	case "session.next.text.started", "session.next.text.delta":
		messageID, _ := e.Data["assistantMessageID"].(string)
		if messageID == "" {
			return false
		}
		builder := node.Text[messageID]
		if builder == nil {
			builder = &strings.Builder{}
			node.Text[messageID] = builder
		}
		delta, _ := e.Data["delta"].(string)
		builder.WriteString(delta)
		return true
	case "session.next.tool.called":
		callID, _ := e.Data["callID"].(string)
		name, _ := e.Data["tool"].(string)
		node.Tools[callID] = ToolState{CallID: callID, Name: name, Status: "pending"}
		t.dirty[sessionID] = true
		return true
	case "session.next.tool.success", "session.next.tool.failed":
		callID, _ := e.Data["callID"].(string)
		status := "completed"
		if e.Type == "session.next.tool.failed" {
			status = "error"
		}
		state := node.Tools[callID]
		state.CallID, state.Status = callID, status
		node.Tools[callID] = state
		t.dirty[sessionID] = true
		return true
	case "session.next.question.asked", "session.next.question.settled",
		"session.next.permission.asked":
		node.Asks++
		return true
	case "session.next.agent.switched":
		agent, _ := e.Data["agent"].(string)
		if agent == "" || agent == node.Agent {
			return false
		}
		node.Agent = agent
		// Dirty: the switch is a timeline entry of its own, so the durable
		// history the interface shows is now behind.
		t.dirty[sessionID] = true
		return true
	case "session.next.step.ended", "session.next.step.failed":
		// Deliberately does NOT clear Busy. A turn is many steps — model,
		// tools, model again — and the gaps between them are most of its
		// wall-clock time. Clearing here is what used to make the footer
		// spinner drop out mid-task and come back seconds later. The turn's
		// own end is run.ended, above.
		node.Text = map[string]*strings.Builder{}
		t.dirty[sessionID] = true
		return true
	case "session.next.prompt.admitted":
		// A prompt entered the inbox. It has no timeline message until it is
		// promoted, so the only thing to do is tell the interface to refetch
		// the queue and show it as waiting.
		node.Queued++
		return true
	case "session.next.prompted":
		// Promotion: the prompt leaves the queue and becomes a real message,
		// so both the queue and the timeline are now behind.
		node.Queued++
		node.Text = map[string]*strings.Builder{}
		t.dirty[sessionID] = true
		return true
	case "session.next.text.ended", "session.next.compaction.ended", "todo.updated":
		node.Text = map[string]*strings.Builder{}
		t.dirty[sessionID] = true
		return true
	}
	return false
}

// snapshot publishes an immutable copy and clears the dirty set.
func (t *tree) snapshot(dropped int) Snapshot {
	out := Snapshot{
		Sessions: make(map[string]*SessionNode, len(t.sessions)),
		Dirty:    make(map[string]bool, len(t.dirty)),
		Dropped:  dropped,
	}
	for id, node := range t.sessions {
		out.Sessions[id] = node.clone()
	}
	for id := range t.dirty {
		out.Dirty[id] = true
	}
	t.dirty = map[string]bool{}
	return out
}

// Aggregate folds the server event stream into coalesced snapshots.
//
// It exists so the main goroutine never does per-event work: with N subagents
// running, the raw stream can carry thousands of events a second, and each one
// previously triggered a full timeline refetch. Here, any number of events
// between two frames collapses into one snapshot.
//
// Sends are non-blocking. Dropping a snapshot is free because each one carries
// complete state; dropping a delta would not be, which is why the state lives
// here rather than on the main goroutine.
func Aggregate(ctx context.Context, events <-chan client.Event, out chan<- Snapshot, frame time.Duration) {
	if frame <= 0 {
		frame = DefaultFrame
	}
	state := newTree()
	ticker := time.NewTicker(frame)
	defer ticker.Stop()
	changed := false
	dropped := 0

	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				// Flush whatever is pending before shutting down.
				if changed {
					select {
					case out <- state.snapshot(dropped):
					default:
					}
				}
				return
			}
			if state.apply(e) {
				changed = true
			}
		case <-ticker.C:
			if !changed {
				continue
			}
			select {
			case out <- state.snapshot(dropped):
				changed = false
				dropped = 0
			default:
				// The program is busy; keep accumulating and retry next tick
				// with a newer snapshot.
				dropped++
			}
		}
	}
}

// snapshotMsg delivers one aggregated snapshot to the Bubble Tea program.
type snapshotMsg struct{ snapshot Snapshot }

// sender is the subset of tea.Program the pump needs, so the pump can be
// tested without starting a real program. A tea.Cmd may only return once, so
// the snapshot stream cannot be a command.
type sender interface{ Send(msg tea.Msg) }

// pumpSnapshots is the only goroutine that talks to the program.
func pumpSnapshots(program sender, snapshots <-chan Snapshot, wg *sync.WaitGroup) {
	if wg != nil {
		defer wg.Done()
	}
	for snapshot := range snapshots {
		program.Send(snapshotMsg{snapshot: snapshot})
	}
}
