package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// taskTestApp builds an app with one assistant message containing a task
// tool call linked to a child session, the way the runner projects it after
// the task tool's SetMeta publish.
func taskTestApp(t *testing.T, taskStatus string, metadata map[string]any) *App {
	t.Helper()
	if metadata == nil {
		metadata = map[string]any{"sessionID": "ses_child_1", "parentSessionID": "ses_1"}
	}
	data, err := json.Marshal(map[string]any{
		"agent": "build", "finish": "stop",
		"model": map[string]string{"providerID": "anthropic", "id": "claude"},
		"content": []map[string]any{{
			"type": "tool", "id": "call_task", "name": "task",
			"state": map[string]any{
				"status":   taskStatus,
				"input":    map[string]any{"description": "find the bug", "subagent_type": "general"},
				"metadata": metadata,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp"}
	app.timeline = []client.Message{
		{ID: "m_task", Type: "assistant", TimeCreated: 1, Data: data},
	}
	return app
}

// childTimeline builds a child session's messages: one user message, then an
// assistant message with the given tool parts.
func childTimeline(t *testing.T, tools []map[string]any, userAt, assistantDone int64) []client.Message {
	t.Helper()
	content := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		content = append(content, tool)
	}
	assistant, err := json.Marshal(map[string]any{
		"agent": "general", "finish": "stop",
		"model":   map[string]string{"providerID": "anthropic", "id": "claude"},
		"content": content,
		"time":    map[string]any{"created": userAt + 1, "completed": assistantDone},
	})
	if err != nil {
		t.Fatal(err)
	}
	return []client.Message{
		{ID: "c_user", Type: "user", TimeCreated: userAt, Data: json.RawMessage(`{"text":"go find it"}`)},
		{ID: "c_assistant", Type: "assistant", TimeCreated: userAt + 1, Data: assistant},
	}
}

func readToolPart(id, path string) map[string]any {
	return map[string]any{
		"type": "tool", "id": id, "name": "read",
		"state": map[string]any{"status": "completed", "input": map[string]any{"path": path}},
	}
}

// A running task with a linked child shows the child's live tool under the
// label — the port of Task()'s `↳ ${tool} ${title}` line.
func TestTaskRowShowsChildProgress(t *testing.T) {
	app := taskTestApp(t, "running", nil)
	app.childMessages["ses_child_1"] = childTimeline(t,
		[]map[string]any{readToolPart("c_t1", "internal/session/runner.go")}, 1000, 0)

	plain := ansi.Strip(strings.Join(app.timelineLines(), "\n"))
	if !strings.Contains(plain, "General Task — find the bug") {
		t.Fatalf("task label missing:\n%s", plain)
	}
	if !strings.Contains(plain, "↳ Read internal/session/runner.go") {
		t.Fatalf("live child progress missing:\n%s", plain)
	}
}

// A completed task summarizes the child's work: toolcall count and duration.
func TestTaskRowShowsCompletionSummary(t *testing.T) {
	app := taskTestApp(t, "completed", nil)
	tools := []map[string]any{
		readToolPart("c_t1", "a.go"),
		readToolPart("c_t2", "b.go"),
	}
	app.childMessages["ses_child_1"] = childTimeline(t, tools, 1000, 14000)

	plain := ansi.Strip(strings.Join(app.timelineLines(), "\n"))
	if !strings.Contains(plain, "↳ 2 toolcalls · 13.0s") {
		t.Fatalf("completion summary missing:\n%s", plain)
	}
	if strings.Contains(plain, "General Task —") && strings.Count(plain, "General Task —") != 1 {
		t.Fatalf("label rendered more than once:\n%s", plain)
	}
}

// Without the metadata link (a task call from before the seam existed, or a
// child whose timeline has not loaded yet) the row is just the label — no
// crash, no fabricated progress.
func TestTaskRowDegradesWithoutLink(t *testing.T) {
	app := taskTestApp(t, "running", map[string]any{})
	plain := ansi.Strip(strings.Join(app.timelineLines(), "\n"))
	if !strings.Contains(plain, "General Task — find the bug") {
		t.Fatalf("label missing:\n%s", plain)
	}
	if strings.Contains(plain, "↳") {
		t.Fatalf("no child known, but a sub-line rendered:\n%s", plain)
	}
}

// Messages carrying a task call show the "view subagents" hint row (the
// settled ones too — the subagent's session stays openable after it
// finished, which is the whole point of the hint). The background segment
// shows only while a foreground task runs under an enabled server.
func TestTaskHintRow(t *testing.T) {
	app := taskTestApp(t, "completed", nil)
	plain := ansi.Strip(strings.Join(app.timelineLines(), "\n"))
	if !strings.Contains(plain, "view subagents") {
		t.Fatalf("hint row missing under a task-bearing message:\n%s", plain)
	}
	if strings.Contains(plain, "ctrl+b background") {
		t.Fatalf("background hint shown for a settled task:\n%s", plain)
	}

	running := taskTestApp(t, "running", nil)
	plain = ansi.Strip(strings.Join(running.timelineLines(), "\n"))
	if strings.Contains(plain, "ctrl+b background") {
		t.Fatalf("background hint shown while background mode is unavailable:\n%s", plain)
	}

	running.backgroundModeAvailable = true
	plain = ansi.Strip(strings.Join(running.timelineLines(), "\n"))
	if !strings.Contains(plain, "ctrl+b background") {
		t.Fatalf("background hint missing while a foreground task runs:\n%s", plain)
	}

	// An already-detached task is not a foreground one.
	detached := taskTestApp(t, "running", map[string]any{
		"sessionID":  "ses_child_1",
		"background": true,
	})
	detached.backgroundModeAvailable = true
	plain = ansi.Strip(strings.Join(detached.timelineLines(), "\n"))
	if strings.Contains(plain, "ctrl+b background") {
		t.Fatalf("background hint shown for an already-background task:\n%s", plain)
	}
}

// The whole task block is a click target: buildTimeline records its rows to
// the child session, and taskClickTarget resolves them the way a mouse click
// arrives (against viewChat's window).
func TestTaskRowClickOpensChild(t *testing.T) {
	app := taskTestApp(t, "running", nil)
	app.childMessages["ses_child_1"] = childTimeline(t,
		[]map[string]any{readToolPart("c_t1", "runner.go")}, 1000, 0)

	_, _, _, taskRows := app.buildTimeline()
	if len(taskRows) == 0 {
		t.Fatal("no task rows recorded for a linked task call")
	}
	found := false
	for _, childID := range taskRows {
		if childID == "ses_child_1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("task rows point elsewhere than the child session: %v", taskRows)
	}

	// End to end through the window arithmetic viewChat records: render the
	// chat view, then click one of the rows the map holds.
	app.viewChat()
	for row, childID := range app.chatTaskRows {
		if childID != "ses_child_1" {
			continue
		}
		if got, ok := app.taskClickTarget(row + app.chatWindowPad); ok {
			if got != "ses_child_1" {
				t.Fatalf("click target = %q, want the child session", got)
			}
			return
		}
		t.Fatalf("row %d recorded but not hit-testable (pad %d, start %d)", row, app.chatWindowPad, app.chatWindowStart)
	}
	t.Fatal("chatTaskRows is empty after viewChat")
}

// trackChildSessions picks the linked children out of a refreshed timeline
// and fetches each once — the point where a running subagent first becomes
// watchable.
func TestTrackChildSessionsFetchesOnce(t *testing.T) {
	app := taskTestApp(t, "running", nil)
	if len(app.childMessages) != 0 {
		t.Fatal("fixture should start with nothing tracked")
	}
	cmd := app.trackChildSessions(app.timeline)
	if cmd == nil {
		t.Fatal("a linked child on the timeline should trigger a fetch")
	}
	if _, tracked := app.childMessages["ses_child_1"]; !tracked {
		t.Fatal("the child should be registered synchronously")
	}
	// A second pass over the same timeline must not re-fetch.
	if app.trackChildSessions(app.timeline) != nil {
		t.Fatal("an already-tracked child was fetched again")
	}
}

// taskChildIDs is the link scanner: order-preserving, duplicate-free.
func TestTaskChildIDs(t *testing.T) {
	app := taskTestApp(t, "completed", nil)
	ids := taskChildIDs(app.timeline)
	if len(ids) != 1 || ids[0] != "ses_child_1" {
		t.Fatalf("taskChildIDs = %v, want [ses_child_1]", ids)
	}

	// Two calls, same child (a resumed task): one entry.
	second, err := json.Marshal(map[string]any{
		"agent": "build", "finish": "stop",
		"content": []map[string]any{{
			"type": "tool", "id": "call_task2", "name": "task",
			"state": map[string]any{
				"status":   "running",
				"metadata": map[string]any{"sessionID": "ses_child_1"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.timeline = append(app.timeline, client.Message{ID: "m2", Type: "assistant", Data: second})
	ids = taskChildIDs(app.timeline)
	if len(ids) != 1 {
		t.Fatalf("duplicate child tracked: %v", ids)
	}
}

// A child's permission request surfaces in the parent's banner (index.tsx's
// children().flatMap(x => sync.data.permission[x.id])), attributed with the
// child's title, and replying settles it in the child's own session.
func TestChildAskSurfacesInParentView(t *testing.T) {
	app := taskTestApp(t, "running", nil)
	// The child must be tracked (the timeline's task link) for the merge to
	// accept it — that is also the attribution source.
	app.trackChildSessions(app.timeline)
	app.activeChildren = []client.Session{{
		ID: "ses_child_1", ParentID: "ses_1", Title: "find the bug (@general subagent)",
		Directory: "/tmp", Version: "1",
	}}

	child := client.PermissionRequest{
		ID: "per_child", SessionID: "ses_child_1", Action: "edit", Resources: []string{"internal/session/runner.go"},
	}
	app.applyPermissions([]client.PermissionRequest{child})
	if app.permission == nil {
		t.Fatal("a tracked child's permission request must surface in the parent view")
	}
	if app.permission.ID != "per_child" {
		t.Fatalf("banner holds %q, want the child's request", app.permission.ID)
	}

	// Attribution: the banner names the subagent that is asking.
	_, title := app.permissionTitle(app.permission)
	if !strings.Contains(title, "find the bug (@general subagent)") {
		t.Fatalf("child ask is not attributed with the subagent's title: %q", title)
	}
	if !strings.Contains(title, "Edit internal/session/runner.go") {
		t.Fatalf("the action itself is missing from the attributed title: %q", title)
	}
}

// A request from a session that is neither the open one nor its child never
// takes the banner — the merge is scoped, not global.
func TestForeignAskNeverTakesBanner(t *testing.T) {
	app := taskTestApp(t, "running", nil)
	app.applyPermissions([]client.PermissionRequest{{
		ID: "per_foreign", SessionID: "ses_unrelated", Action: "edit", Resources: []string{"x.go"},
	}})
	if app.permission != nil {
		t.Fatal("a foreign session's request took the parent's banner")
	}
}

// The open session's own ask wins over a child's when both are pending: it is
// the older ask in practice (a subagent's tool runs inside its parent turn)
// and the list the loader builds puts it first.
func TestOwnAskWinsOverChildAsk(t *testing.T) {
	app := taskTestApp(t, "running", nil)
	app.trackChildSessions(app.timeline)
	app.applyPermissions([]client.PermissionRequest{
		{ID: "per_own", SessionID: "ses_1", Action: "bash", Resources: []string{"ls"}},
		{ID: "per_child", SessionID: "ses_child_1", Action: "edit", Resources: []string{"x.go"}},
	})
	if app.permission == nil || app.permission.ID != "per_own" {
		t.Fatalf("the session's own ask should hold the banner, got %+v", app.permission)
	}
}

// The session list excludes subagent sessions (and forks): they belong to
// their parent's task row and the children overlay, not the switcher.
func TestSessionListExcludesChildren(t *testing.T) {
	app := taskTestApp(t, "running", nil)
	app.sessions = []client.Session{
		{ID: "ses_root_b", Title: "later root", Directory: "/tmp", TimeCreated: 3},
		{ID: "ses_child_1", ParentID: "ses_1", Title: "subagent", Directory: "/tmp", TimeCreated: 2},
		{ID: "ses_root_a", Title: "earlier root", Directory: "/tmp", TimeCreated: 1},
	}
	app.sessionsOverlay()
	if app.overlay == nil {
		t.Fatal("overlay did not open")
	}
	ids := make([]string, 0, len(app.overlay.items))
	for _, item := range app.overlay.items {
		ids = append(ids, item.value)
	}
	for _, id := range ids {
		if id == "ses_child_1" {
			t.Fatalf("child session listed in the switcher: %v", ids)
		}
	}
	if len(ids) != 2 {
		t.Fatalf("expected the two root sessions, got %v", ids)
	}
}

// ctrl+b promotes the open session's foreground subagents (session.background):
// it posts to the endpoint, reports how many were promoted, and refreshes the
// timeline whose task rows settle to "Background task started".
func TestBackgroundSubagentsCommand(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	app.view = viewChat
	app.backgroundModeAvailable = true
	openSession(t, app)
	// The mock's message list has no task call; swap in one.
	app.timeline = taskTestApp(t, "running", nil).timeline

	cmd := app.backgroundSubagents()
	if cmd == nil {
		t.Fatal("the command should run")
	}
	status := findStatus(t, cmd())
	if !strings.Contains(status, "moved to the background") {
		t.Fatalf("status = %q, want the promotion count", status)
	}
	if api.backgrounded != "ses_1" {
		t.Fatalf("promotion posted for %q, want ses_1", api.backgrounded)
	}
}

// findStatus digs the first statusMsg out of a command's result, following
// nested batch commands so a composite result still yields its report.
func findStatus(t *testing.T, msg tea.Msg) string {
	t.Helper()
	switch value := msg.(type) {
	case statusMsg:
		return value.text
	case tea.BatchMsg:
		for _, cmd := range value {
			if cmd == nil {
				continue
			}
			if inner := cmd(); inner != nil {
				if text := findStatus(t, inner); text != "" {
					return text
				}
			}
		}
	case tea.Cmd:
		if inner := value(); inner != nil {
			return findStatus(t, inner)
		}
	}
	t.Fatalf("no statusMsg in %T", msg)
	return ""
}

// The command explains itself when there is nothing to promote or the server
// runs without the feature.
func TestBackgroundSubagentsGuards(t *testing.T) {
	app := taskTestApp(t, "completed", nil)
	app.backgroundModeAvailable = true
	msg := drainCmd(t, app.backgroundSubagents())
	if status, ok := msg.(statusMsg); !ok || !strings.Contains(status.text, "no foreground subagents") {
		t.Fatalf("settled task: got %v", msg)
	}

	running := taskTestApp(t, "running", nil)
	msg = drainCmd(t, running.backgroundSubagents())
	if status, ok := msg.(statusMsg); !ok || !strings.Contains(status.text, "disabled") {
		t.Fatalf("background mode off: got %v", msg)
	}
}

// drainCmd runs one tea.Cmd to its message synchronously, failing the test
// on nil (a silent command) or a batch (the caller must flatten it first).
func drainCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("command is nil")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("command produced no message")
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		t.Fatalf("command returned a batch of %d; the caller wants one message", len(batch))
	}
	return msg
}
