package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/langazov/gocode-go/internal/harness"
	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/dialog"
)

func sampleHarnessRuns() []client.HarnessRun {
	return []client.HarnessRun{
		{ID: "fix-login--k1-parallel", Task: "fix-login", Domain: "auth", K: 1, Mode: "parallel", Status: "ok",
			Backend: "local", Adopted: "agg1", CostUSD: 1.25, Budget: "parallel k=1 → 4 rollouts, 3 editor sessions",
			StartedAt: time.Unix(100, 0),
			Rows: []client.HarnessRow{
				{Label: "base1", Phase: "base", State: "ok", CostUSD: 0.2},
				{Label: "inv1", Phase: "probe", State: "ok", CostUSD: 0.3},
				{Label: "dom1", Phase: "probe", State: "failed", Detail: "fell back: invalid skill"},
				{Label: "agg1", Phase: "compose", State: "ok", CostUSD: 0.4},
				{Label: "experience", Phase: "reflect", State: "ok"},
			}},
		{ID: "add-docs--k2-sequential", Task: "add-docs", Domain: "docs", K: 2, Mode: "sequential", Status: "running",
			Phase: "probe", Backend: "split", StartedAt: time.Unix(200, 0),
			Rows: []client.HarnessRow{{Label: "base1", Phase: "base", State: "ok"}, {Label: "inv1", Phase: "probe", State: "editing"}}},
	}
}

// harnessServer stubs the /api/harness routes the dialogs call.
type harnessServer struct {
	mu       sync.Mutex
	runs     []client.HarnessRun
	requests []string
	bodies   []map[string]any
}

func newHarnessServer(t *testing.T, runs []client.HarnessRun) (*harnessServer, string) {
	t.Helper()
	state := &harnessServer{runs: runs}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.requests = append(state.requests, r.Method+" "+r.URL.Path)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		state.bodies = append(state.bodies, body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/harness/run":
			json.NewEncoder(w).Encode(state.runs)
		case r.Method == http.MethodPost && r.URL.Path == "/api/harness/run":
			run := client.HarnessRun{ID: "new--k1-parallel", Task: "new", Domain: "proj", K: 1, Mode: "parallel",
				Budget: "parallel k=1 → 4 rollouts, 3 editor sessions", Backend: "local", Reason: "library-plugin not loaded"}
			if body["dryRun"] == true {
				run.Status = "planned"
			} else {
				run.Status = "running"
			}
			json.NewEncoder(w).Encode(run)
		case strings.Contains(r.URL.Path, "/stage/"):
			json.NewEncoder(w).Encode(client.HarnessStage{Label: "agg1", Diff: "+check the answer", SessionID: "ses_rollout",
				Files: client.HarnessFiles{Modified: []string{"systemprompt.md"}}, Deliverable: "done", Delivered: true})
		case strings.HasSuffix(r.URL.Path, "/log"):
			all := []client.HarnessLogEvent{
				{Type: "phase", Time: 1000, Phase: "base"},
				{Type: "session", Time: 2000, Label: "inv1", Role: "harness-editor", SessionID: "ses_edit"},
				{Type: "stage.retry", Time: 3000, Label: "inv1", Attempt: 1, Detail: "skill 'x' is never named"},
				{Type: "rollout.ended", Time: 4000, Label: "inv1", OK: true, CostUSD: 0.25},
			}
			after, _ := strconv.Atoi(r.URL.Query().Get("after"))
			if after > len(all) {
				after = len(all)
			}
			json.NewEncoder(w).Encode(all[after:])
		case strings.HasPrefix(r.URL.Path, "/api/session/"):
			json.NewEncoder(w).Encode(client.Session{ID: strings.TrimPrefix(r.URL.Path, "/api/session/"), Title: "harness rollout"})
		case r.Method == http.MethodDelete:
			json.NewEncoder(w).Encode(map[string]any{"sessions": 1})
		case r.URL.Path == "/api/harness/installed":
			json.NewEncoder(w).Encode(client.HarnessInstalled{Installed: true, Dir: "/p/.gocode/harness", Fingerprint: "a3bd62d2f70f",
				Skills: []string{"lint"}, Hooks: 2, Tree: map[string]string{"systemprompt.md": "be precise"}})
		case strings.HasSuffix(r.URL.Path, "/compare"):
			json.NewEncoder(w).Encode(client.HarnessComparison{From: r.URL.Query().Get("with"), To: "fix-login--k1-parallel · agg1",
				Files: client.HarnessFiles{Modified: []string{"systemprompt.md"}}, Diff: "--- installed/systemprompt.md\n+++ agg1/systemprompt.md\n-old\n+new"})
		case r.URL.Path == "/api/harness/sweep" && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]client.HarnessSweep{})
		case r.URL.Path == "/api/harness/sweep" && r.Method == http.MethodPost:
			sweep := client.HarnessSweep{ID: "20261026T100000Z", Source: "tasks", K: 1, Mode: "parallel", Workers: 1,
				DomainOrder: []string{"math"}, Budget: "parallel k=1 → 4 rollouts, 3 editor sessions", Rollouts: 8, Edits: 6,
				Tasks: []client.HarnessSweepTask{
					{Task: "one", Domain: "math", RunID: "one--k1-parallel", Status: "pending"},
					{Task: "two", Domain: "math", RunID: "two--k1-parallel", Status: "pending"},
				}}
			if body["dryRun"] == true {
				sweep.Status, sweep.ID = "planned", ""
			} else {
				sweep.Status, sweep.StartedAt = "running", time.Now()
			}
			json.NewEncoder(w).Encode(sweep)
		case strings.HasSuffix(r.URL.Path, "/resume"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/harness/run/"), "/resume")
			json.NewEncoder(w).Encode(client.HarnessRun{ID: id, Task: strings.Split(id, "--")[0], Status: "running",
				Mode: "parallel", K: 1, StartedAt: time.Now()})
		case strings.HasSuffix(r.URL.Path, "/install"):
			json.NewEncoder(w).Encode(client.HarnessInstall{Target: "/p/.gocode/harness",
				Files: client.HarnessFiles{Added: []string{"systemprompt.md"}}, Written: body["dryRun"] != true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return state, server.URL
}

func (h *harnessServer) saw(request string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.requests {
		if r == request {
			return true
		}
	}
	return false
}

func TestHarnessRunsListGroupsRunningFirst(t *testing.T) {
	_, url := newHarnessServer(t, sampleHarnessRuns())
	app := newTestApp(t, url)
	driveCmd(t, app, app.harnessOverlay())
	o := app.overlay
	if o == nil || o.Title != harnessRunsTitle {
		t.Fatal("/harness did not open the runs list")
	}
	items := o.Items()
	if len(items) != 2 || items[0].Category != "Running" || items[1].Category != "Finished" {
		t.Fatalf("rows = %+v", items)
	}
	if items[0].Gutter != "◐" || items[1].Gutter != "✓" {
		t.Errorf("gutters = %q %q", items[0].Gutter, items[1].Gutter)
	}
	if !strings.Contains(items[1].Hint, "adopted agg1") || !strings.Contains(items[0].Hint, "probe") {
		t.Errorf("hints = %q / %q", items[0].Hint, items[1].Hint)
	}
}

func TestHarnessRunsEmptyStateSaysHowToStart(t *testing.T) {
	_, url := newHarnessServer(t, nil)
	app := newTestApp(t, url)
	driveCmd(t, app, app.harnessOverlay())
	if !strings.Contains(app.overlay.EmptyBody(), "/harness refine") {
		t.Fatalf("empty body = %q", app.overlay.EmptyBody())
	}
}

func TestHarnessRunPlanGroupsByPhase(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.harnessRuns = sampleHarnessRuns()
	app.openHarnessRun("fix-login--k1-parallel")
	o := app.overlay
	if o == nil || !strings.HasPrefix(o.Title, harnessRunTitle) {
		t.Fatal("run plan did not open")
	}
	categories := map[string]string{}
	for _, item := range o.Items() {
		categories[item.Value] = item.Category
	}
	if categories["base1"] != "Base rollouts" || categories["agg1"] != "Compose" || categories["experience"] != "Reflect" {
		t.Fatalf("categories = %v", categories)
	}
	var adopted overlayItem
	for _, item := range o.Items() {
		if item.Value == "agg1" {
			adopted = item
		}
	}
	if !strings.Contains(adopted.Label, "adopted") {
		t.Errorf("adopted row label = %q", adopted.Label)
	}
	// install is offered for a finished run; publish only with the Library.
	titles := strings.Join(actionTitles(o), ",")
	if !strings.Contains(titles, "install") || strings.Contains(titles, "publish") || strings.Contains(titles, "cancel") {
		t.Fatalf("actions = %s", titles)
	}
	app.openHarnessRun("add-docs--k2-sequential")
	titles = strings.Join(actionTitles(app.overlay), ",")
	if strings.Contains(titles, "install") || !strings.Contains(titles, "cancel") {
		t.Fatalf("running run actions = %s", titles)
	}
}

func actionTitles(o *dialog.Shell) []string {
	var out []string
	for _, action := range o.Actions() {
		out = append(out, action.Title)
	}
	return out
}

// Live events move the cache and the open plan; a run that stops says so.
func TestHarnessSnapshotUpdatesOpenRunAndToasts(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.harnessRuns = sampleHarnessRuns()
	app.openHarnessRun("add-docs--k2-sequential")

	live := sampleHarnessRuns()[1]
	live.Rows = append([]client.HarnessRow(nil), live.Rows...)
	live.Rows[1].State = "ok"
	if app.applyHarnessSnapshot(Snapshot{Harness: map[string]client.HarnessRun{live.ID: live}, HarnessSeq: 1}) != nil {
		t.Fatal("a run still running must not toast")
	}
	for _, item := range app.overlay.Items() {
		if item.Value == "inv1" && item.Gutter != "✓" {
			t.Fatalf("open plan not refreshed: %+v", item)
		}
	}
	if seg := app.harnessFooterSegment(); !strings.Contains(seg, "add-docs") || !strings.Contains(seg, "2/2") {
		t.Fatalf("footer segment = %q", seg)
	}

	live.Status, live.Adopted = "ok", "c2"
	if app.applyHarnessSnapshot(Snapshot{Harness: map[string]client.HarnessRun{live.ID: live}, HarnessSeq: 2}) == nil {
		t.Fatal("a finished run must toast")
	}
	if app.toast == nil || !strings.Contains(app.toast.text, "adopted c2") {
		t.Fatalf("toast = %+v", app.toast)
	}
	if app.harnessFooterSegment() != "" {
		t.Fatal("footer still shows a finished run")
	}
	// The same counter again is the same frame: nothing to do.
	if app.applyHarnessSnapshot(Snapshot{HarnessSeq: 2}) != nil {
		t.Fatal("an unchanged harness counter re-applied")
	}
}

// The aggregator keeps the newest run state per id from harness.* events.
func TestAggregatorKeepsLatestHarnessRun(t *testing.T) {
	tr := newTree()
	for _, state := range []string{"editing", "ok"} {
		event := client.Event{Type: "harness.stage.ended", Data: map[string]any{
			"run": map[string]any{"id": "x--k1-parallel", "task": "x", "status": "running",
				"rows": []any{map[string]any{"label": "inv1", "state": state}}},
		}}
		if !tr.apply(event) {
			t.Fatal("a harness event did not register")
		}
	}
	snap := tr.snapshot(0)
	if snap.HarnessSeq != 2 || snap.Harness["x--k1-parallel"].Rows[0].State != "ok" {
		t.Fatalf("snapshot = %+v", snap.Harness)
	}
	if tr.apply(client.Event{Type: "harness.phase", Data: map[string]any{}}) {
		t.Fatal("a harness event without a run registered")
	}
}

// /harness refine prefills the task from the session, plans it, and asks
// before spending anything.
func TestHarnessRefinePlansThenConfirms(t *testing.T) {
	state, url := newHarnessServer(t, nil)
	app := newTestApp(t, url)
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "t", Directory: "/work/proj"}
	app.activeModel = "anthropic/claude-test"
	userData, _ := json.Marshal(map[string]any{"text": "Fix the login redirect"})
	app.timeline = []client.Message{{ID: "m1", Type: "user", Data: userData}}

	app.harnessCommand("refine")
	o := app.overlay
	if o == nil || o.Kind != dialog.KindInput || o.InputValue() != "Fix the login redirect" {
		t.Fatalf("input = %+v", o)
	}
	applyCmd(t, app, app.handleOverlayKey("enter"))
	if app.overlay == nil || app.overlay.Title != "Harness run budget" {
		t.Fatalf("budget picker did not open: %+v", app.overlay)
	}
	if len(app.overlay.Items()) != len(harness.Budgets()) {
		t.Fatalf("budgets = %d", len(app.overlay.Items()))
	}
	// Pick sequential k=2: down twice from the cheapest.
	app.handleOverlayKey("down")
	app.handleOverlayKey("down")
	applyCmd(t, app, app.handleOverlayKey("enter"))
	if !state.saw("POST /api/harness/run") {
		t.Fatal("the plan was not requested")
	}
	if app.overlay == nil || app.overlay.Kind != dialog.KindConfirm {
		t.Fatalf("confirm = %+v", app.overlay)
	}
	if panel := strings.Join(panelLines(t, app), " "); !strings.Contains(panel, "rollouts") || !strings.Contains(panel, "touched") {
		t.Fatalf("confirm does not state the budget:\n%s", panel)
	}
	state.mu.Lock()
	first := state.bodies[len(state.bodies)-1]
	state.mu.Unlock()
	if first["dryRun"] != true || first["workspace"] != "/work/proj" || first["mode"] != "sequential" || first["k"] != float64(2) ||
		first["model"] != "anthropic/claude-test" {
		t.Fatalf("plan body = %v", first)
	}
}

func TestHarnessStageLines(t *testing.T) {
	lines := harnessStageLines(client.HarnessStage{
		Files: client.HarnessFiles{Added: []string{"skills/x/SKILL.md"}}, Notes: "why\tthis",
		Diff: "+a", Deliverable: "done", Delivered: true, CostUSD: 0.5,
	})
	text := strings.Join(panelText(lines), "\n")
	for _, want := range []string{"HARNESS", "+ skills/x/SKILL.md", "EDITOR NOTES", "why    this", "HARNESS DIFF", "ROLLOUT · DELIVERED"} {
		if !strings.Contains(text, want) {
			t.Errorf("stage lines lack %q:\n%s", want, text)
		}
	}
}

// remove arms first; on a finished run the second press deletes it.
func TestHarnessRemoveArmsThenDeletes(t *testing.T) {
	state, url := newHarnessServer(t, sampleHarnessRuns())
	app := newTestApp(t, url)
	driveCmd(t, app, app.harnessOverlay())
	finished := overlayItem{Value: "fix-login--k1-parallel"}
	if cmd := app.removeHarnessAction(finished); cmd != nil || app.overlay.Armed() != finished.Value {
		t.Fatal("the first press must only arm")
	}
	applyCmd(t, app, app.removeHarnessAction(finished))
	if !state.saw("DELETE /api/harness/run/fix-login--k1-parallel") {
		t.Fatalf("no delete sent: %v", state.requests)
	}
}

func TestToolInputSummary(t *testing.T) {
	if got := toolInputSummary(map[string]any{"command": "go  test\n ./..."}); got != "go test ./..." {
		t.Fatalf("command summary = %q", got)
	}
	if got := toolInputSummary(map[string]any{"filePath": "/w/a.go", "content": "x"}); got != "/w/a.go" {
		t.Fatalf("path summary = %q", got)
	}
	if toolInputSummary("not a map") != "" {
		t.Fatal("non-map input summarised")
	}
}

// A working row says what its session is doing now: the running tool and
// its argument, from the relayed session events.
func TestHarnessRowShowsLiveActivity(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	run := sampleHarnessRuns()[1]
	run.Rows = []client.HarnessRow{
		{Label: "inv1", Phase: "probe", State: "editing", EditSessionID: "ses_edit", Retries: 1},
		{Label: "dom1", Phase: "probe", State: "rolling", SessionID: "ses_roll"},
		{Label: "agg1", Phase: "compose", State: "pending"},
	}
	app.harnessRuns = []client.HarnessRun{run}
	app.agents = Snapshot{Sessions: map[string]*SessionNode{
		"ses_edit": {ID: "ses_edit", Busy: true, Tools: map[string]ToolState{
			"c1": {CallID: "c1", Name: "read", Status: "completed", Input: "old.md"},
			"c2": {CallID: "c2", Name: "read", Status: "pending", Input: "context/rollouts/base1.md"},
		}},
		"ses_roll": {ID: "ses_roll", Busy: true, Tools: map[string]ToolState{}},
	}}
	app.openHarnessRun(run.ID)
	hints := map[string]string{}
	for _, item := range app.overlay.Items() {
		hints[item.Value] = item.Hint
	}
	if !strings.Contains(hints["inv1"], "read context/rollouts/base1.md") || !strings.Contains(hints["inv1"], "↻1") {
		t.Errorf("editing row hint = %q", hints["inv1"])
	}
	if !strings.Contains(hints["dom1"], "thinking") {
		t.Errorf("rolling row hint = %q", hints["dom1"])
	}
	if strings.Contains(hints["agg1"], "·") {
		t.Errorf("a pending row shows activity: %q", hints["agg1"])
	}

	// A new tool in the editor's session rebuilds the open plan.
	app.agents.Sessions["ses_edit"].Tools["c3"] = ToolState{CallID: "c3", Name: "grep", Status: "pending", Input: "deliverable"}
	app.refreshHarnessActivity()
	for _, item := range app.overlay.Items() {
		if item.Value == "inv1" && !strings.Contains(item.Hint, "grep deliverable") {
			t.Fatalf("plan not refreshed: %q", item.Hint)
		}
	}
}

// watch opens the session a row is working in now; editor opens its
// editor's session.
func TestHarnessWatchPicksTheLiveSession(t *testing.T) {
	if got := harnessRowSession(client.HarnessRow{State: "editing", SessionID: "r", EditSessionID: "e"}, false); got != "e" {
		t.Fatalf("editing row watches %q", got)
	}
	if got := harnessRowSession(client.HarnessRow{State: "rolling", SessionID: "r", EditSessionID: "e"}, false); got != "r" {
		t.Fatalf("rolling row watches %q", got)
	}
	if got := harnessRowSession(client.HarnessRow{State: "ok", SessionID: "r", EditSessionID: "e"}, true); got != "e" {
		t.Fatalf("editor request watches %q", got)
	}
	state, url := newHarnessServer(t, nil)
	app := newTestApp(t, url)
	run := sampleHarnessRuns()[0]
	run.Rows = []client.HarnessRow{{Label: "base1", Phase: "base", State: "ok", SessionID: "ses_base"}}
	app.harnessRuns = []client.HarnessRun{run}
	if cmd := app.watchHarnessRow(run.ID, "base1", true); cmd == nil {
		t.Fatal("no feedback for a row without an editor session")
	}
	msg := app.watchHarnessRow(run.ID, "base1", false)()
	if _, ok := msg.(dialog.CloseThenMsg); !ok || !state.saw("GET /api/session/ses_base") {
		t.Fatalf("watch = %#v, requests %v", msg, state.requests)
	}
}

// The log lists every event and follows new ones while open.
func TestHarnessLogFollows(t *testing.T) {
	_, url := newHarnessServer(t, sampleHarnessRuns())
	app := newTestApp(t, url)
	app.harnessRuns = sampleHarnessRuns()
	id := "add-docs--k2-sequential"
	msg := app.openHarnessLog(id)().(harnessLogMsg)
	msg.events = msg.events[:2] // as if only two had happened yet
	app.handleHarnessMsg(msg)
	if !app.harnessLogOpen() || len(app.harnessLog) != 2 {
		t.Fatalf("log not open: %+v", app.overlay)
	}
	text := strings.Join(panelLines(t, app), "\n")
	if !strings.Contains(text, "inv1 harness-editor session ses_edit") {
		t.Fatalf("log panel:\n%s", text)
	}
	cmd := app.applyHarnessSnapshot(Snapshot{HarnessSeq: app.harnessSeq + 1, Harness: map[string]client.HarnessRun{}})
	if cmd == nil {
		t.Fatal("an open log did not ask for new events")
	}
	more := cmd().(harnessLogMsg)
	app.handleHarnessMsg(more)
	if len(app.harnessLog) != 4 {
		t.Fatalf("log has %d events after following, want 4", len(app.harnessLog))
	}
	lines := harnessLogLines(app.harnessLog)
	if !strings.Contains(lines[2], "↻ inv1 attempt 1 rejected · skill 'x' is never named") || !strings.Contains(lines[3], "✓ rollout.ended inv1 · $0.25") {
		t.Fatalf("lines = %q", lines)
	}
}

// A finished run leaves the snapshots a minute after its last event; a
// running one never does.
func TestAggregatorForgetsFinishedRuns(t *testing.T) {
	tr := newTree()
	for _, run := range []map[string]any{
		{"id": "done--k1-parallel", "task": "done", "status": "ok"},
		{"id": "live--k1-parallel", "task": "live", "status": "running"},
	} {
		tr.apply(client.Event{Type: "harness.stage.ended", Data: map[string]any{"run": run}})
	}
	if snap := tr.snapshot(0); len(snap.Harness) != 2 {
		t.Fatalf("fresh runs = %d, want both", len(snap.Harness))
	}
	for id := range tr.harnessAt {
		tr.harnessAt[id] = time.Now().Add(-2 * harnessRetention)
	}
	snap := tr.snapshot(0)
	if _, ok := snap.Harness["done--k1-parallel"]; ok {
		t.Fatal("a finished run outlived its retention")
	}
	if _, ok := snap.Harness["live--k1-parallel"]; !ok {
		t.Fatal("a running run was dropped")
	}
}

// A working row whose session has gone quiet says so, in minutes, long
// before the run's idle limit stops it; the tick moves it on without events.
func TestHarnessRowSaysWhenQuiet(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	run := sampleHarnessRuns()[1]
	run.Rows = []client.HarnessRow{
		{Label: "dom1", Phase: "probe", State: "rolling", SessionID: "ses_roll"},
		{Label: "dom2", Phase: "probe", State: "rolling", SessionID: "ses_busy"},
	}
	app.harnessRuns = []client.HarnessRun{run}
	app.agents = Snapshot{Sessions: map[string]*SessionNode{
		"ses_roll": {ID: "ses_roll", Busy: true, LastEvent: time.Now().Add(-3 * time.Minute), Tools: map[string]ToolState{
			"c1": {CallID: "c1", Name: "bash", Status: "pending", Input: "go test ./..."},
		}},
		"ses_busy": {ID: "ses_busy", Busy: true, LastEvent: time.Now(), Tools: map[string]ToolState{}},
	}}
	app.openHarnessRun(run.ID)
	hints := func() map[string]string {
		out := map[string]string{}
		for _, item := range app.overlay.Items() {
			out[item.Value] = item.Hint
		}
		return out
	}
	if got := hints()["dom1"]; !strings.Contains(got, "bash go test ./... · quiet 3m") {
		t.Errorf("quiet row hint = %q", got)
	}
	if got := hints()["dom2"]; strings.Contains(got, "quiet") {
		t.Errorf("a busy row says quiet: %q", got)
	}

	// No event arrives; time passes; the tick refreshes the plan.
	app.agents.Sessions["ses_busy"].LastEvent = time.Now().Add(-5 * time.Minute)
	app.refreshHarnessActivity()
	if got := hints()["dom2"]; !strings.Contains(got, "thinking · quiet 5m") {
		t.Errorf("after the tick: %q", got)
	}
}

// A running run says how long it has been going: in the runs list, the
// plan's placeholder line and the hint row.
func TestHarnessRunShowsItsAge(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	runs := sampleHarnessRuns()
	runs[1].StartedAt = time.Now().Add(-72 * time.Minute)
	app.harnessRuns = runs
	app.harnessLoaded = true
	app.openHarnessRuns()
	for _, item := range app.overlay.Items() {
		switch item.Value {
		case runs[1].ID:
			if !strings.Contains(item.Hint, "1h 12m") {
				t.Errorf("running hint = %q", item.Hint)
			}
		case runs[0].ID:
			if strings.Contains(item.Hint, "m ") || strings.HasSuffix(item.Hint, "m") {
				t.Errorf("a finished run shows an age: %q", item.Hint)
			}
		}
	}
	if footer := app.harnessFooterSegment(); !strings.Contains(footer, "1h 12m") {
		t.Errorf("footer = %q", footer)
	}
	for d, want := range map[time.Duration]string{30 * time.Second: "<1m", 12 * time.Minute: "12m", 65 * time.Minute: "1h 05m"} {
		if got := harnessElapsed(d); got != want {
			t.Errorf("harnessElapsed(%s) = %q, want %q", d, got, want)
		}
	}
}

// resume confirms with the budget first, then asks the server to resume;
// it is offered only for runs that stopped short of ok.
func TestHarnessResumeConfirmsThenResumes(t *testing.T) {
	runs := sampleHarnessRuns()
	runs[0].Status = "partial"
	state, url := newHarnessServer(t, runs)
	app := newTestApp(t, url)
	app.harnessRuns = runs

	app.openHarnessRun(runs[0].ID)
	if !hasAction(app, "resume") {
		t.Fatal("a partial run offers no resume")
	}
	app.openHarnessRun(runs[1].ID)
	if hasAction(app, "resume") {
		t.Fatal("a running run offers resume")
	}

	applyCmd(t, app, app.resumeHarnessRun(runs[0].ID))
	if app.overlay == nil || app.overlay.Kind != dialog.KindConfirm {
		t.Fatalf("no confirm: %+v", app.overlay)
	}
	if panel := strings.Join(panelLines(t, app), " "); !strings.Contains(panel, "rollouts") || !strings.Contains(panel, "reused") {
		t.Fatalf("confirm does not state the budget and reuse:\n%s", panel)
	}
	if state.saw("POST /api/harness/run/" + runs[0].ID + "/resume") {
		t.Fatal("resumed before the confirm")
	}
	applyCmd(t, app, app.handleOverlayKey("enter"))
	if !state.saw("POST /api/harness/run/" + runs[0].ID + "/resume") {
		t.Fatalf("no resume sent: %v", state.requests)
	}
	if run, ok := app.harnessRunByID(runs[0].ID); !ok || run.Status != "running" {
		t.Fatalf("resumed run = %+v", run)
	}
}

func hasAction(app *App, title string) bool {
	for _, action := range app.overlay.Actions() {
		if action.Title == title {
			return true
		}
	}
	return false
}

// The stage panel opens on what went wrong, then the harness, notes, diff
// and the rollout — its words wrapped, its changes colored as a diff.
func TestHarnessStagePanelOrderAndTones(t *testing.T) {
	lines := harnessStageLines(client.HarnessStage{
		Error: "stopped after 15m0s without activity: the session stalled",
		Files: client.HarnessFiles{Added: []string{"skills/x/SKILL.md"}, Removed: []string{"old.md"}},
		Notes: "tightened the guardrails", Diff: "--- base/a\n+++ inv1/a\n@@ -1 +1 @@\n-old\n+new",
		Deliverable: "# Final message\n\nDone.\n\n# Changes to the workspace\n\n1 file(s) changed: a.go\n\n```diff\n-x\n+y\n```\n",
		Delivered:   true,
	})
	if lines[0].Text != "ERROR" || lines[1].Tone != toneError {
		t.Fatalf("the error does not come first: %+v", lines[:2])
	}
	tones := map[string]panelTone{}
	for _, line := range lines {
		tones[line.Text] = line.Tone
	}
	for text, want := range map[string]panelTone{
		"+ skills/x/SKILL.md": toneAdd, "- old.md": toneDel, "+new": toneAdd, "-old": toneDel,
		"@@ -1 +1 @@": toneHunk, "+++ inv1/a": toneMuted, "Done.": toneBody, "+y": toneAdd,
		"CHANGES TO THE WORKSPACE": toneHeading, "tightened the guardrails": toneBody,
	} {
		if got, ok := tones[text]; !ok || got != want {
			t.Errorf("%q: tone %v (present %v), want %v", text, got, ok, want)
		}
	}
	if strings.Contains(strings.Join(panelText(lines), "\n"), "# Final message") {
		t.Error("the deliverable's own heading leaked into the panel")
	}
}

// A stage's evidence is whatever the rollout wrote. Raw control bytes in it —
// CRLF, an agent's colored output, a screen clear — moved the terminal's
// cursor behind the renderer, and the panel's cells outlived closing it.
func TestHarnessStagePanelStripsControlBytes(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 200, 50
	app.handleHarnessMsg(harnessStageMsg{stage: client.HarnessStage{Label: "agg2",
		Diff:        "+line one\r\n+\x1b[31mred\x1b[0m\r\n+bell\a\b\tend",
		Notes:       "notes with\r carriage",
		Deliverable: "# Final message\n\nok \x1b[2J\x1b[H cleared\r\n"}})
	out, _ := app.overlay.Panel()
	for _, bad := range []string{"\r", "\a", "\b", "\t", "\x1b[2J", "\x1b[H", "\x1b[31m"} {
		if strings.Contains(out, bad) {
			t.Errorf("panel leaks %q", bad)
		}
	}
	if plain := ansi.Strip(out); !strings.Contains(plain, "+red") || !strings.Contains(plain, "ok cleared") {
		t.Errorf("the text around the control bytes was lost:\n%s", plain)
	}
	app.handleOverlayKey("esc")
	if app.overlay != nil {
		t.Fatal("esc did not close the stage panel")
	}
}

// A failed row leads with a short reason; a working row is footed with its
// session's step and tool counts.
func TestHarnessRowReasonsAndCounts(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	run := sampleHarnessRuns()[1]
	run.Rows = []client.HarnessRow{
		{Label: "base1", Phase: "base", State: "failed", Detail: "stopped after 15m0s without activity: the session stalled"},
		{Label: "base2", Phase: "base", State: "failed", Detail: "stopped after 2h0m0s: the session did not finish in time"},
		{Label: "dom1", Phase: "probe", State: "rolling", SessionID: "ses_roll"},
	}
	app.harnessRuns = []client.HarnessRun{run}
	app.agents = Snapshot{Sessions: map[string]*SessionNode{
		"ses_roll": {ID: "ses_roll", Busy: true, LastEvent: time.Now(), Steps: 12, ToolCalls: 30, ToolFailures: 2, Tools: map[string]ToolState{}},
	}}
	app.openHarnessRun(run.ID)
	items := map[string]overlayItem{}
	for _, item := range app.overlay.Items() {
		items[item.Value] = item
	}
	if got := items["base1"].Hint; got != "failed · stalled (no activity)" {
		t.Errorf("stalled row = %q", got)
	}
	if got := items["base2"].Hint; got != "failed · timed out" {
		t.Errorf("timed-out row = %q", got)
	}
	if got := items["dom1"].Footer; got != "12 steps · 30 tools · 2 failed" {
		t.Errorf("working row footer = %q", got)
	}
}

// The installed harness opens from the runs list, and the hint row names
// the one the open session runs under when no run is going.
func TestHarnessInstalledPanelAndHint(t *testing.T) {
	state, url := newHarnessServer(t, nil)
	app := newTestApp(t, url)
	driveCmd(t, app, app.openInstalledHarness())
	if !state.saw("GET /api/harness/installed") || app.overlay == nil || app.overlay.Title != "Installed harness" {
		t.Fatalf("installed panel = %+v", app.overlay)
	}
	if panel := strings.Join(panelLines(t, app), " "); !strings.Contains(panel, "a3bd62d2f70f") || !strings.Contains(panel, "be precise") {
		t.Fatalf("installed panel:\n%s", panel)
	}

	marker, _ := json.Marshal(client.HarnessData{Loaded: true, Dir: "/p/.gocode/harness", Fingerprint: "a3bd62d2f70f"})
	app.timeline = []client.Message{{ID: "m1", Type: "harness", Data: marker}}
	if got := ansi.Strip(app.harnessFooterSegment()); got != "harness a3bd62d2f70f" {
		t.Errorf("hint = %q", got)
	}
	removed, _ := json.Marshal(client.HarnessData{})
	app.timeline = append(app.timeline, client.Message{ID: "m2", Type: "harness", Data: removed})
	if got := app.harnessFooterSegment(); got != "" {
		t.Errorf("hint after removal = %q", got)
	}
}

// compare picks what to compare with, then shows the diff.
func TestHarnessCompare(t *testing.T) {
	state, url := newHarnessServer(t, sampleHarnessRuns())
	app := newTestApp(t, url)
	app.harnessRuns = sampleHarnessRuns()
	app.openHarnessRun("fix-login--k1-parallel")
	if !hasAction(app, "compare") {
		t.Fatal("a finished run offers no compare")
	}
	app.openHarnessCompare("fix-login--k1-parallel")
	if app.overlay == nil || !strings.HasPrefix(app.overlay.Title, "Compare fix-login") || len(app.overlay.Items()) != 1 {
		t.Fatalf("compare picker = %+v", app.overlay)
	}
	driveCmd(t, app, app.handleOverlayKey("enter"))
	if !state.saw("GET /api/harness/run/fix-login--k1-parallel/compare") {
		t.Fatalf("no compare request: %v", state.requests)
	}
	if panel := strings.Join(panelLines(t, app), " "); !strings.Contains(panel, "~ systemprompt.md") {
		t.Fatalf("compare panel:\n%s", panel)
	}
	lines := harnessCompareLines(client.HarnessComparison{From: "installed", Files: client.HarnessFiles{Modified: []string{"a"}}, Diff: "-old\n+new"})
	if last := lines[len(lines)-1]; last.Text != "+new" || last.Tone != toneAdd {
		t.Fatalf("compare diff = %+v", last)
	}
}

// A sweep is planned before it is started: task files, budget, dry run,
// then a confirm with the totals; its view lists tasks by domain.
func TestHarnessSweepPlansThenStarts(t *testing.T) {
	state, url := newHarnessServer(t, nil)
	app := newTestApp(t, url)
	driveCmd(t, app, app.harnessSweepsOverlay())
	if app.overlay == nil || app.overlay.Title != harnessSweepsTitle {
		t.Fatalf("sweeps list = %+v", app.overlay)
	}
	app.newHarnessSweep()
	if o := app.overlay; o == nil || o.InputValue() != "tasks" {
		t.Fatalf("input = %+v", o)
	}
	driveCmd(t, app, app.handleOverlayKey("enter"))
	if app.overlay == nil || app.overlay.Title != "Harness sweep budget" {
		t.Fatalf("budget picker = %+v", app.overlay)
	}
	driveCmd(t, app, app.handleOverlayKey("enter"))
	if app.overlay == nil || app.overlay.Kind != dialog.KindConfirm {
		t.Fatalf("confirm = %+v", app.overlay)
	}
	panel := strings.Join(panelLines(t, app), " ")
	for _, want := range []string{"2 tasks in 1 domain", "math: one → two", "In all: 8", "not touched"} {
		if !strings.Contains(panel, want) {
			t.Errorf("confirm lacks %q:\n%s", want, panel)
		}
	}
	state.mu.Lock()
	planned := state.bodies[len(state.bodies)-1]
	state.mu.Unlock()
	if planned["dryRun"] != true || fmt.Sprint(planned["paths"]) != "[tasks]" {
		t.Fatalf("plan body = %v", planned)
	}
	// applyCmd stops at the status line; driveCmd would sit out its timer.
	applyCmd(t, app, app.handleOverlayKey("enter"))
	if app.overlay == nil || !strings.HasPrefix(app.overlay.Title, harnessSweepTitle+" · tasks") {
		t.Fatalf("sweep view = %+v", app.overlay)
	}
	if items := app.overlay.Items(); len(items) != 2 || items[0].Category != "math" || items[0].Gutter != "·" {
		t.Fatalf("sweep tasks = %+v", items)
	}
}

// A sweep event updates the open sweep view, like a run event updates a plan.
func TestAggregatorCarriesSweeps(t *testing.T) {
	tree := newTree()
	sweep := client.HarnessSweep{ID: "s1", Status: "running", Tasks: []client.HarnessSweepTask{{Task: "one", Status: "running"}}}
	if !tree.apply(client.Event{Type: "harness.sweep", Data: map[string]any{"sweep": sweep}}) {
		t.Fatal("a sweep event changed nothing")
	}
	snap := tree.snapshot(0)
	if got := snap.HarnessSweeps["s1"]; got.Status != "running" || len(got.Tasks) != 1 {
		t.Fatalf("snapshot sweep = %+v", got)
	}

	app := newTestApp(t, "http://example.invalid")
	app.harnessSweeps = []client.HarnessSweep{{ID: "s1", Status: "running", Tasks: []client.HarnessSweepTask{{Task: "one", Status: "pending"}}}}
	app.openHarnessSweep("s1")
	app.applyHarnessSnapshot(snap)
	if items := app.overlay.Items(); items[0].Hint != "running" {
		t.Fatalf("open sweep view not refreshed: %+v", items)
	}
}
