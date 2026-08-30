package client

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/event"
	"github.com/anomalyco/opencode-go/internal/llm"
	"github.com/anomalyco/opencode-go/internal/permission"
	"github.com/anomalyco/opencode-go/internal/server"
	"github.com/anomalyco/opencode-go/internal/session"
	"github.com/anomalyco/opencode-go/internal/tool"
)

// TestRoundTripAgainstServer exercises the TUI client against the real
// server stack, proving the wire contract end to end.
func TestRoundTripAgainstServer(t *testing.T) {
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	bus := event.NewBus(database)
	session.RegisterProjectors(bus)
	session.RegisterRunnerProjectors(bus)

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "hello from the model"},
		{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 3, Output: 5}},
	}}}
	runner := &session.Runner{
		DB:       database,
		Bus:      bus,
		Messages: session.NewMessageStore(database),
		Provider: provider,
		Tools:    tool.NewRegistry(),
		Agent:    "build",
		System:   "You are opencode.",
		Model:    session.ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
	}
	execution := session.NewExecution(&session.DBSessionLookup{DB: database}, runner)
	service := session.NewService(database, bus)
	service.Execution = execution
	engine := permission.NewEngine(
		permission.StaticRules{Rules: permission.Ruleset{{Action: "bash", Resource: "*", Effect: permission.Ask}}},
		nil, permission.Hooks{}, nil)

	api := httptest.NewServer((&server.Server{Session: service, Bus: bus, Permissions: engine}).Mux())
	defer api.Close()

	c := New(api.URL)
	ctx := context.Background()

	created, err := c.CreateSession(ctx, CreateInput{Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("expected session id")
	}

	sessions, err := c.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != created.ID {
		t.Fatalf("expected created session listed, got %v", sessions)
	}

	// Subscribe before prompting: the stream is live-only, no replay.
	eventCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	events, err := c.Events(eventCtx, created.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Prompt(ctx, created.ID, "say hi"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var messages []Message
	var data AssistantData
	for time.Now().Before(deadline) {
		messages, err = c.Messages(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(messages) >= 2 {
			candidate, decodeErr := DecodeAssistant(messages[len(messages)-1].Data)
			if decodeErr == nil {
				for _, part := range candidate.Content {
					if part.Type == "text" && part.Text != "" {
						data = candidate
					}
				}
			}
		}
		if data.Finish != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(messages) < 2 {
		t.Fatalf("expected user+assistant messages, got %d", len(messages))
	}
	if len(data.Content) == 0 || data.Content[0].Text != "hello from the model" {
		t.Fatalf("unexpected assistant content: %+v", data.Content)
	}
	if data.Finish != "end_turn" {
		t.Fatalf("unexpected finish: %q", data.Finish)
	}

	// The stream must carry the committed events.
	seen := map[string]bool{}
	for event := range events {
		seen[event.Type] = true
		if event.Type == "session.next.step.ended" {
			break
		}
	}
	if !seen["session.next.step.started"] {
		t.Fatalf("expected step events on the stream, saw %v", seen)
	}
}

type scriptedProvider struct{ turns [][]llm.StreamEvent }

func (p *scriptedProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	turn := p.turns[0]
	if len(p.turns) > 1 {
		p.turns = p.turns[1:]
	}
	for _, streamEvent := range turn {
		emit(streamEvent)
	}
	return nil
}
