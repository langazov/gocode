package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/tui/client"
)

type renameCall struct {
	sessionID string
	title     string
}

type modelCall struct {
	sessionID string
	provider  string
	model     string
}

type mockAPI struct {
	prompted   []string
	replies    []string
	interrupts int
	created    int
	compacts   int
	pending    []client.PermissionRequest
	questions  []client.QuestionRequest
	answered   [][]([]string) // answers posted to /api/question/{id}/reply
	rejected   []string       // request ids posted to /api/question/{id}/reject
	renamed    []renameCall
	models     []modelCall
	forkedFrom string
	mcpStatus  string // GET /api/mcp responds with this status for "test-server"; mutate mid-test to verify tickMsg re-fetches it
	statsCalls int
}

func newMockAPI(t *testing.T) (*mockAPI, *httptest.Server) {
	t.Helper()
	api := &mockAPI{pending: []client.PermissionRequest{}, questions: []client.QuestionRequest{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/session", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]client.Session{{
			ID: "ses_1", ProjectID: "prj_1", Title: "Test session", Directory: "/tmp", Version: "1",
		}})
	})
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		api.created++
		json.NewEncoder(w).Encode(client.Session{ID: fmt.Sprintf("ses_new%d", api.created), Title: "new"})
	})
	mux.HandleFunc("POST /api/session/{sessionID}/prompt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text string `json:"text"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		api.prompted = append(api.prompted, r.PathValue("sessionID")+":"+body.Text)
		json.NewEncoder(w).Encode(map[string]string{"messageID": "msg_out"})
	})
	mux.HandleFunc("POST /api/session/{sessionID}/interrupt", func(w http.ResponseWriter, r *http.Request) {
		api.interrupts++
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("GET /api/session/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("sessionID")
		if id != "ses_1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(client.Session{ID: "ses_1", ProjectID: "prj_1", Title: "Test session", Directory: "/tmp", Version: "1"})
	})
	mux.HandleFunc("GET /api/session/{sessionID}/message", func(w http.ResponseWriter, r *http.Request) {
		messages := []client.Message{
			{ID: "msg_u", SessionID: "ses_1", Type: "user", Seq: 0, Data: json.RawMessage(`{"text":"hello"}`)},
			{ID: "msg_a", SessionID: "ses_1", Type: "assistant", Seq: 1, Data: json.RawMessage(
				`{"agent":"build","content":[{"type":"text","id":"t1","text":"world"}],"finish":"end_turn"}`)},
		}
		json.NewEncoder(w).Encode(messages)
	})
	mux.HandleFunc("GET /api/session/{sessionID}/permission", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.pending)
	})
	mux.HandleFunc("POST /api/session/{sessionID}/permission/{requestID}/reply", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reply string `json:"reply"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		api.replies = append(api.replies, body.Reply)
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /api/session/{sessionID}/question", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.questions)
	})
	mux.HandleFunc("POST /api/question/{requestID}/reply", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Answers [][]string `json:"answers"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		api.answered = append(api.answered, body.Answers)
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /api/question/{requestID}/reject", func(w http.ResponseWriter, r *http.Request) {
		api.rejected = append(api.rejected, r.PathValue("requestID"))
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /api/session/{sessionID}/rename", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title string `json:"title"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		api.renamed = append(api.renamed, renameCall{r.PathValue("sessionID"), body.Title})
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /api/session/{sessionID}/model", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProviderID string `json:"providerID"`
			ID         string `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		api.models = append(api.models, modelCall{r.PathValue("sessionID"), body.ProviderID, body.ID})
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /api/model", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]client.Model{
			{ProviderID: "anthropic", ID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5"},
			{ProviderID: "anthropic", ID: "claude-opus-4-5", Name: "Claude Opus 4.5"},
		})
	})
	mux.HandleFunc("GET /api/skill", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]client.Skill{
			{Name: "chunk-sidecar", Description: "Validate on a remote sidecar", Location: "/tmp/skill/chunk-sidecar/SKILL.md"},
			{Name: "artifact-design", Description: "Design guidance for Artifacts", Slash: true, Location: "/tmp/skill/artifact-design/SKILL.md"},
		})
	})
	mux.HandleFunc("POST /api/session/{sessionID}/compact", func(w http.ResponseWriter, r *http.Request) {
		api.compacts++
		w.Write([]byte(`{"compacted":true}`))
	})
	mux.HandleFunc("POST /api/session/{sessionID}/fork", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MessageID string `json:"messageID"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		api.forkedFrom = body.MessageID
		json.NewEncoder(w).Encode(client.Session{ID: "ses_fork", Title: "Fork: Test session", Directory: "/tmp", Version: "1"})
	})
	mux.HandleFunc("GET /api/session/{sessionID}/children", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]client.Session{{ID: "ses_child", Title: "child", Directory: "/tmp", Version: "1"}})
	})
	mux.HandleFunc("GET /api/session/{sessionID}/stats", func(w http.ResponseWriter, r *http.Request) {
		api.statsCalls++
		json.NewEncoder(w).Encode(map[string]any{
			"cost": 0.5, "tokensInput": 10, "tokensOutput": 20,
			"tokensReasoning": 0, "tokensCacheRead": 0, "tokensCacheWrite": 0, "messages": 2,
		})
	})
	mux.HandleFunc("GET /api/mcp", func(w http.ResponseWriter, r *http.Request) {
		status := api.mcpStatus
		if status == "" {
			status = "connected"
		}
		json.NewEncoder(w).Encode(map[string]any{"test-server": map[string]string{"status": status}})
	})
	mux.HandleFunc("GET /api/event", func(w http.ResponseWriter, r *http.Request) {
		// Hold briefly, then end the stream; tests drive events directly and
		// server.Close must not block on this handler.
		select {
		case <-r.Context().Done():
		case <-time.After(250 * time.Millisecond):
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return api, server
}

// TestSetThemeRefreshesInputStyles guards the bug where switching themes
// (the theme picker's live preview, dialogs.go's themesOverlay) left the
// prompt textarea's cursor-line background and placeholder color on
// whatever theme was active when New built it: input.SetStyles bakes the
// theme's colors into the textarea's own Styles once, so a bare `a.theme =`
// reassignment elsewhere never touched them again. setTheme exists so every
// theme change goes through one place that re-derives them too.
func TestSetThemeRefreshesInputStyles(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.setTheme(themeResolve("gocode-light"))

	rendered := app.promptBox(app.sessionPromptBoxWidth())
	// #1e1e1e, gocode-dark's backgroundElement (the theme New actually
	// built the input with) — must not survive the switch.
	if strings.Contains(rendered, "48;2;30;30;30") {
		t.Fatalf("prompt box still carries gocode-dark's backgroundElement after setTheme(gocode-light):\n%q", rendered)
	}
	// #f5f5f5, gocode-light's backgroundElement — must be what's actually
	// painted now.
	if !strings.Contains(rendered, "48;2;245;245;245") {
		t.Fatalf("prompt box is missing gocode-light's backgroundElement after setTheme(gocode-light):\n%q", rendered)
	}
}

func newTestApp(t *testing.T, baseURL string) *App {
	t.Helper()
	// Isolate prompt-history.jsonl reads/writes (New loads it from
	// global.Resolve().State) so tests never touch the real machine's
	// gocode state directory.
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	app := New(context.Background(), client.New(baseURL), "gocode-dark")
	app.width, app.height = 100, 30
	return app
}

// drive feeds a message into Update and executes the returned commands,
// flattening batches, the way the bubbletea runtime would.
func drive(t *testing.T, app *App, msg tea.Msg) {
	t.Helper()
	runCmds(t, app, app.Update(msg), 0)
}

// driveCmd executes a command and feeds its message back into Update.
func driveCmd(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()
	runCmds(t, app, cmd, 0)
}

func runCmds(t *testing.T, app *App, cmd tea.Cmd, depth int) {
	t.Helper()
	if cmd == nil || depth > 4 {
		return
	}
	result := cmd()
	if batch, ok := result.(tea.BatchMsg); ok {
		for _, inner := range batch {
			runCmds(t, app, inner, depth+1)
		}
		return
	}
	if result == nil {
		return
	}
	runCmds(t, app, app.Update(result), depth+1)
}

// press feeds one keypress through the app, running any resulting commands.
func press(t *testing.T, app *App, key string) {
	t.Helper()
	r := []rune(key)[0]
	drive(t, app, tea.KeyPressMsg{Text: key, Code: r})
}

// armLeader presses the leader key, intentionally dropping the timeout
// command the runtime would schedule.
func armLeader(t *testing.T, app *App) {
	t.Helper()
	app.handleKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
}

// openSession drives the leader+l overlay to open the seeded session.
func openSession(t *testing.T, app *App) {
	t.Helper()
	driveCmd(t, app, app.loadSessionsCmd())
	armLeader(t, app)
	press(t, app, "l")
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
}

// TestResumeSessionOpensChatView is the regression for "./gocode -s
// <sessionID> doesn't resume the session": RunOptions.SessionID must open
// straight into that session's chat view instead of the home screen. Drives
// resumeSessionCmd directly (rather than the full Init() batch) to avoid the
// real 2s ticker Init() also schedules.
func TestResumeSessionOpensChatView(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.resumeSessionID = "ses_1"
	if app.view != viewHome {
		t.Fatalf("app should start on the home view before resuming")
	}

	driveCmd(t, app, app.resumeSessionCmd(app.resumeSessionID))

	if app.view != viewChat {
		t.Fatalf("expected chat view after resume, got view=%d", app.view)
	}
	if app.active == nil || app.active.ID != "ses_1" {
		t.Fatalf("expected active session ses_1, got %+v", app.active)
	}
	if len(app.timeline) == 0 {
		t.Fatalf("expected resumed session's history to load")
	}
}

// TestResumeUnknownSessionFallsBackToHome ensures a bad --session id
// surfaces a status message rather than crashing or hanging.
func TestResumeUnknownSessionFallsBackToHome(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.resumeSessionID = "ses_missing"

	driveCmd(t, app, app.resumeSessionCmd(app.resumeSessionID))

	if app.view != viewHome {
		t.Fatalf("expected to stay on home view for an unknown session, got view=%d", app.view)
	}
	if app.statusMsg == "" {
		t.Fatalf("expected a status message reporting the missing session")
	}
}

// TestInitIncludesResumeCommandWhenSessionIDSet guards the Init() wiring
// itself (that RunOptions.SessionID actually reaches a resume attempt at
// startup), without paying for Init()'s real 2s ticker: batches run inside
// runCmds synchronously, so a bare a.tick() in the mix would block the test
// for multiples of 2s once its message loops back through Update.
func TestInitIncludesResumeCommandWhenSessionIDSet(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.resumeSessionID = "ses_1"

	cmd := app.Init()
	if cmd == nil {
		t.Fatal("Init() returned nil with resumeSessionID set")
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected Init() to batch commands, got %T", msg)
	}
	if len(batch) < 5 {
		t.Fatalf("expected the resume command alongside the usual startup batch, got %d commands", len(batch))
	}
}

// TestTickRefreshesMCPStatus guards against MCP status going stale after
// Init(): the backend now reconnects servers in the background (a boot-time
// failure recovering, or a later drop), so the TUI must re-poll GET /api/mcp
// periodically rather than trusting the one-time value fetched at startup.
// update()'s tickMsg case batches [a.tick(), a.loadMCPCmd()] (in that order,
// with no active session to append loadPermissions/loadStats/todos after);
// batch[0] is a real 2s timer that must never be invoked in a test, so this
// picks out and runs only batch[1] directly instead of using drive()'s full
// recursive runner.
func TestTickRefreshesMCPStatus(t *testing.T) {
	api, server := newMockAPI(t)
	api.mcpStatus = "connecting"
	app := newTestApp(t, server.URL)

	driveCmd(t, app, app.loadMCPCmd())
	if len(app.mcpServers) != 1 || app.mcpServers[0].Status != "connecting" {
		t.Fatalf("initial mcpServers = %+v, want one server with status connecting", app.mcpServers)
	}

	api.mcpStatus = "connected"

	msg := app.Update(tickMsg{})()
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) < 2 {
		t.Fatalf("expected tickMsg to batch [tick, loadMCP, ...], got %T len=%d", msg, len(batch))
	}
	driveCmd(t, app, batch[1])

	if len(app.mcpServers) != 1 || app.mcpServers[0].Status != "connected" {
		t.Fatalf("after tickMsg, mcpServers = %+v, want status connected (tickMsg should re-fetch MCP status)", app.mcpServers)
	}
}

func TestHomeShowsLogoAndPrompt(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	// lipgloss v2's Style.Render always emits real ANSI (v1 no-op'd styling
	// when stdout wasn't a TTY, which is what let these plain-substring
	// checks work unmodified); strip it back to plain text, since these
	// assertions care about structure/content, not the escape codes — the
	// logo in particular is rendered one rune at a time (renderLogoLine), so
	// each rune now carries its own SGR wrapper that would otherwise split
	// "█▀▀█" apart.
	view := ansi.Strip(app.View())
	if !strings.Contains(view, "█▀▀█") {
		t.Fatalf("home should render the logo, got %q", view)
	}
	if !strings.Contains(view, "Ask anything...") {
		t.Fatalf("home should show the prompt placeholder, got %q", view)
	}
	if !strings.Contains(view, "tab agents") || !strings.Contains(view, "ctrl+p commands") {
		t.Fatalf("home should advertise shortcuts like the original, got %q", view)
	}
	if !strings.Contains(view, "● Tip") {
		t.Fatalf("home should show a rotating tip, got %q", view)
	}
	if !strings.Contains(view, appVersion) {
		t.Fatalf("home status bar should show the version, got %q", view)
	}
}

func TestLeaderLOpensSessionList(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	driveCmd(t, app, app.loadSessionsCmd())

	armLeader(t, app)
	press(t, app, "l")
	if app.overlay == nil {
		t.Fatal("leader+l should open the session list dialog")
	}
	view := app.View()
	if !strings.Contains(view, "Sessions") || !strings.Contains(view, "Test session") {
		t.Fatalf("session dialog should list sessions, got %q", view)
	}

	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.view != viewChat || app.overlay != nil {
		t.Fatal("enter should open the selected session and close the dialog")
	}
	_ = api
}

func TestHomePromptCreatesAndSends(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)

	for _, r := range "fix the bug" {
		press(t, app, string(r))
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if len(api.prompted) != 1 {
		t.Fatalf("expected prompt sent to created session, got %v", api.prompted)
	}
	if !strings.HasPrefix(api.prompted[0], "ses_new1:") || !strings.HasSuffix(api.prompted[0], "fix the bug") {
		t.Fatalf("unexpected prompt: %q", api.prompted[0])
	}
	if app.view != viewChat || !app.busy {
		t.Fatal("home prompt should transition to the chat view, busy")
	}
}

func TestChatSendAndTimeline(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	view := app.View()
	if !strings.Contains(view, "hello") || !strings.Contains(view, "world") {
		t.Fatalf("timeline should show the exchanged messages, got %q", view)
	}

	for _, r := range "again" {
		press(t, app, string(r))
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(api.prompted) != 1 || !strings.HasSuffix(api.prompted[0], "again") {
		t.Fatalf("expected chat prompt sent, got %v", api.prompted)
	}
	if !app.busy {
		t.Fatal("sending should mark the session busy")
	}
}

// feed folds events through the aggregator's tree and applies the resulting
// snapshot, which is the path a real event takes since the multi-agent
// rework: per-event work happens off the main goroutine, and Update only ever
// sees a coalesced snapshot.
func feed(app *App, events ...client.Event) bool {
	state := newTree()
	for _, e := range events {
		state.apply(e)
	}
	return app.applySnapshot(state.snapshot(0)).timeline
}

func TestStreamingTextDeltas(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	feed(app,
		client.Event{Type: "session.next.step.started", Session: "ses_1"},
		client.Event{Type: "session.next.text.delta", Session: "ses_1", Data: map[string]any{
			"assistantMessageID": "msg_a1", "delta": "Hel",
		}},
		client.Event{Type: "session.next.text.delta", Session: "ses_1", Data: map[string]any{
			"assistantMessageID": "msg_a1", "delta": "lo",
		}},
	)
	if got := app.streaming["msg_a1"]; got == nil || got.String() != "Hello" {
		t.Fatalf("expected accumulated streaming text, got %v", app.streaming["msg_a1"])
	}
	if !app.busy {
		t.Fatal("step.started should mark the session busy")
	}

	feed(app, client.Event{Type: "session.next.step.ended", Session: "ses_1"})
	if len(app.streaming) != 0 {
		t.Fatal("streaming buffers should clear when the step ends")
	}
	if app.busy {
		t.Fatal("step.ended should clear busy")
	}
}

// TestSubagentEventsDoNotTouchParent is the phase 4 isolation guarantee: a
// child session's deltas must never land in the parent's streaming text. The
// old per-event path ignored the event's session entirely.
func TestSubagentEventsDoNotTouchParent(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	feed(app,
		client.Event{Type: "session.next.text.delta", Session: "ses_1", Data: map[string]any{
			"assistantMessageID": "msg_parent", "delta": "parent text",
		}},
		client.Event{Type: "session.next.text.delta", Session: "ses_child", Data: map[string]any{
			"assistantMessageID": "msg_child", "delta": "subagent text",
		}},
		client.Event{Type: "session.next.step.started", Session: "ses_child"},
	)

	if got := app.streaming["msg_parent"]; got == nil || got.String() != "parent text" {
		t.Fatalf("parent text = %v, want %q", app.streaming["msg_parent"], "parent text")
	}
	if _, leaked := app.streaming["msg_child"]; leaked {
		t.Fatal("a subagent's streamed text leaked into the parent's view")
	}
	subagents := app.activeSubagents()
	if len(subagents) != 1 || subagents[0].ID != "ses_child" {
		t.Fatalf("expected the child to show as a live subagent, got %+v", subagents)
	}
}

func TestPermissionDialogAndReply(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	api.pending = []client.PermissionRequest{{
		ID: "per_1", SessionID: "ses_1", Action: "bash", Resources: []string{"ls"},
	}}
	app.applyPermissions(api.pending)
	if app.permission == nil {
		t.Fatal("expected pending permission to surface")
	}
	view := app.View()
	for _, want := range []string{"Permission required", "Shell command", "Allow once", "Allow always", "Reject"} {
		if !strings.Contains(view, want) {
			t.Fatalf("permission dialog missing %q: %q", want, view)
		}
	}

	driveCmd(t, app, app.handleKey(tea.KeyPressMsg{Text: "y", Code: 'y'}))
	if len(api.replies) != 1 || api.replies[0] != "once" {
		t.Fatalf("expected once reply, got %v", api.replies)
	}
	if app.permission != nil {
		t.Fatal("dialog should clear after replying")
	}
}

func TestEscapeInterruptsBusySession(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.busy = true

	// Interrupt is a two-press gesture upstream (prompt/index.tsx's
	// session.interrupt increments a counter and only aborts at >= 2): the
	// first escape arms it and repaints the footer hint, the second fires.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if api.interrupts != 0 {
		t.Fatalf("the first escape should only arm the interrupt, got %d interrupts", api.interrupts)
	}
	if app.interruptArmed != 1 {
		t.Fatalf("the first escape should arm the interrupt, got %d", app.interruptArmed)
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})
	if api.interrupts != 1 {
		t.Fatalf("the second escape should interrupt a busy session, got %d interrupts", api.interrupts)
	}
	if app.interruptArmed != 0 {
		t.Fatalf("interrupting should disarm the gesture, got %d", app.interruptArmed)
	}

	// ctrl+c exits regardless of state.
	drive(t, app, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !app.quitting {
		t.Fatal("ctrl+c should exit the application")
	}
}

// TestChatScrollKeepsPromptPinnedToBottom guards against a regression where
// scrolling grew the visible window instead of shifting it (see
// viewChat's maxOffset clamp): the total rendered height, and thus the
// pinned prompt/input block's position, must not change as scrollOffset
// grows.
func TestChatScrollKeepsPromptPinnedToBottom(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1"}
	app.width, app.height = 100, 30

	messages := make([]client.Message, 0, 40)
	for i := 0; i < 40; i++ {
		messages = append(messages, client.Message{
			ID: fmt.Sprintf("msg_%d", i), SessionID: "ses_1", Type: "user", Seq: i,
			Data: json.RawMessage(mustJSON(map[string]string{"text": fmt.Sprintf("message number %d", i)})),
		})
	}
	app.timeline = messages

	renderedHeight := func() int { return strings.Count(app.View(), "\n") + 1 }
	base := renderedHeight()

	for _, offset := range []int{1, 5, 30, 1000} {
		app.scrollOffset = offset
		if got := renderedHeight(); got != base {
			t.Fatalf("scrollOffset=%d rendered height = %d, want unchanged %d (prompt should stay pinned)",
				offset, got, base)
		}
	}

	view := ansi.Strip(app.View()) // see TestHomeShowsLogoAndPrompt's comment
	if !strings.Contains(view, "Ask anything") {
		t.Fatalf("prompt placeholder should remain visible while scrolled back, got:\n%s", view)
	}
}

// TestTimelineHasExactlyOneBlankLineBetweenMessages guards against a
// regression where assistantTextBlock's own leading blank line (needed to
// separate it from a reasoning/tool block earlier in the *same* message)
// doubled up with timelineLines' between-message separator for the common
// case of a text-only assistant message, leaving two blank lines instead
// of one and visibly loosening the whole timeline's vertical rhythm.
func TestTimelineHasExactlyOneBlankLineBetweenMessages(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.timeline = []client.Message{
		{ID: "m1", Type: "user", Data: json.RawMessage(`{"text":"hello"}`)},
		{ID: "m2", Type: "assistant", Data: json.RawMessage(
			`{"agent":"build","content":[{"type":"text","id":"t","text":"hi there"}],"finish":"stop"}`)},
	}
	lines := app.timelineLines()
	for i := 0; i < len(lines)-1; i++ {
		if lines[i] == "" && lines[i+1] == "" {
			t.Fatalf("timeline has two consecutive blank lines at %d/%d, want at most one: %q",
				i, i+1, lines)
		}
	}
}

// TestScrolledConversationKeepsFooterVisible guards against a regression
// where the "N more lines" scroll indicator was appended as an extra row
// beyond viewportHeight's budget instead of coming out of it — once
// scrolled, that pushed the footer past frame()'s MaxHeight crop and it
// disappeared entirely (and everything below the timeline shifted down a
// line versus the unscrolled render).
func TestScrolledConversationKeepsFooterVisible(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1"}
	app.width, app.height = 100, 30
	app.sidebar = false // this test is about vertical layout, not the sidebar

	messages := make([]client.Message, 0, 40)
	for i := 0; i < 40; i++ {
		messages = append(messages, client.Message{
			ID: fmt.Sprintf("msg_%d", i), SessionID: "ses_1", Type: "user", Seq: i,
			Data: json.RawMessage(mustJSON(map[string]string{"text": fmt.Sprintf("message number %d", i)})),
		})
	}
	app.timeline = messages

	unscrolledLines := strings.Split(app.View(), "\n")
	if len(unscrolledLines) != app.height {
		t.Fatalf("unscrolled height = %d, want %d", len(unscrolledLines), app.height)
	}

	app.scrollOffset = 5
	scrolledLines := strings.Split(app.View(), "\n")
	if len(scrolledLines) != app.height {
		t.Fatalf("scrolled height = %d, want %d (should not grow past the frame)", len(scrolledLines), app.height)
	}
	last := scrolledLines[len(scrolledLines)-1]
	if !strings.Contains(last, "ctrl+p") {
		t.Fatalf("footer should still be the last line once scrolled, got last line %q\nfull view:\n%s",
			last, strings.Join(scrolledLines, "\n"))
	}
}

// TestShortConversationAnchorsPromptToBottom guards against a regression
// where a short conversation (fewer messages than fit on screen) rendered
// starting at row 0, leaving the prompt/input floating a few rows below the
// last message with a large unused gap beneath it instead of at the very
// bottom of the terminal — TS's timeline is a stickyScroll="bottom"
// scrollbox, so short conversations anchor to the bottom with any slack
// above the messages, not below the prompt.
func TestShortConversationAnchorsPromptToBottom(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1"}
	app.width, app.height = 100, 40
	app.timeline = []client.Message{
		{ID: "msg_u", SessionID: "ses_1", Type: "user", Seq: 0, Data: json.RawMessage(`{"text":"hello"}`)},
	}

	view := ansi.Strip(app.View()) // see TestHomeShowsLogoAndPrompt's comment
	lines := strings.Split(view, "\n")
	promptRow := -1
	for i, line := range lines {
		if strings.Contains(line, "Ask anything") {
			promptRow = i
			break
		}
	}
	if promptRow == -1 {
		t.Fatalf("prompt placeholder not found in view:\n%s", view)
	}
	// The prompt box plus footer take a handful of rows below it; anywhere
	// in the bottom quarter of the screen is "anchored to the bottom",
	// anywhere near the top (as when unrendered space was appended below
	// instead of above) is the bug.
	if want := app.height * 3 / 4; promptRow < want {
		t.Fatalf("prompt at row %d, want it in the bottom quarter (>= row %d) of a %d-row screen:\n%s",
			promptRow, want, app.height, view)
	}
}

func TestPageScrolling(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	// The scroll now clamps to the content above the viewport, so the
	// timeline has to be longer than the screen for there to be anywhere to
	// go — an empty one correctly refuses to scroll at all.
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp"}
	for i := range 40 {
		app.timeline = append(app.timeline, settledAssistant(t, fmt.Sprintf("m%d", i), "line"))
	}

	before := app.scrollOffset
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyPgUp})
	if app.scrollOffset <= before {
		t.Fatal("pageup should scroll the timeline up")
	}
	// messages_page_up is a *full* page upstream, not the half page this port
	// used to scroll.
	if app.scrollOffset != app.viewportHeight() {
		t.Fatalf("pageup scrolled %d lines, want a full page of %d", app.scrollOffset, app.viewportHeight())
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyPgDown})
	if app.scrollOffset != before {
		t.Fatalf("pagedown should scroll back, got offset %d", app.scrollOffset)
	}
}

// TestHeadlessProgramRun boots the real Bubble Tea program without a TTY,
// renders frames, and quits — catching startup/render panics.
func TestHeadlessProgramRun(t *testing.T) {
	_, server := newMockAPI(t)
	app := New(context.Background(), client.New(server.URL), "gocode-dark")
	p := tea.NewProgram(program{app: app}, tea.WithInput(nil), tea.WithOutput(io.Discard))
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for quit := false; !quit; {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			quit = true
		case <-deadline:
			t.Fatal("program did not quit in time")
		default:
			p.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestCommandPalette(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 120, 80 // tall enough to list every command
	drive(t, app, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})

	if app.overlay == nil {
		t.Fatal("ctrl+p should open the command palette")
	}
	view := app.View()
	for _, want := range []string{"session.new", "model.list", "theme.list", "help.show"} {
		if !strings.Contains(view, want) {
			t.Fatalf("palette missing command %s: %q", want, view)
		}
	}

	// filter narrows the list
	press(t, app, "t")
	press(t, app, "h")
	if got := len(app.overlay.items); got >= len(app.overlay.all) {
		t.Fatalf("filter should narrow commands, got %d of %d", got, len(app.overlay.all))
	}
}

func TestSlashCommandExecutes(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	for _, r := range "/help" {
		press(t, app, string(r))
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.overlay == nil || app.overlay.kind != overlayHelp {
		t.Fatal("/help should open the help dialog")
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEscape})

	for _, r := range "/nope" {
		press(t, app, string(r))
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !strings.Contains(app.statusMsg, "unknown command") {
		t.Fatalf("unknown slash command should report, got %q", app.statusMsg)
	}
	_ = api
}

func TestRenameDialog(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	drive(t, app, tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if app.overlay == nil || app.overlay.kind != overlayInput {
		t.Fatal("ctrl+r should open the rename dialog")
	}
	press(t, app, "!")
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(api.renamed) != 1 || api.renamed[0].title != "Test session!" {
		t.Fatalf("expected rename submitted, got %+v", api.renamed)
	}
	if app.active.Title != "Test session!" {
		t.Fatalf("title should update locally, got %q", app.active.Title)
	}
}

func TestModelDialogSwitchesModel(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	cmd := app.modelsOverlay()
	driveCmd(t, app, cmd)
	if app.overlay == nil {
		t.Fatal("expected model dialog")
	}
	view := app.View()
	if !strings.Contains(view, "Claude Sonnet 4.5") {
		t.Fatalf("model dialog should list catalog models, got %q", view)
	}

	// select the first model
	app.overlay.selected = 0
	chosen := app.overlay.items[app.overlay.selected]
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = chosen
	// The session's own Model field is the source of truth once a session
	// is active (the prompt box reads it directly via currentModelParts) —
	// not a.activeModel, which is reserved for a pending pick made before
	// any session exists (see TestModelDialogFromHomeAppliesToNewSession).
	if app.active.Model == nil {
		t.Fatal("choosing a model should update the session's Model field")
	}
	if app.activeModelSet() {
		t.Error("a.activeModel should stay empty once the session's own Model field was updated")
	}
	if len(api.models) != 1 {
		t.Fatalf("expected SetModel API call, got %+v", api.models)
	}
}

// TestModelDialogFromHomeAppliesToNewSession is the regression for "in home
// view it is not able to change [the model] at all": picking a model before
// any session exists used to be a pure no-op (SetModel needs a session ID
// the home view doesn't have yet). It must instead be remembered and pinned
// to whichever session gets created for the first prompt.
func TestModelDialogFromHomeAppliesToNewSession(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	if app.view != viewHome {
		t.Fatal("test expects to start on the home view")
	}

	driveCmd(t, app, app.modelsOverlay())
	if app.overlay == nil {
		t.Fatal("expected model dialog")
	}
	app.overlay.selected = 0
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if !app.activeModelSet() {
		t.Fatal("choosing a model from the home view should record a pending choice")
	}
	providerID, modelID, ok := app.currentModelParts()
	if !ok || providerID == "" || modelID == "" {
		t.Fatalf("prompt box should reflect the pending choice immediately, got %q/%q ok=%v", providerID, modelID, ok)
	}

	for _, r := range "hi" {
		press(t, app, string(r))
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if app.active == nil {
		t.Fatal("expected a session to be created")
	}
	if app.active.Model == nil || app.active.Model.ProviderID != providerID || app.active.Model.ID != modelID {
		t.Fatalf("new session should be pinned to the chosen model, got %+v", app.active.Model)
	}
	if app.activeModelSet() {
		t.Error("a.activeModel should be cleared once consumed by the new session")
	}
	if len(api.models) != 1 || api.models[0].sessionID != app.active.ID {
		t.Fatalf("expected one SetModel call pinning the new session, got %+v", api.models)
	}
}

// TestSkillsDialogOpensViaSlashCommand covers "/skills" reaching the picker
// the same way a user would type it: through the palette command table
// (dialogs.go's commandsRegistry), not by calling skillsOverlay directly.
func TestSkillsDialogOpensViaSlashCommand(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)

	var entry overlayItem
	for _, item := range app.commandsRegistry() {
		if item.slash == "skills" {
			entry = item
			break
		}
	}
	if entry.action == nil {
		t.Fatal("expected a \"skills\" palette entry with slash=\"skills\"")
	}
	driveCmd(t, app, runItemAction(entry))

	if app.overlay == nil || app.overlay.title != "Skills" {
		t.Fatalf("expected the Skills dialog to open, got overlay %+v", app.overlay)
	}
	view := app.View()
	if !strings.Contains(view, "chunk-sidecar") || !strings.Contains(view, "artifact-design") {
		t.Fatalf("skills dialog should list discovered skills, got %q", view)
	}
	if !strings.Contains(view, "Validate on a remote sidecar") {
		t.Fatalf("skills dialog should show descriptions, got %q", view)
	}
}

// TestSkillDialogSelectionInsertsSlashCommand: selecting a skill writes
// "/<name> " into the prompt — the same convention as any other custom
// slash command — rather than running it immediately or inserting an
// @mention. Every discovered skill is command-invocable this way regardless
// of its Slash frontmatter flag (see internal/command's skill merge), so the
// dialog does not filter or badge on it.
func TestSkillDialogSelectionInsertsSlashCommand(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)

	driveCmd(t, app, app.skillsOverlay())
	if app.overlay == nil {
		t.Fatal("expected skills dialog")
	}
	// Skills are sorted by name: "artifact-design" before "chunk-sidecar".
	app.overlay.selected = 0
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})

	if got := app.input.Value(); got != "/artifact-design " {
		t.Fatalf("prompt = %q, want \"/artifact-design \"", got)
	}
	if app.overlay != nil {
		t.Error("selecting a skill should close the dialog")
	}
}

// TestSkillsDialogShowsLoadError mirrors DialogSkill's error state: a failed
// fetch must not just render an empty "no skills found" list, which reads as
// "this project genuinely has none" rather than "the request failed".
func TestSkillsDialogShowsLoadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	app := newTestApp(t, server.URL)

	driveCmd(t, app, app.skillsOverlay())
	if app.overlay == nil {
		t.Fatal("expected skills dialog")
	}
	if app.overlay.emptyTitle != "Could not load skills" {
		t.Errorf("emptyTitle = %q, want the load-failure message", app.overlay.emptyTitle)
	}
	if !app.overlay.locked {
		t.Error("a failed load should lock the dialog rather than let it look searchable")
	}
}

func TestTimelineDialogForks(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	armLeader(t, app)
	press(t, app, "g")
	if app.overlay == nil {
		t.Fatal("leader+g should open the timeline dialog")
	}
	view := app.View()
	if !strings.Contains(view, "Timeline") || !strings.Contains(view, "hello") {
		t.Fatalf("timeline should list user prompts, got %q", view)
	}

	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if api.forkedFrom == "" {
		t.Fatal("selecting a timeline entry should fork from that message")
	}
	if app.active == nil || app.active.ID != "ses_fork" {
		t.Fatalf("fork should open the child session, got %+v", app.active)
	}
}

func TestChildrenDialog(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	driveCmd(t, app, app.childrenOverlay())
	if app.overlay == nil {
		t.Fatal("expected forked-sessions dialog")
	}
	view := app.View()
	if !strings.Contains(view, "child") {
		t.Fatalf("children dialog should list forked sessions, got %q", view)
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.active == nil || app.active.ID != "ses_child" {
		t.Fatalf("enter should open the child session, got %+v", app.active)
	}
	_ = api
}

func TestCompactCommand(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	armLeader(t, app)
	press(t, app, "c")
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter}) // flush
	if api.compacts != 1 {
		t.Fatalf("leader+c should call compact, got %d calls", api.compacts)
	}
	if app.statusMsg != "context compacted" {
		t.Fatalf("expected compaction toast, got %q", app.statusMsg)
	}
}

func TestToastLifecycle(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	// Update directly (not drive) so the expiry tick is not executed.
	app.Update(statusMsg{text: "hello toast"})
	if app.toast == nil || app.toast.text != "hello toast" {
		t.Fatal("status should surface as a toast")
	}
	if !time.Now().Before(app.toast.expires) {
		t.Fatal("toast should expire in the future")
	}
	app.toast.expires = time.Now().Add(-time.Second)
	drive(t, app, toastExpiredMsg{})
	if app.toast != nil {
		t.Fatal("expired toast should clear")
	}
}

func TestStatusOverlay(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	armLeader(t, app)
	press(t, app, "s")
	if app.overlay == nil || app.overlay.kind != overlayStatus {
		t.Fatal("leader+s should open the status view")
	}
	view := app.View()
	for _, want := range []string{"Status", "MCP Servers", "Formatters", "Plugins"} {
		if !strings.Contains(view, want) {
			t.Fatalf("status view missing %q: %q", want, view)
		}
	}
}

// TestEventPumpKeepsFlowing proves the pump goroutine forwards a stream of
// events (not just the first), so live deltas accumulate in the model.
func TestEventPumpKeepsFlowing(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	// Snapshots are applied against the active session, so open one first.
	openSession(t, app)
	p := tea.NewProgram(program{app: app}, tea.WithInput(nil), tea.WithOutput(io.Discard))
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()

	events := make(chan client.Event, 8)
	snapshots := make(chan Snapshot, 8)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go Aggregate(ctx, events, snapshots, time.Millisecond)
	go pumpSnapshots(p, snapshots, nil)
	events <- client.Event{Type: "session.next.step.started", Session: "ses_1"}
	events <- client.Event{Type: "session.next.text.delta", Session: "ses_1", Data: map[string]any{
		"assistantMessageID": "msg_x", "delta": "Hel",
	}}
	events <- client.Event{Type: "session.next.text.delta", Session: "ses_1", Data: map[string]any{
		"assistantMessageID": "msg_x", "delta": "lo",
	}}

	time.Sleep(500 * time.Millisecond) // allow the runtime to process the stream
	p.Send(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("program did not quit")
	}
	// p.Run has returned: all Update calls happen-before this read.
	if got := app.streaming["msg_x"]; got == nil || got.String() != "Hello" {
		t.Fatalf("expected accumulated deltas through the pump, got %v", app.streaming["msg_x"])
	}
	if !app.busy {
		t.Fatal("step.started should mark the session busy")
	}
}

// Styled lines must never leak raw escape codes after truncation.
func TestTruncateRunesANSISafe(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Render("Build · glm-5.3")
	got := truncateRunes(styled, 10)
	for i := 0; i < len(got); i++ {
		if got[i] == 0x1b {
			j := i
			for j < len(got) && got[j] != 'm' {
				j++
			}
			if j >= len(got) {
				t.Fatalf("unterminated escape sequence after truncation: %q", got)
			}
			i = j
		}
	}
	if visible := lipgloss.Width(got); visible > 10 {
		t.Fatalf("truncated string too long: %d cells", visible)
	}
}

// The whole layout must fit the terminal width when the sidebar is visible.
func TestLayoutFitsTerminal(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.width, app.height = 155, 38
	openSession(t, app)
	view := app.View()
	lines := strings.Split(view, "\n")
	max := 0
	for _, line := range lines {
		if w := lipgloss.Width(line); w > max {
			max = w
		}
	}
	if max > 155 {
		t.Fatalf("layout overflows terminal: %d > 155", max)
	}
	if !strings.Contains(view, "Test session") {
		t.Fatal("sidebar content missing")
	}
}

// Terminal resized after start: the layout must re-wrap to the new width.
func TestLayoutRewrapsOnResize(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	long := "Hello! 👋 I'm ready to help you with coding tasks — whether that's writing code, debugging, refactoring, exploring a codebase, or anything else. What would you like to work on?"
	app.timeline = []client.Message{{
		ID: "msg_a", SessionID: "ses_1", Type: "assistant", Seq: 1,
		Data: json.RawMessage(`{"agent":"build","model":{"providerID":"zai-coding-plan","id":"glm-5.3"},"content":[{"type":"text","id":"t","text":` + mustJSON(long) + `}],"finish":"stop"}`),
	}}

	app.width, app.height = 192, 46
	app.View() // render at the old size

	drive(t, app, tea.WindowSizeMsg{Width: 143, Height: 54})
	view := app.View()
	lines := strings.Split(view, "\n")
	for i, line := range lines {
		if w := lipgloss.Width(line); w > 143 {
			t.Fatalf("line %d is %d cells wide after resize to 143: %q", i, w, line)
		}
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
