package question

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func askInBackground(t *testing.T, service *Service, sessionID string) (<-chan []Answer, <-chan error) {
	t.Helper()
	answers := make(chan []Answer, 1)
	errs := make(chan error, 1)
	go func() {
		got, err := service.Ask(context.Background(), AskInput{
			SessionID: sessionID,
			Questions: []Prompt{{Question: "Pick one", Header: "Choice", Options: []Option{
				{Label: "A", Description: "first"},
				{Label: "B", Description: "second"},
			}}},
		})
		answers <- got
		errs <- err
	}()
	return answers, errs
}

func waitPending(t *testing.T, service *Service) Request {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if pending := service.List(); len(pending) == 1 {
			return pending[0]
		}
		select {
		case <-deadline:
			t.Fatal("question never became pending")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestAskAndReply(t *testing.T) {
	service := NewService(Hooks{}, nil)
	answers, errs := askInBackground(t, service, "ses_1")
	request := waitPending(t, service)

	if err := service.Reply(request.ID, []Answer{{"A"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-answers:
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || len(got[0]) != 1 || got[0][0] != "A" {
			t.Fatalf("answers = %#v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reply did not unblock the ask")
	}
	if len(service.List()) != 0 {
		t.Fatal("answered question is still pending")
	}
}

func TestRejectFailsAsk(t *testing.T) {
	service := NewService(Hooks{}, nil)
	_, errs := askInBackground(t, service, "ses_1")
	request := waitPending(t, service)

	if err := service.Reject(request.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errs:
		if !errors.Is(err, ErrRejected) {
			t.Fatalf("err = %v, want ErrRejected", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reject did not unblock the ask")
	}
}

func TestReplyUnknownRequest(t *testing.T) {
	service := NewService(Hooks{}, nil)
	if err := service.Reply("que_missing", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := service.Reject("que_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAskHonorsContextCancellation(t *testing.T) {
	service := NewService(Hooks{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := service.Ask(ctx, AskInput{
			SessionID: "ses_1",
			Questions: []Prompt{{Question: "q", Header: "h"}},
		})
		errs <- err
	}()
	waitPending(t, service)
	cancel()

	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not unblock the ask")
	}
	if len(service.List()) != 0 {
		t.Fatal("cancelled question is still pending")
	}
}

// TestConcurrentAsksAreIndependent is what makes questions usable alongside
// subagents: several sessions can be waiting at once, and answering one must
// not disturb the others.
func TestConcurrentAsksAreIndependent(t *testing.T) {
	service := NewService(Hooks{}, nil)
	var wg sync.WaitGroup
	results := make(map[string]string)
	var mu sync.Mutex

	for _, sessionID := range []string{"ses_a", "ses_b", "ses_c"} {
		wg.Add(1)
		go func(sessionID string) {
			defer wg.Done()
			got, err := service.Ask(context.Background(), AskInput{
				SessionID: sessionID,
				Questions: []Prompt{{Question: "q", Header: "h"}},
			})
			if err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			results[sessionID] = got[0][0]
		}(sessionID)
	}

	deadline := time.After(5 * time.Second)
	for len(service.List()) < 3 {
		select {
		case <-deadline:
			t.Fatalf("only %d questions became pending", len(service.List()))
		case <-time.After(5 * time.Millisecond):
		}
	}
	for _, request := range service.List() {
		if err := service.Reply(request.ID, []Answer{{request.SessionID}}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	for _, sessionID := range []string{"ses_a", "ses_b", "ses_c"} {
		if results[sessionID] != sessionID {
			t.Fatalf("session %s got answer %q", sessionID, results[sessionID])
		}
	}
}

func TestRejectSession(t *testing.T) {
	service := NewService(Hooks{}, nil)
	_, errsA := askInBackground(t, service, "ses_target")
	_, _ = askInBackground(t, service, "ses_other")

	deadline := time.After(5 * time.Second)
	for len(service.List()) < 2 {
		select {
		case <-deadline:
			t.Fatal("questions never became pending")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if got := service.RejectSession("ses_target"); got != 1 {
		t.Fatalf("rejected %d questions, want 1", got)
	}
	select {
	case err := <-errsA:
		if !errors.Is(err, ErrRejected) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session reject did not unblock the ask")
	}
	if len(service.ForSession("ses_other")) != 1 {
		t.Fatal("another session's question was rejected too")
	}
}

func TestHooksFire(t *testing.T) {
	// OnAsked runs on the asking goroutine and OnReplied on the replying one,
	// so the counters are shared across goroutines by design.
	var asked, replied atomic.Int32
	service := NewService(Hooks{
		OnAsked:   func(Request) { asked.Add(1) },
		OnReplied: func(string, string, []Answer) { replied.Add(1) },
	}, nil)
	_, _ = askInBackground(t, service, "ses_1")
	request := waitPending(t, service)
	if err := service.Reply(request.ID, []Answer{{"A"}}); err != nil {
		t.Fatal(err)
	}
	if asked.Load() != 1 || replied.Load() != 1 {
		t.Fatalf("asked = %d, replied = %d", asked.Load(), replied.Load())
	}
}

func TestAskRequiresQuestions(t *testing.T) {
	service := NewService(Hooks{}, nil)
	if _, err := service.Ask(context.Background(), AskInput{SessionID: "ses_1"}); err == nil {
		t.Fatal("expected an error for an empty question set")
	}
}
