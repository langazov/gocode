package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/harness"
)

// routeExec writes answer.txt in the workspace; routeImprover edits the
// system prompt. Enough for one k=1 run end to end.
type routeExec struct{}

func (routeExec) Rollout(_ context.Context, req harness.RolloutRequest) (harness.Rollout, error) {
	if req.OnSession != nil {
		req.OnSession("ses_" + req.Label)
	}
	_ = os.WriteFile(filepath.Join(req.Workspace, "answer.txt"), []byte("42\n"), 0o644)
	return harness.Rollout{OK: true, Final: "done", Trajectory: "[ASSISTANT] done", SessionID: "ses_" + req.Label}, nil
}

type routeImprover struct{}

func (routeImprover) Session(_ context.Context, req harness.SessionRequest) (harness.Reply, error) {
	if req.OnSession != nil {
		req.OnSession("ses_edit_" + req.Label)
	}
	if req.Role == harness.RoleEditor {
		_ = os.WriteFile(filepath.Join(req.Workspace, "context", "harness", harness.FileSystemPrompt), []byte("check the answer\n"), 0o644)
		return harness.Reply{Text: "<notes>edited</notes>"}, nil
	}
	return harness.Reply{Text: "<experience>\ncue: c\nlesson: l\nevidence: e\n</experience>"}, nil
}

func TestHarnessRoutes(t *testing.T) {
	server, _, bus := newTestServer(t)
	root := t.TempDir()
	store := &harness.LocalStore{Root: t.TempDir()}
	workspace := t.TempDir()
	server.VCSWorkdir = workspace
	installed := ""
	server.Harness = &harness.Service{
		Root: root,
		NewEngine: func(context.Context, harness.Launch) (*harness.Engine, harness.Detection, error) {
			return &harness.Engine{Exec: routeExec{}, Improver: routeImprover{}, Store: store, Root: root},
				harness.Detection{Backend: harness.BackendLocal, Reason: "test"}, nil
		},
		Store: func(context.Context) (harness.ExperienceStore, harness.Detection, error) {
			return store, harness.Detection{Backend: harness.BackendLocal}, nil
		},
		Install: func(_ context.Context, summary *harness.Summary, req harness.InstallRequest) (harness.InstallResult, error) {
			installed = summary.Adopted
			return harness.InstallResult{Target: "x", Written: !req.DryRun}, nil
		},
	}
	server.Harness.Publish = HarnessPublisher(bus)
	events, unsubscribe := bus.Subscribe(256)
	defer unsubscribe()

	rec := doJSON(t, server, "POST", "/api/harness/run", map[string]any{"prompt": "Write the answer", "dryRun": true})
	var planned harness.RunState
	_ = json.Unmarshal(rec.Body.Bytes(), &planned)
	if rec.Code != 200 || planned.Status != "planned" || len(planned.Rows) != 5 || !strings.Contains(planned.Budget, "4 rollouts") {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}

	// S4: the budgets the GUI's picker serves.
	rec = doJSON(t, server, "GET", "/api/harness/budgets", nil)
	var budgets []harness.Budget
	_ = json.Unmarshal(rec.Body.Bytes(), &budgets)
	if rec.Code != 200 || len(budgets) != len(harness.Budgets()) || budgets[0].Mode != harness.ModeParallel || budgets[0].Rollouts != 4 {
		t.Fatalf("budgets: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, server, "POST", "/api/harness/run", map[string]any{"prompt": "Write the answer", "id": "answer"})
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var started harness.RunState
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	if started.ID != "answer--k1-parallel" || started.Status != "running" {
		t.Fatalf("started = %+v", started)
	}

	var final harness.RunState
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec = doJSON(t, server, "GET", "/api/harness/run/"+started.ID, nil)
		_ = json.Unmarshal(rec.Body.Bytes(), &final)
		if final.Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != "ok" || final.Adopted != "agg1" || final.Lesson != "c" {
		t.Fatalf("final = %+v", final)
	}
	for _, row := range final.Rows {
		if row.State != harness.RowOK {
			t.Errorf("row %s is %s", row.Label, row.State)
		}
		if row.Phase != "reflect" && row.SessionID != "ses_"+row.Label {
			t.Errorf("row %s rollout session = %q", row.Label, row.SessionID)
		}
		if (row.Label == "inv1" || row.Label == "agg1") && !strings.HasPrefix(row.EditSessionID, "ses_edit_"+row.Label) {
			t.Errorf("row %s editor session = %q", row.Label, row.EditSessionID)
		}
	}

	var log []harness.Event
	rec = doJSON(t, server, "GET", "/api/harness/run/"+started.ID+"/log", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &log)
	if rec.Code != 200 || len(log) < 10 || log[0].Type != harness.EventPhase {
		t.Fatalf("log: %d, %d events", rec.Code, len(log))
	}
	var tail []harness.Event
	rec = doJSON(t, server, "GET", fmt.Sprintf("/api/harness/run/%s/log?after=%d", started.ID, len(log)-2), nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &tail)
	if len(tail) != 2 {
		t.Fatalf("log?after: %d events, want 2", len(tail))
	}

	sawHarnessEvent := false
drain:
	for {
		select {
		case payload := <-events:
			if strings.HasPrefix(payload.Type, "harness.") {
				sawHarnessEvent = true
			}
		default:
			break drain
		}
	}
	if !sawHarnessEvent {
		t.Fatal("no harness.* event reached the bus")
	}

	rec = doJSON(t, server, "GET", "/api/harness/run", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), started.ID) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	// S2: the listing carries each run's workspace and prompt excerpt.
	var listed []harness.RunState
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	var live *harness.RunState
	for i := range listed {
		if listed[i].ID == started.ID {
			live = &listed[i]
		}
	}
	if live == nil || live.Workspace != workspace || live.Prompt != "Write the answer" {
		t.Fatalf("listed run workspace/prompt: %+v", live)
	}
	rec = doJSON(t, server, "GET", "/api/harness/run/"+started.ID+"/stage/agg1", nil)
	var stage harness.StageDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &stage)
	if rec.Code != 200 || !strings.Contains(stage.Diff, "+check the answer") || !strings.Contains(stage.Deliverable, "+42") {
		t.Fatalf("stage: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, "GET", "/api/harness/run/"+started.ID+"/stage/..%2F..", nil)
	if rec.Code == 200 {
		t.Fatal("a path-escaping label was served")
	}
	rec = doJSON(t, server, "POST", "/api/harness/run/"+started.ID+"/install", map[string]any{"to": "project"})
	if rec.Code != 200 || installed == "" {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	// S1: an explicit dir must be a directory on the server.
	rec = doJSON(t, server, "POST", "/api/harness/run/"+started.ID+"/install", map[string]any{"to": "project", "dir": filepath.Join(t.TempDir(), "missing")})
	if rec.Code != 400 {
		t.Fatalf("install into a missing dir: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, server, "GET", "/api/harness/library", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"task":"answer"`) {
		t.Fatalf("library: %d %s", rec.Code, rec.Body.String())
	}
	domain := harness.Slug(filepath.Base(workspace))
	rec = doJSON(t, server, "GET", "/api/harness/library/"+domain+"/answer", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cue":"c"`) {
		t.Fatalf("entry: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, "DELETE", "/api/harness/library/"+domain+"/answer", nil)
	if rec.Code != 204 {
		t.Fatalf("forget: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, "POST", "/api/harness/library/sync", nil)
	if rec.Code != 409 {
		t.Fatalf("sync on a local store: %d", rec.Code)
	}

	var pruned []string
	server.Harness.Prune = func(_ context.Context, dir string) (int64, error) {
		pruned = append(pruned, dir)
		return 3, nil
	}
	rec = doJSON(t, server, "DELETE", "/api/harness/run/"+started.ID, nil)
	if rec.Code != 200 || len(pruned) != 1 || !strings.HasSuffix(pruned[0], filepath.Join("answer", "k1")) {
		t.Fatalf("delete: %d %s, pruned %v", rec.Code, rec.Body.String(), pruned)
	}
	if _, err := os.Stat(filepath.Join(root, "answer")); !os.IsNotExist(err) {
		t.Fatalf("run directory left behind: %v", err)
	}
	rec = doJSON(t, server, "DELETE", "/api/harness/run/"+started.ID, nil)
	if rec.Code != 404 {
		t.Fatalf("second delete: %d", rec.Code)
	}
}

// stopOnceExec cuts its first base1 rollout short, as an idle limit or an
// interrupt would, and counts every rollout it runs.
type stopOnceExec struct {
	mu      sync.Mutex
	stopped bool
	calls   map[string]int
}

func (e *stopOnceExec) Rollout(ctx context.Context, req harness.RolloutRequest) (harness.Rollout, error) {
	e.mu.Lock()
	e.calls[req.Label]++
	stop := req.Label == "base1" && !e.stopped
	e.stopped = e.stopped || stop
	e.mu.Unlock()
	if stop {
		return harness.Rollout{Error: "stopped after 15m0s without activity: the session stalled", Stopped: true}, nil
	}
	return routeExec{}.Rollout(ctx, req)
}

// A run cut short resumes from its own task: what finished is reused, the
// stopped rollout runs again, and a run that finished ok is refused.
func TestHarnessResumeRoute(t *testing.T) {
	server, _, _ := newTestServer(t)
	root := t.TempDir()
	exec := &stopOnceExec{calls: map[string]int{}}
	server.VCSWorkdir = t.TempDir()
	server.Harness = &harness.Service{
		Root: root,
		NewEngine: func(context.Context, harness.Launch) (*harness.Engine, harness.Detection, error) {
			return &harness.Engine{Exec: exec, Improver: routeImprover{}, Root: root},
				harness.Detection{Backend: harness.BackendLocal}, nil
		},
	}
	wait := func(id string) harness.RunState {
		t.Helper()
		var state harness.RunState
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			rec := doJSON(t, server, "GET", "/api/harness/run/"+id, nil)
			_ = json.Unmarshal(rec.Body.Bytes(), &state)
			if state.Status != "running" {
				return state
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("run %s did not settle", id)
		return state
	}

	rec := doJSON(t, server, "POST", "/api/harness/run", map[string]any{"prompt": "Write the answer", "id": "answer"})
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	const id = "answer--k1-parallel"
	if first := wait(id); first.Status == "ok" {
		t.Fatalf("a run with a stopped base rollout finished ok: %+v", first)
	}
	before := map[string]int{}
	for label, n := range exec.calls {
		before[label] = n
	}

	// A crash: the process died mid-rollout, before the summary was
	// written. The log is all that is left, and the run is still listed —
	// interrupted, the rollout it was in failed — so it can be resumed.
	runDir := filepath.Join(root, "answer", "k1", "parallel")
	if err := os.Remove(filepath.Join(runDir, "summary.json")); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(runDir, harness.LogFile), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = logFile.WriteString(`{"type":"rollout.started","time":1,"label":"base1"}` + "\n")
	_ = logFile.Close()
	var listed []harness.RunState
	_ = json.Unmarshal(doJSON(t, server, "GET", "/api/harness/run", nil).Body.Bytes(), &listed)
	if len(listed) != 1 || listed[0].ID != id || listed[0].Status != "interrupted" {
		t.Fatalf("crashed run listed as %+v", listed)
	}
	if crashed := wait(id); crashed.Rows[0].Label != "base1" || crashed.Rows[0].State != harness.RowFailed ||
		crashed.Rows[0].Detail != harness.ErrInterrupted {
		t.Fatalf("crashed run's base1 = %+v", crashed.Rows[0])
	}

	rec = doJSON(t, server, "POST", "/api/harness/run/"+id+"/resume", nil)
	if rec.Code != 202 {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
	if final := wait(id); final.Status != "ok" {
		t.Fatalf("resumed run = %+v", final)
	}
	if exec.calls["base1"] != before["base1"]+1 {
		t.Errorf("base1 rolled %d times after resume, want once more than %d", exec.calls["base1"], before["base1"])
	}
	for label, n := range before {
		if label != "base1" && exec.calls[label] != n {
			t.Errorf("%s rolled again on resume (%d → %d)", label, n, exec.calls[label])
		}
	}

	rec = doJSON(t, server, "POST", "/api/harness/run/"+id+"/resume", nil)
	if rec.Code != 409 {
		t.Fatalf("resuming a finished run: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, "POST", "/api/harness/run/nothing--k1-parallel/resume", nil)
	if rec.Code != 404 {
		t.Fatalf("resuming an unknown run: %d %s", rec.Code, rec.Body.String())
	}
}

// gateExec holds every rollout until release closes, so a test can look at
// a sweep while its first task runs.
type gateExec struct{ release chan struct{} }

func (g gateExec) Rollout(ctx context.Context, req harness.RolloutRequest) (harness.Rollout, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
		return harness.Rollout{Error: "stopped: " + ctx.Err().Error(), Stopped: true}, nil
	}
	return routeExec{}.Rollout(ctx, req)
}

// A sweep plans from task files, runs each task as a live run, refuses a
// second sweep meanwhile, and is served from its record once finished.
func TestHarnessSweepRoutes(t *testing.T) {
	server, _, _ := newTestServer(t)
	root := t.TempDir()
	server.VCSWorkdir = t.TempDir()
	tasks := filepath.Join(server.VCSWorkdir, "tasks")
	if err := os.MkdirAll(tasks, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"one.md": "---\ndomain: math\n---\nWrite the answer.",
		"two.md": "---\ndomain: math\n---\nWrite it again.",
	} {
		if err := os.WriteFile(filepath.Join(tasks, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gate := gateExec{release: make(chan struct{})}
	server.Harness = &harness.Service{
		Root: root, SweepsDir: t.TempDir(),
		NewEngine: func(context.Context, harness.Launch) (*harness.Engine, harness.Detection, error) {
			return &harness.Engine{Exec: gate, Improver: routeImprover{}, Root: root},
				harness.Detection{Backend: harness.BackendLocal}, nil
		},
	}

	rec := doJSON(t, server, "POST", "/api/harness/sweep", map[string]any{"paths": []string{"tasks"}, "dryRun": true})
	var plan harness.SweepState
	_ = json.Unmarshal(rec.Body.Bytes(), &plan)
	if rec.Code != 200 || plan.Status != "planned" || len(plan.Tasks) != 2 || plan.Tasks[0].Task != "one" ||
		plan.Rollouts != 2*harness.ExpectedRollouts(1) || plan.DomainOrder[0] != "math" {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, "POST", "/api/harness/sweep", map[string]any{"paths": []string{"missing"}})
	if rec.Code != 400 {
		t.Fatalf("a missing task folder: %d", rec.Code)
	}

	rec = doJSON(t, server, "POST", "/api/harness/sweep", map[string]any{"paths": []string{"tasks"}})
	var started harness.SweepState
	_ = json.Unmarshal(rec.Body.Bytes(), &started)
	if rec.Code != 202 || started.Status != "running" || started.ID == "" {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	// While the first task runs it is a live run like any other.
	deadline := time.Now().Add(5 * time.Second)
	live := false
	for !live && time.Now().Before(deadline) {
		rec = doJSON(t, server, "GET", "/api/harness/run", nil)
		live = strings.Contains(rec.Body.String(), `"id":"one--k1-parallel"`) && strings.Contains(rec.Body.String(), `"status":"running"`)
		time.Sleep(10 * time.Millisecond)
	}
	if !live {
		t.Fatalf("the running task is not a live run: %s", rec.Body.String())
	}
	if rec = doJSON(t, server, "POST", "/api/harness/sweep", map[string]any{"paths": []string{"tasks"}}); rec.Code != 409 {
		t.Fatalf("a second sweep: %d %s", rec.Code, rec.Body.String())
	}
	close(gate.release)

	var final harness.SweepState
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec = doJSON(t, server, "GET", "/api/harness/sweep/"+started.ID, nil)
		_ = json.Unmarshal(rec.Body.Bytes(), &final)
		if final.Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != "ok" || final.Done != 2 || final.Tasks[1].Adopted == "" {
		t.Fatalf("final sweep = %+v", final)
	}
	rec = doJSON(t, server, "GET", "/api/harness/sweep", nil)
	var listed []harness.SweepState
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	if rec.Code != 200 || len(listed) != 1 || listed[0].ID != started.ID || listed[0].Status != "ok" {
		t.Fatalf("sweeps: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, server, "GET", "/api/harness/run", nil)
	if !strings.Contains(rec.Body.String(), `"id":"two--k1-parallel"`) {
		t.Fatalf("the sweep's runs are not listed: %s", rec.Body.String())
	}
	if rec = doJSON(t, server, "POST", "/api/harness/sweep/"+started.ID+"/cancel", nil); rec.Code != 404 {
		t.Fatalf("cancelling a finished sweep: %d", rec.Code)
	}
	if rec = doJSON(t, server, "GET", "/api/harness/sweep/..%2Fx", nil); rec.Code != 404 {
		t.Fatalf("a path-escaping sweep id: %d", rec.Code)
	}
}

// The installed harness is described with the fingerprint the chat marker
// carries, and a finished run's adopted harness compares against it.
func TestHarnessInstalledAndCompare(t *testing.T) {
	server, _, _ := newTestServer(t)
	root := t.TempDir()
	server.VCSWorkdir = t.TempDir()
	server.Harness = &harness.Service{
		Root: root,
		NewEngine: func(context.Context, harness.Launch) (*harness.Engine, harness.Detection, error) {
			return &harness.Engine{Exec: routeExec{}, Improver: routeImprover{}, Root: root},
				harness.Detection{Backend: harness.BackendLocal}, nil
		},
	}
	rec := doJSON(t, server, "GET", "/api/harness/installed", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"installed":false`) {
		t.Fatalf("nothing installed: %d %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, server, "POST", "/api/harness/run", map[string]any{"prompt": "Write the answer", "id": "answer"})
	if rec.Code != 202 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rec = doJSON(t, server, "GET", "/api/harness/run/answer--k1-parallel", nil)
		if strings.Contains(rec.Body.String(), `"status":"ok"`) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Nothing installed yet: installing would add the whole harness.
	rec = doJSON(t, server, "GET", "/api/harness/run/answer--k1-parallel/compare", nil)
	var comparison harness.Comparison
	_ = json.Unmarshal(rec.Body.Bytes(), &comparison)
	if rec.Code != 200 || comparison.From != "installed" || !strings.Contains(comparison.Diff, "+check the answer") {
		t.Fatalf("compare with nothing installed: %d %s", rec.Code, rec.Body.String())
	}

	installed := filepath.Join(server.VCSWorkdir, filepath.FromSlash(harness.InstalledDir))
	if _, err := harness.WriteTree(harness.Tree{harness.FileSystemPrompt: "check the answer\n"}, installed, false); err != nil {
		t.Fatal(err)
	}
	rec = doJSON(t, server, "GET", "/api/harness/installed", nil)
	var described struct {
		Installed   bool              `json:"installed"`
		Fingerprint string            `json:"fingerprint"`
		Tree        map[string]string `json:"tree"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &described)
	want, _ := harness.NewOverlay(installed).Describe()
	if !described.Installed || described.Fingerprint != want.Fingerprint || described.Tree[harness.FileSystemPrompt] == "" {
		t.Fatalf("installed: %s", rec.Body.String())
	}
	if rec = doJSON(t, server, "GET", "/api/harness/run/missing--k1-parallel/compare", nil); rec.Code != 404 {
		t.Fatalf("comparing an unknown run: %d", rec.Code)
	}
}
