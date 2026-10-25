package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
		case r.Method == http.MethodDelete:
			json.NewEncoder(w).Encode(map[string]any{"sessions": 1})
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
	userData, _ := json.Marshal(map[string]any{"text": "Fix the login redirect"})
	app.timeline = []client.Message{{ID: "m1", Type: "user", Data: userData}}

	app.harnessCommand("refine")
	o := app.overlay
	if o == nil || o.Kind != dialog.KindInput || o.InputValue() != "Fix the login redirect" {
		t.Fatalf("input = %+v", o)
	}
	applyCmd(t, app, app.handleOverlayKey("enter"))
	if !state.saw("POST /api/harness/run") {
		t.Fatal("the plan was not requested")
	}
	if app.overlay == nil || app.overlay.Kind != dialog.KindConfirm {
		t.Fatalf("confirm = %+v", app.overlay)
	}
	if panel := strings.Join(panelLines(t, app), " "); !strings.Contains(panel, "rollouts") || !strings.Contains(panel, "not touched") {
		t.Fatalf("confirm does not state the budget:\n%s", panel)
	}
	state.mu.Lock()
	first := state.bodies[len(state.bodies)-1]
	state.mu.Unlock()
	if first["dryRun"] != true || first["workspace"] != "/work/proj" {
		t.Fatalf("plan body = %v", first)
	}
}

func TestHarnessStageLines(t *testing.T) {
	lines := harnessStageLines(client.HarnessStage{
		Files: client.HarnessFiles{Added: []string{"skills/x/SKILL.md"}}, Notes: "why\tthis",
		Diff: "+a", Deliverable: "done", Delivered: true, CostUSD: 0.5,
	})
	text := strings.Join(lines, "\n")
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
