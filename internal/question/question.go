// Package question implements the ask/reply loop behind the question tool:
// the model asks the user something mid-run, the run blocks, and a client
// answers over HTTP.
//
// Ports packages/core/src/question.ts. Structurally it mirrors
// internal/permission: a pending map keyed by request ID, each entry parking
// its caller on a channel.
package question

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/langazov/gocode-go/internal/id"
)

// ErrRejected reports a question the user declined to answer.
var ErrRejected = errors.New("question: rejected")

// ErrNotFound reports a reply for a question that is not pending.
var ErrNotFound = errors.New("question: request not found")

// Option is one selectable choice.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Prompt is a single question put to the user.
type Prompt struct {
	Question string   `json:"question"`
	Header   string   `json:"header"`
	Options  []Option `json:"options"`
	// Multiple allows selecting more than one option.
	Multiple bool `json:"multiple,omitempty"`
}

// Source ties a question back to the tool call that asked it.
type Source struct {
	MessageID string `json:"messageID,omitempty"`
	CallID    string `json:"callID,omitempty"`
}

// Request is a pending ask.
type Request struct {
	ID        string   `json:"id"`
	SessionID string   `json:"sessionID"`
	Questions []Prompt `json:"questions"`
	Source    *Source  `json:"source,omitempty"`
}

// Answer is the set of labels chosen for one question.
type Answer []string

type outcome struct {
	answers []Answer
	err     error
}

type pending struct {
	request Request
	done    chan outcome
}

// Hooks observe the ask/reply lifecycle, the seam for event publication.
type Hooks struct {
	OnAsked    func(request Request)
	OnReplied  func(sessionID, requestID string, answers []Answer)
	OnRejected func(sessionID, requestID string)
}

// Service owns the pending questions for a process.
type Service struct {
	hooks Hooks
	idGen func() string

	mu      sync.Mutex
	pending map[string]*pending
}

func NewService(hooks Hooks, idGen func() string) *Service {
	if idGen == nil {
		idGen = func() string {
			generated, err := id.Ascending(id.KindQuestion)
			if err != nil {
				return "que_unknown"
			}
			return generated
		}
	}
	return &Service{hooks: hooks, idGen: idGen, pending: map[string]*pending{}}
}

// AskInput describes one round of questions.
type AskInput struct {
	SessionID string
	Questions []Prompt
	Source    *Source
}

// Ask registers a question and blocks until it is answered, rejected, or the
// context is cancelled. Several sessions may have questions outstanding at
// once, so nothing here is global state beyond the map.
func (s *Service) Ask(ctx context.Context, input AskInput) ([]Answer, error) {
	if len(input.Questions) == 0 {
		return nil, errors.New("question: at least one question is required")
	}
	request := Request{
		ID:        s.idGen(),
		SessionID: input.SessionID,
		Questions: input.Questions,
		Source:    input.Source,
	}
	item := &pending{request: request, done: make(chan outcome, 1)}
	s.mu.Lock()
	s.pending[request.ID] = item
	s.mu.Unlock()

	if s.hooks.OnAsked != nil {
		s.hooks.OnAsked(request)
	}

	defer func() {
		s.mu.Lock()
		delete(s.pending, request.ID)
		s.mu.Unlock()
	}()

	select {
	case result := <-item.done:
		return result.answers, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Reply answers a pending question.
func (s *Service) Reply(requestID string, answers []Answer) error {
	s.mu.Lock()
	item, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
	}
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if s.hooks.OnReplied != nil {
		s.hooks.OnReplied(item.request.SessionID, requestID, answers)
	}
	item.done <- outcome{answers: answers}
	return nil
}

// Reject declines a pending question, failing the caller.
func (s *Service) Reject(requestID string) error {
	s.mu.Lock()
	item, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
	}
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if s.hooks.OnRejected != nil {
		s.hooks.OnRejected(item.request.SessionID, requestID)
	}
	item.done <- outcome{err: ErrRejected}
	return nil
}

// List returns every pending question, ordered by ID so output is stable.
func (s *Service) List() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, 0, len(s.pending))
	for _, item := range s.pending {
		out = append(out, item.request)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ForSession returns the pending questions belonging to one session.
func (s *Service) ForSession(sessionID string) []Request {
	var out []Request
	for _, request := range s.List() {
		if request.SessionID == sessionID {
			out = append(out, request)
		}
	}
	return out
}

// RejectSession declines every pending question for a session, used when a
// run is interrupted so no caller is left parked forever.
func (s *Service) RejectSession(sessionID string) int {
	rejected := 0
	for _, request := range s.ForSession(sessionID) {
		if s.Reject(request.ID) == nil {
			rejected++
		}
	}
	return rejected
}
