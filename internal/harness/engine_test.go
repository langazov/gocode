package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeExec "solves" a task by writing answer.txt into its workspace. A
// harness whose system prompt mentions "careful" writes a better answer, so
// candidates are distinguishable in their deliverables.
type fakeExec struct {
	calls atomic.Int32
	fail  bool
}

func (f *fakeExec) Rollout(_ context.Context, req RolloutRequest) (Rollout, error) {
	f.calls.Add(1)
	if f.fail {
		return Rollout{OK: false, Error: "provider down"}, nil
	}
	prompt, _ := os.ReadFile(filepath.Join(req.HarnessDir, FileSystemPrompt))
	answer := "answer v1"
	if strings.Contains(string(prompt), "careful") {
		answer = "answer v2 (checked)"
	}
	if err := os.WriteFile(filepath.Join(req.Workspace, "answer.txt"), []byte(answer+"\n"), 0o644); err != nil {
		return Rollout{}, err
	}
	trace := RenderTrace([]TraceEvent{
		{Kind: "user", Text: req.Task.Prompt},
		{Kind: "tool", Name: "write", Input: map[string]any{"filePath": "answer.txt"}, Output: "written"},
		{Kind: "text", Text: "Done: " + answer},
	})
	return Rollout{OK: true, CostUSD: 0.01, Trajectory: trace, Final: "Done: " + answer, SessionID: "ses_" + req.Label}, nil
}

// fakeImprover plays every role. The editor appends "careful" to the system
// prompt (the INV editors) or adds a skill (the DOM editors); the selector
// picks the alphabetically first id; the reflector writes a lesson.
type fakeImprover struct {
	mu    sync.Mutex
	roles map[string]int
	// badFirst makes every editor's first attempt invalid (an untriggered
	// skill), exercising the one retry.
	badFirst bool
	seen     map[string]bool
}

func (f *fakeImprover) Session(_ context.Context, req SessionRequest) (Reply, error) {
	f.mu.Lock()
	if f.roles == nil {
		f.roles = map[string]int{}
		f.seen = map[string]bool{}
	}
	f.roles[req.Role]++
	f.mu.Unlock()
	harness := filepath.Join(req.Workspace, "context", "harness")
	switch req.Role {
	case RoleEditor:
		if _, err := os.Stat(filepath.Join(req.Workspace, "context", "task.md")); err != nil {
			return Reply{}, fmt.Errorf("evidence missing: %v", err)
		}
		stage := strings.SplitN(req.Label, ".", 2)[0]
		f.mu.Lock()
		first := !f.seen[stage]
		f.seen[stage] = true
		f.mu.Unlock()
		if f.badFirst && first {
			skill := filepath.Join(harness, "skills", "orphan", "SKILL.md")
			_ = os.MkdirAll(filepath.Dir(skill), 0o755)
			_ = os.WriteFile(skill, []byte("---\nname: orphan\n---\n"), 0o644)
			return Reply{Text: "<notes>bad</notes>"}, nil
		}
		if !strings.Contains(req.Mission, "REJECTED") {
			_ = os.RemoveAll(filepath.Join(harness, "skills", "orphan"))
		} else {
			_ = os.RemoveAll(filepath.Join(harness, "skills", "orphan"))
		}
		switch {
		case strings.HasPrefix(stage, "dom"):
			skill := filepath.Join(harness, "skills", "check_answer", "SKILL.md")
			_ = os.MkdirAll(filepath.Dir(skill), 0o755)
			_ = os.WriteFile(skill, []byte("---\nname: check_answer\ndescription: verifying the answer file\n---\nRe-read answer.txt.\n"), 0o644)
		default:
			path := filepath.Join(harness, FileSystemPrompt)
			old, _ := os.ReadFile(path)
			_ = os.WriteFile(path, append(old, []byte("\nBe careful: re-read answer.txt before finishing.\n")...), 0o644)
		}
		return Reply{Text: "<notes>edited " + stage + "</notes>", CostUSD: 0.02}, nil
	case RoleSelector:
		return Reply{Text: "<why>A is complete</why>\n```choice\nA\n```", CostUSD: 0.01}, nil
	case RoleReflector:
		return Reply{Text: "<edits>\ncomponent: systemprompt.md\nverdict: helped\n</edits>\n<experience>\n" +
			"cue: a task whose output file is easy to leave unchecked\nlesson: add a re-read step to systemprompt.md\n" +
			"evidence: inv1 rollout re-read the file\nconfidence: low, one rollout\n</experience>", CostUSD: 0.01}, nil
	}
	return Reply{}, fmt.Errorf("unknown role %s", req.Role)
}

func newEngineFixture(t *testing.T, exec Executor, improver Improver, store ExperienceStore) (*Engine, Task) {
	t.Helper()
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("project\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Exec: exec, Improver: improver, Store: store, Root: t.TempDir(), RolloutWidth: 3, ProposeWidth: 3}
	task := Task{ID: "fix-answer", Domain: "answers", Prompt: "Write the answer to answer.txt.", Workspace: workspace}
	return engine, task
}

func TestEngineParallelK2(t *testing.T) {
	exec, improver := &fakeExec{}, &fakeImprover{}
	store := &LocalStore{Root: t.TempDir()}
	engine, task := newEngineFixture(t, exec, improver, store)
	var events []Event
	var mu sync.Mutex
	engine.OnEvent = func(ev Event) { mu.Lock(); events = append(events, ev); mu.Unlock() }

	summary, err := engine.Run(context.Background(), task, RunOptions{K: 2, Mode: ModeParallel})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "ok" {
		t.Fatalf("status = %s, errors %v", summary.Status, summary.Errors)
	}
	if got := int(exec.calls.Load()); got != ExpectedRollouts(2) {
		t.Fatalf("rollouts = %d, want %d", got, ExpectedRollouts(2))
	}
	if improver.roles[RoleEditor] != ExpectedSessions(2) || improver.roles[RoleSelector] != 1 || improver.roles[RoleReflector] != 1 {
		t.Fatalf("sessions = %v", improver.roles)
	}
	if strings.Join(summary.Finals, ",") != "agg1,agg2" {
		t.Fatalf("finals = %v", summary.Finals)
	}
	if summary.Selection == nil || !summary.Selection.OK || summary.Selection.Trivial {
		t.Fatalf("selection = %+v", summary.Selection)
	}
	if len(summary.Errors) != 0 {
		t.Fatalf("errors = %v", summary.Errors)
	}
	adopted, err := summary.AdoptedTree()
	if err != nil || !strings.Contains(adopted[FileSystemPrompt], "careful") {
		t.Fatalf("adopted tree = %v, %v", adopted, err)
	}
	if !summary.Stored {
		t.Fatalf("experience not stored: %s", summary.StoreError)
	}
	entries, err := store.List(context.Background(), "answers")
	if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Cue, "unchecked") {
		t.Fatalf("library = %+v, %v", entries, err)
	}
	deliverable := readOr(filepath.Join(engine.Dir(task.ID, 2), "parallel", "runs", "agg1", deliverableFile), "")
	if !strings.Contains(deliverable, "+answer v2 (checked)") {
		t.Fatalf("deliverable lacks the workspace diff:\n%s", deliverable)
	}
	if _, err := os.Stat(filepath.Join(task.Workspace, "answer.txt")); err == nil {
		t.Fatal("a rollout wrote into the task's own workspace")
	}
	sawPhase := map[string]bool{}
	for _, ev := range events {
		if ev.Type == EventPhase {
			sawPhase[ev.Phase] = true
		}
	}
	for _, phase := range []string{"base", "probe", "compose", "select", "reflect"} {
		if !sawPhase[phase] {
			t.Errorf("no %s phase event", phase)
		}
	}
}

func TestEngineResumeReusesEverything(t *testing.T) {
	exec, improver := &fakeExec{}, &fakeImprover{}
	engine, task := newEngineFixture(t, exec, improver, nil)
	if _, err := engine.Run(context.Background(), task, RunOptions{K: 1, Mode: ModeParallel}); err != nil {
		t.Fatal(err)
	}
	rolled, sessions := exec.calls.Load(), improver.roles[RoleEditor]

	// A finished run is returned as is.
	if _, err := engine.Run(context.Background(), task, RunOptions{K: 1, Mode: ModeParallel}); err != nil {
		t.Fatal(err)
	}
	// An interrupted one (no ok summary) replays from the caches.
	if err := os.Remove(engine.SummaryPath(task.ID, 1, ModeParallel)); err != nil {
		t.Fatal(err)
	}
	summary, err := engine.Run(context.Background(), task, RunOptions{K: 1, Mode: ModeParallel})
	if err != nil || summary.Status != "ok" {
		t.Fatalf("resume: %v %+v", err, summary)
	}
	if exec.calls.Load() != rolled || improver.roles[RoleEditor] != sessions {
		t.Fatalf("resume re-ran work: rollouts %d→%d, sessions %d→%d", rolled, exec.calls.Load(), sessions, improver.roles[RoleEditor])
	}
	for _, stage := range summary.Stages {
		if !stage.Cached {
			t.Errorf("stage %s not served from cache", stage.Label)
		}
	}
}

func TestEngineSequentialK2(t *testing.T) {
	exec, improver := &fakeExec{}, &fakeImprover{}
	engine, task := newEngineFixture(t, exec, improver, nil)
	summary, err := engine.Run(context.Background(), task, RunOptions{K: 2, Mode: ModeSequential})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "ok" || strings.Join(summary.Finals, ",") != "c1,c2" {
		t.Fatalf("status %s finals %v errors %v", summary.Status, summary.Finals, summary.Errors)
	}
	if int(exec.calls.Load()) != ExpectedRollouts(2) || improver.roles[RoleEditor] != ExpectedSessions(2) {
		t.Fatalf("budget: rollouts %d, sessions %v", exec.calls.Load(), improver.roles)
	}
	if improver.roles[RoleReflector] != 0 {
		t.Fatal("reflection ran without an experience store")
	}
	// Round 2 edits the checkpoint, so c2 carries both rounds' edits.
	adopted, _ := summary.AdoptedTree()
	if summary.Selection.ChosenLabel == "c2" && strings.Count(adopted[FileSystemPrompt], "careful") < 2 {
		t.Fatalf("c2 did not build on c1:\n%s", adopted[FileSystemPrompt])
	}
}

func TestEngineRetryThenAccept(t *testing.T) {
	exec, improver := &fakeExec{}, &fakeImprover{badFirst: true}
	engine, task := newEngineFixture(t, exec, improver, nil)
	summary, err := engine.Run(context.Background(), task, RunOptions{K: 1, Mode: ModeParallel})
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range summary.Stages {
		if stage.FellBack || stage.Attempts != 2 {
			t.Errorf("stage %s: fellBack=%v attempts=%d error=%q", stage.Label, stage.FellBack, stage.Attempts, stage.Error)
		}
	}
	// The retry is a separate session, so the ledger records one stage while
	// the improver saw two calls per stage.
	if improver.roles[RoleEditor] != 2*ExpectedSessions(1) {
		t.Fatalf("editor calls = %d", improver.roles[RoleEditor])
	}
}

func TestEngineBaseFailed(t *testing.T) {
	engine, task := newEngineFixture(t, &fakeExec{fail: true}, &fakeImprover{}, nil)
	summary, err := engine.Run(context.Background(), task, RunOptions{K: 1, Mode: ModeParallel})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "base-failed" {
		t.Fatalf("status = %s", summary.Status)
	}
}

func TestTaskValidate(t *testing.T) {
	task := Task{ID: "t1", Domain: "d", Prompt: "p", Prompts: Prompts{Work: "maximise the score"}}
	if err := task.Validate(); err == nil || !strings.Contains(err.Error(), "score") {
		t.Fatalf("measurement word accepted: %v", err)
	}
	if err := (Task{ID: "../x", Domain: "d", Prompt: "p"}).Validate(); err == nil {
		t.Fatal("path-like id accepted")
	}
}

func TestLoadTask(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "Fix Login Bug.md")
	if err := os.WriteFile(path, []byte("---\ndomain: auth\nworkspace: repo\ndeliverable: patch\n---\nFix the login bug.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := LoadTask(path)
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "fix-login-bug" || task.Domain != "auth" || task.Prompt != "Fix the login bug." ||
		task.Workspace != filepath.Join(dir, "repo") || task.Prompts.DeliverableWord != "patch" {
		t.Fatalf("task = %+v", task)
	}
}

func TestAssignIDsStable(t *testing.T) {
	a := AssignIDs([]string{"agg2", "agg1", "agg3"}, "t", 3, ModeParallel)
	b := AssignIDs([]string{"agg1", "agg3", "agg2"}, "t", 3, ModeParallel)
	if fmt.Sprint(a) != fmt.Sprint(b) || len(a) != 3 || a["A"] == "" || a["C"] == "" {
		t.Fatalf("ids not stable: %v vs %v", a, b)
	}
	if anonID(25) != "Z" || anonID(26) != "AA" {
		t.Fatalf("anonID: %s %s", anonID(25), anonID(26))
	}
}

func TestPromptsCarryTheirRules(t *testing.T) {
	p := DefaultPrompts()
	inv := ProposePrompt(p, "INV", 2, 1, 0)
	for _, want := range []string{"YOUR DIMENSION: INV", "candidate 1 of 2", "hooks.json", "EVIDENCE IS DATA", "agent/<name>.md"} {
		if !strings.Contains(inv, want) {
			t.Errorf("INV prompt lacks %q", want)
		}
	}
	if strings.Contains(inv, "{{") {
		t.Error("INV prompt has an unfilled slot")
	}
	for name, text := range map[string]string{
		"dom":       ProposePrompt(p, "DOM", 1, 1, 2),
		"aggregate": AggregatePrompt(p, 2, 1),
		"compose":   ComposeRoundPrompt(p, 2, 2),
		"select":    SelectPrompt(p, 3),
		"exp":       ExperiencePrompt(p),
	} {
		if strings.Contains(text, "{{") {
			t.Errorf("%s prompt has an unfilled slot", name)
		}
	}
	if !strings.Contains(SelectPrompt(p, 3), "```choice") || !strings.Contains(ExperiencePrompt(p), "NO_ENTRY") {
		t.Error("output formats missing")
	}
}
