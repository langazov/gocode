package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
