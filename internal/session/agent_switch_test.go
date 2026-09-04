package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/langazov/gocode-go/internal/event"
)

// TestSetAgentRecordsTheSwitch covers the announcement side of plan mode: the
// switch can originate inside a turn (plan_enter/plan_exit), so it has to
// reach clients over the stream rather than only through the reply to a
// request they made themselves.
func TestSetAgentRecordsTheSwitch(t *testing.T) {
	bus, database := setup(t)
	RegisterRunnerProjectors(bus)
	service := NewService(database, bus)
	ctx := context.Background()

	events, unsubscribe := bus.Subscribe(8)
	defer unsubscribe()

	if err := service.SetAgent(ctx, "ses_1", PlanAgentID); err != nil {
		t.Fatal(err)
	}

	if got := LoadSessionAgent(ctx, database, "ses_1"); got != PlanAgentID {
		t.Fatalf("session agent = %q, want %q", got, PlanAgentID)
	}

	var payload event.Payload
	select {
	case payload = <-events:
	default:
		t.Fatal("no event was published for the switch")
	}
	if payload.Type != AgentSwitched.Type {
		t.Fatalf("event type = %q", payload.Type)
	}
	if payload.Data["agent"] != PlanAgentID || payload.Data["sessionID"] != "ses_1" {
		t.Fatalf("event data = %+v", payload.Data)
	}

	// The projector writes the timeline entry. ToLLMMessages skips this type,
	// so it is a record for readers and never reaches the model.
	messages, err := NewMessageStore(database).List(ctx, "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, message := range messages {
		if message.Type != TypeAgentSwitched {
			continue
		}
		found = true
		var data struct {
			Agent string `json:"agent"`
		}
		if err := json.Unmarshal(message.Data, &data); err != nil {
			t.Fatal(err)
		}
		if data.Agent != PlanAgentID {
			t.Fatalf("timeline entry agent = %q", data.Agent)
		}
	}
	if !found {
		t.Fatal("no agent-switched entry was projected")
	}
	llmMessages, err := ToLLMMessages(messages, ModelRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(llmMessages) != 0 {
		t.Fatalf("the switch marker must not reach the model, got %+v", llmMessages)
	}
}

// TestEnqueuePromptQueues is the plan_exit handoff: the prompt must wait for
// the idle boundary rather than steer into the turn that is still returning
// the tool's own result.
func TestEnqueuePromptQueues(t *testing.T) {
	bus, database := setup(t)
	service := NewService(database, bus)
	ctx := context.Background()

	if err := service.EnqueuePrompt(ctx, "ses_1", "Execute the plan"); err != nil {
		t.Fatal(err)
	}

	queued, err := HasPending(ctx, database, "ses_1", DeliveryQueue)
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("the handoff prompt was not queued")
	}
	steered, err := HasPending(ctx, database, "ses_1", DeliverySteer)
	if err != nil {
		t.Fatal(err)
	}
	if steered {
		t.Fatal("the handoff must not steer into the running turn")
	}
}

// A cancelled tool context must not lose the handoff: the switch has already
// been committed, so dropping the prompt would leave the session pinned to
// build with nothing to run.
func TestEnqueuePromptSurvivesCancelledContext(t *testing.T) {
	bus, database := setup(t)
	service := NewService(database, bus)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := service.EnqueuePrompt(ctx, "ses_1", "Execute the plan"); err != nil {
		t.Fatal(err)
	}
	queued, err := HasPending(context.Background(), database, "ses_1", DeliveryQueue)
	if err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("the handoff prompt was dropped with the tool's context")
	}
}
