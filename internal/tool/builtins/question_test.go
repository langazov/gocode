package builtins

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/question"
	"github.com/anomalyco/opencode-go/internal/tool"
)

// stubAsker captures the ask and returns a scripted answer.
type stubAsker struct {
	mu      sync.Mutex
	inputs  []question.AskInput
	answers []question.Answer
	err     error
	// block, when non-nil, holds Ask until closed.
	block chan struct{}
}

func (a *stubAsker) Ask(ctx context.Context, input question.AskInput) ([]question.Answer, error) {
	a.mu.Lock()
	a.inputs = append(a.inputs, input)
	block := a.block
	a.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return a.answers, a.err
}

func (a *stubAsker) lastInput(t *testing.T) question.AskInput {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.inputs) == 0 {
		t.Fatal("Ask was never called")
	}
	return a.inputs[len(a.inputs)-1]
}

var questionInput = map[string]any{
	"questions": []any{map[string]any{
		"question": "Which database?",
		"header":   "Database",
		"multiple": false,
		"options": []any{
			map[string]any{"label": "Postgres", "description": "Relational"},
			map[string]any{"label": "SQLite", "description": "Embedded"},
		},
	}},
}

func TestQuestionToolReturnsAnswers(t *testing.T) {
	asker := &stubAsker{answers: []question.Answer{{"Postgres"}}}
	tool := NewQuestionTool(asker)

	out, err := tool.ExecuteWithContext(context.Background(), questionInput,
		tool2ExecContext("ses_1", "msg_1", "call_1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"Which database?"="Postgres"`) {
		t.Fatalf("output missing the answer: %q", out)
	}

	input := asker.lastInput(t)
	if input.SessionID != "ses_1" {
		t.Fatalf("session = %q", input.SessionID)
	}
	if input.Source == nil || input.Source.CallID != "call_1" || input.Source.MessageID != "msg_1" {
		t.Fatalf("source = %+v", input.Source)
	}
	if len(input.Questions) != 1 || len(input.Questions[0].Options) != 2 {
		t.Fatalf("questions = %+v", input.Questions)
	}
	if input.Questions[0].Options[0].Label != "Postgres" {
		t.Fatalf("options = %+v", input.Questions[0].Options)
	}
}

func TestQuestionToolMarksUnanswered(t *testing.T) {
	tool := NewQuestionTool(&stubAsker{answers: []question.Answer{{}}})
	out, err := tool.ExecuteWithContext(context.Background(), questionInput,
		tool2ExecContext("ses_1", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `="Unanswered"`) {
		t.Fatalf("output should mark an empty answer as unanswered: %q", out)
	}
}

func TestQuestionToolPropagatesRejection(t *testing.T) {
	tool := NewQuestionTool(&stubAsker{err: question.ErrRejected})
	_, err := tool.ExecuteWithContext(context.Background(), questionInput,
		tool2ExecContext("ses_1", "", ""))
	if !errors.Is(err, question.ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

func TestQuestionToolValidatesInput(t *testing.T) {
	tool := NewQuestionTool(&stubAsker{})
	for name, input := range map[string]map[string]any{
		"no questions key": {},
		"empty list":       {"questions": []any{}},
		"missing text":     {"questions": []any{map[string]any{"header": "h"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := tool.ExecuteWithContext(context.Background(), input,
				tool2ExecContext("ses_1", "", "")); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

// TestQuestionToolUnblocksOnCancel guards against a parked question surviving
// an interrupted turn.
func TestQuestionToolUnblocksOnCancel(t *testing.T) {
	asker := &stubAsker{block: make(chan struct{})}
	tool := NewQuestionTool(asker)
	ctx, cancel := context.WithCancel(context.Background())

	errs := make(chan error, 1)
	go func() {
		_, err := tool.ExecuteWithContext(ctx, questionInput, tool2ExecContext("ses_1", "", ""))
		errs <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errs:
		if err == nil {
			t.Fatal("expected a cancellation error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not unblock the question tool")
	}
}

// stubSwitcher records agent switches for the plan-mode tests.
type stubSwitcher struct {
	mu       sync.Mutex
	switched []string
	err      error
}

func (s *stubSwitcher) SetAgent(ctx context.Context, sessionID, agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.switched = append(s.switched, sessionID+"->"+agent)
	return nil
}

func (s *stubSwitcher) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.switched...)
}

func TestPlanExitSwitchesOnYes(t *testing.T) {
	asker := &stubAsker{answers: []question.Answer{{"Yes"}}}
	switcher := &stubSwitcher{}
	planExit := NewPlanExitTool(asker, switcher).(interface {
		ExecuteWithContext(context.Context, map[string]any, tool.ExecContext) (string, error)
	})

	out, err := planExit.ExecuteWithContext(context.Background(), nil, tool2ExecContext("ses_1", "msg", "call"))
	if err != nil {
		t.Fatal(err)
	}
	if got := switcher.all(); len(got) != 1 || got[0] != "ses_1->build" {
		t.Fatalf("switches = %v", got)
	}
	if !strings.Contains(out, "build") {
		t.Fatalf("output = %q", out)
	}
}

func TestPlanExitStaysOnNo(t *testing.T) {
	asker := &stubAsker{answers: []question.Answer{{"No"}}}
	switcher := &stubSwitcher{}
	planExit := NewPlanExitTool(asker, switcher).(interface {
		ExecuteWithContext(context.Context, map[string]any, tool.ExecContext) (string, error)
	})

	out, err := planExit.ExecuteWithContext(context.Background(), nil, tool2ExecContext("ses_1", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := switcher.all(); len(got) != 0 {
		t.Fatalf("declining should not switch agents, got %v", got)
	}
	if !strings.Contains(out, "stay") && !strings.Contains(out, "Continue") {
		t.Fatalf("output = %q", out)
	}
}

func TestPlanEnterTargetsPlanAgent(t *testing.T) {
	asker := &stubAsker{answers: []question.Answer{{"Yes"}}}
	switcher := &stubSwitcher{}
	planEnter := NewPlanEnterTool(asker, switcher).(interface {
		ExecuteWithContext(context.Context, map[string]any, tool.ExecContext) (string, error)
	})

	if _, err := planEnter.ExecuteWithContext(context.Background(), nil,
		tool2ExecContext("ses_1", "", "")); err != nil {
		t.Fatal(err)
	}
	if got := switcher.all(); len(got) != 1 || got[0] != "ses_1->plan" {
		t.Fatalf("switches = %v", got)
	}
}

func TestPlanToolsRequireSession(t *testing.T) {
	planExit := NewPlanExitTool(&stubAsker{}, &stubSwitcher{}).(interface {
		ExecuteWithContext(context.Context, map[string]any, tool.ExecContext) (string, error)
	})
	if _, err := planExit.ExecuteWithContext(context.Background(), nil, tool.ExecContext{}); err == nil {
		t.Fatal("expected a missing-session error")
	}
}

func tool2ExecContext(sessionID, messageID, callID string) tool.ExecContext {
	return tool.ExecContext{SessionID: sessionID, AssistantMessageID: messageID, CallID: callID}
}
