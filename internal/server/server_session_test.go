package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/db"
	"github.com/anomalyco/opencode-go/internal/event"
	"github.com/anomalyco/opencode-go/internal/llm"
	"github.com/anomalyco/opencode-go/internal/session"
	"github.com/anomalyco/opencode-go/internal/tool"
)

type scriptedProvider struct {
	turns [][]llm.StreamEvent
}

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

func newTestServer(t *testing.T) (*Server, *db.DB, *event.Bus) {
	t.Helper()
	database, err := db.OpenAndMigrate(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	bus := event.NewBus(database)
	session.RegisterProjectors(bus)
	session.RegisterRunnerProjectors(bus)

	provider := &scriptedProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "Hello from the assistant"},
		{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 5, Output: 7}},
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
	return &Server{Session: service, Bus: bus}, database, bus
}

func doJSON(t *testing.T, handler *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.Mux().ServeHTTP(rec, req)
	return rec
}

func TestSessionLifecycle(t *testing.T) {
	server, _, _ := newTestServer(t)

	rec := doJSON(t, server, "POST", "/api/session", map[string]string{"directory": t.TempDir()})
	if rec.Code != 200 {
		t.Fatalf("create: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var created session.Info
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("expected session ID")
	}

	rec = doJSON(t, server, "GET", "/api/session/"+created.ID, nil)
	if rec.Code != 200 {
		t.Fatalf("get: expected 200, got %d", rec.Code)
	}

	rec = doJSON(t, server, "GET", "/api/session", nil)
	if rec.Code != 200 {
		t.Fatalf("list: expected 200, got %d", rec.Code)
	}
	var listed []session.Info
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 session, got %d", len(listed))
	}

	rec = doJSON(t, server, "POST", "/api/session/"+created.ID+"/prompt", map[string]string{"text": "say hello"})
	if rec.Code != 200 {
		t.Fatalf("prompt: expected 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var promptResult struct {
		MessageID string `json:"messageID"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&promptResult); err != nil {
		t.Fatal(err)
	}
	if promptResult.MessageID == "" {
		t.Fatal("expected prompt message ID")
	}

	waitForAssistant(t, server, created.ID)
}

func waitForAssistant(t *testing.T, server *Server, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec := doJSON(t, server, "GET", "/api/session/"+sessionID+"/message", nil)
		if rec.Code != 200 {
			t.Fatalf("messages: expected 200, got %d", rec.Code)
		}
		var messages []struct {
			Type string `json:"type"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&messages); err != nil {
			t.Fatal(err)
		}
		var hasUser, hasAssistant bool
		for _, message := range messages {
			if message.Type == "user" {
				hasUser = true
			}
			if message.Type == "assistant" {
				hasAssistant = true
			}
		}
		if hasUser && hasAssistant {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("assistant message was not projected in time")
}

func TestPromptMissingSession(t *testing.T) {
	server, _, _ := newTestServer(t)
	rec := doJSON(t, server, "POST", "/api/session/ses_missing/prompt", map[string]string{"text": "hi"})
	if rec.Code == 200 {
		t.Fatal("expected error for missing session")
	}
}

func TestEventStream(t *testing.T) {
	server, _, _ := newTestServer(t)
	httpServer := httptest.NewServer(server.Mux())
	defer httpServer.Close()

	rec := doJSON(t, server, "POST", "/api/session", map[string]string{"directory": t.TempDir()})
	var created session.Info
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", httpServer.URL+"/api/event?sessionID="+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("expected text/event-stream, got %s", contentType)
	}

	if _, err := doJSONConcurrent(httpServer.URL, created.ID); err != nil {
		t.Fatal(err)
	}

	scanner := bufio.NewScanner(response.Body)
	sawStep := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var wire struct {
			Type    string `json:"type"`
			Session string `json:"sessionID"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &wire); err != nil {
			continue
		}
		if wire.Session != created.ID {
			t.Fatalf("expected filtered session, got %s", wire.Session)
		}
		if wire.Type == "session.next.step.started" || wire.Type == "session.next.step.ended" {
			sawStep = true
			break
		}
	}
	if !sawStep {
		t.Fatal("expected a step event on the SSE stream")
	}
}

func doJSONConcurrent(baseURL, sessionID string) (*http.Response, error) {
	body := bytes.NewReader([]byte(`{"text":"say hello"}`))
	return http.Post(baseURL+"/api/session/"+sessionID+"/prompt", "application/json", body)
}
