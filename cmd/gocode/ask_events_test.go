package main

import (
	"context"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/session"
)

// TestQuestionAskPublishes is the other half of the reported hang: the tool
// call parks on a question and the session then emits nothing at all, so
// without this event the interface waits out its 10s reconciliation tick
// before showing the prompt that is holding the turn up.
func TestQuestionAskPublishes(t *testing.T) {
	testCatalog(t)
	stack := bootStackT(t, context.Background(), "")

	events, unsubscribe := stack.Bus.Subscribe(8)
	defer unsubscribe()

	asked := make(chan struct{})
	go func() {
		close(asked)
		// Ask blocks until replied to; the reply below is what releases it.
		_, _ = stack.Questions.Ask(context.Background(), question.AskInput{
			SessionID: "ses_ask",
			Questions: []question.Prompt{{
				Question: "Switch to plan?",
				Header:   "Plan Agent",
				Options:  []question.Option{{Label: "Yes"}, {Label: "No"}},
			}},
		})
	}()
	<-asked

	payload := awaitEvent(t, events, session.QuestionAsked.Type)
	if payload.Data["sessionID"] != "ses_ask" {
		t.Fatalf("event data = %+v", payload.Data)
	}
	// sessionID is what routes the event to a session-filtered subscriber;
	// without it the TUI's stream drops it.
	requestID, _ := payload.Data["requestID"].(string)
	if requestID == "" {
		t.Fatal("the event carries no request id to fetch")
	}

	if err := stack.Questions.Reply(requestID, []question.Answer{{"Yes"}}); err != nil {
		t.Fatal(err)
	}
	settled := awaitEvent(t, events, session.QuestionSettled.Type)
	if settled.Data["requestID"] != requestID {
		t.Fatalf("settled event = %+v", settled.Data)
	}
}

// A rejected question settles too, so a banner raised on one client retracts
// when another answers or the run is interrupted.
func TestQuestionRejectPublishesSettled(t *testing.T) {
	testCatalog(t)
	stack := bootStackT(t, context.Background(), "")

	events, unsubscribe := stack.Bus.Subscribe(8)
	defer unsubscribe()

	go func() {
		_, _ = stack.Questions.Ask(context.Background(), question.AskInput{
			SessionID: "ses_reject",
			Questions: []question.Prompt{{Question: "Switch?", Options: []question.Option{{Label: "Yes"}}}},
		})
	}()
	asked := awaitEvent(t, events, session.QuestionAsked.Type)
	requestID, _ := asked.Data["requestID"].(string)

	if err := stack.Questions.Reject(requestID); err != nil {
		t.Fatal(err)
	}
	if settled := awaitEvent(t, events, session.QuestionSettled.Type); settled.Data["sessionID"] != "ses_reject" {
		t.Fatalf("settled event = %+v", settled.Data)
	}
}

// awaitEvent drains the subscription until the named type arrives. Other
// events (catalog refreshes, and so on) share the bus.
func awaitEvent(t *testing.T, events <-chan event.Payload, eventType string) event.Payload {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case payload := <-events:
			if payload.Type == eventType {
				return payload
			}
		case <-deadline:
			t.Fatalf("no %s event was published", eventType)
		}
	}
}
