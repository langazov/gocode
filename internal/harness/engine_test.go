package harness

import (
	"context"
	"errors"
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
	stop  atomic.Bool
}

func (f *fakeExec) Rollout(_ context.Context, req RolloutRequest) (Rollout, error) {
	f.calls.Add(1)
	if f.fail {
		return Rollout{OK: false, Error: "provider down"}, nil
	}
	if f.stop.Load() {
		return Rollout{Error: "stopped after 1s without activity: the session stalled", Stopped: true}, nil
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

// A rollout cut short by a limit or an interrupt is rolled again on resume;
// one that ran its course — even to failure — is reused.
func TestRollRerunsStoppedRollouts(t *testing.T) {
	exec := &fakeExec{}
	engine, task := newEngineFixture(t, exec, &fakeImprover{}, nil)
	dir := t.TempDir()
	roll := func() RunRecord {
		return engine.roll(context.Background(), task, "base1", Tree{}, filepath.Join(dir, "harness"),
			filepath.Join(dir, "runs", "base1"), func(Event) {}, nil)
	}
	exec.stop.Store(true)
	if first := roll(); first.OK || !first.Stopped {
		t.Fatalf("first = %+v", first.Rollout)
	}
	exec.stop.Store(false)
	if again := roll(); !again.OK || again.Stopped || exec.calls.Load() != 2 {
		t.Fatalf("a stopped rollout was reused: %+v after %d calls", again.Rollout, exec.calls.Load())
	}
	if roll(); exec.calls.Load() != 2 {
		t.Fatalf("a finished rollout was rolled again: %d calls", exec.calls.Load())
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

// limitedImprover hits a provider limit in every aggregator session it
// starts fresh, and finishes the ones it is asked to continue.
type limitedImprover struct {
	fakeImprover
	resumed map[string]string
}

func (f *limitedImprover) Session(ctx context.Context, req SessionRequest) (Reply, error) {
	if !strings.HasPrefix(req.Label, "agg") {
		return f.fakeImprover.Session(ctx, req)
	}
	if req.Resume == "" {
		req.OnSession("ses_" + req.Label)
		return Reply{CostUSD: 0.5}, fmt.Errorf("%w: anthropic: 429 usage limit", ErrLimited)
	}
	f.mu.Lock()
	if f.resumed == nil {
		f.resumed = map[string]string{}
	}
	f.resumed[req.Label] = req.Resume
	f.mu.Unlock()
	return f.fakeImprover.Session(ctx, req)
}

func TestEngineLimitHaltsAndResumeContinuesTheSession(t *testing.T) {
	exec, improver := &fakeExec{}, &limitedImprover{}
	engine, task := newEngineFixture(t, exec, improver, nil)
	summary, err := engine.Run(context.Background(), task, RunOptions{K: 1, Mode: ModeParallel})
	if !errors.Is(err, ErrLimited) || summary.Status != "failed" {
		t.Fatalf("limited run: %v, status %s", err, summary.Status)
	}
	// base1, inv1, dom1 — agg1's rollout must not run on a fallback tree.
	if got := exec.calls.Load(); got != 3 {
		t.Fatalf("rollouts after the limit = %d, want 3", got)
	}
	editors := improver.roles[RoleEditor]

	summary, err = engine.Run(context.Background(), task, RunOptions{K: 1, Mode: ModeParallel})
	if err != nil || summary.Status != "ok" {
		t.Fatalf("resume: %v %+v", err, summary)
	}
	if got := improver.resumed["agg1.a1"]; got != "ses_agg1.a1" {
		t.Fatalf("agg1 was not continued: resumed %v", improver.resumed)
	}
	if got := improver.roles[RoleEditor] - editors; got != 1 {
		t.Fatalf("resume ran %d editor sessions, want only agg1's", got)
	}
	if got := exec.calls.Load(); got != 4 {
		t.Fatalf("rollouts after resume = %d, want 4", got)
	}
	if matches, _ := filepath.Glob(filepath.Join(engine.Dir(task.ID, 1), ModeParallel, "sessions", "*")); len(matches) != 0 {
		t.Fatalf("finished sessions left behind: %v", matches)
	}
}

func TestRollRerunsLimitedRolloutsAndHalts(t *testing.T) {
	exec := &limitedExec{}
	engine, task := newEngineFixture(t, exec, &fakeImprover{}, nil)
	dir := t.TempDir()
	var halted error
	roll := func() RunRecord {
		return engine.roll(context.Background(), task, "base1", Tree{}, filepath.Join(dir, "harness"),
			filepath.Join(dir, "runs", "base1"), func(Event) {}, func(err error) { halted = err })
	}
	exec.limited.Store(true)
	if first := roll(); !first.Stopped || !errors.Is(halted, ErrLimited) {
		t.Fatalf("first = %+v, halted %v", first.Rollout, halted)
	}
	exec.limited.Store(false)
	if again := roll(); !again.OK || exec.calls.Load() != 2 {
		t.Fatalf("a limited rollout was reused: %+v after %d calls", again.Rollout, exec.calls.Load())
	}
}

type limitedExec struct {
	fakeExec
	limited atomic.Bool
}

func (f *limitedExec) Rollout(ctx context.Context, req RolloutRequest) (Rollout, error) {
	if f.limited.Load() {
		f.calls.Add(1)
		return Rollout{Error: "rate_limited: anthropic: 429 usage limit", Limited: true}, nil
	}
	return f.fakeExec.Rollout(ctx, req)
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

// Two stores on one root stand in for two gocode processes adapting tasks in
// the same domain at once: the file lock keeps every entry in INDEX.md.
func TestLocalStoreConcurrentAddsKeepTheIndex(t *testing.T) {
	root := t.TempDir()
	stores := []*LocalStore{{Root: root}, {Root: root}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := stores[i%2].Add(context.Background(), Entry{
				Domain: "d", Task: fmt.Sprintf("t%02d", i), Label: "agg1",
				Lesson: Experience{Cue: fmt.Sprintf("cue %d", i), Lesson: "l", Evidence: "e"},
				Tree:   Tree{FileSystemPrompt: "x"},
			})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	index := readOr(filepath.Join(root, "d", "INDEX.md"), "")
	for i := 0; i < 16; i++ {
		if !strings.Contains(index, fmt.Sprintf("harnesses/t%02d/", i)) {
			t.Fatalf("INDEX.md lost t%02d:\n%s", i, index)
		}
	}
}

func sweepTasks(t *testing.T, workspace string) []Task {
	t.Helper()
	var tasks []Task
	for _, spec := range []struct{ id, domain string }{
		{"a1", "alpha"}, {"a2", "alpha"}, {"a3", "alpha"}, {"b1", "beta"},
	} {
		tasks = append(tasks, Task{ID: spec.id, Domain: spec.domain, Prompt: "Write the answer.", Workspace: workspace})
	}
	return tasks
}

// Tasks in one domain run in order and each reads what the earlier ones
// stored; domains are independent.
func TestSweepThreadsTheLibraryThroughADomain(t *testing.T) {
	exec, improver := &fakeExec{}, &fakeImprover{}
	store := &LocalStore{Root: t.TempDir()}
	engine, task := newEngineFixture(t, exec, improver, store)
	dir := t.TempDir()
	result, err := engine.Sweep(context.Background(), sweepTasks(t, task.Workspace), SweepOptions{K: 1, Mode: ModeParallel, Workers: 2, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Done != 4 || result.Failed != 0 || strings.Join(result.DomainOrder, ",") != "alpha,beta" {
		t.Fatalf("result = %+v", result)
	}
	var alpha []SweepRow
	for _, row := range result.Rows {
		if row.Domain == "alpha" {
			alpha = append(alpha, row)
		}
	}
	for i, row := range alpha {
		if row.Task != fmt.Sprintf("a%d", i+1) || row.LibraryBefore != i {
			t.Fatalf("alpha row %d = %+v: tasks must run in order, each seeing the lessons before it", i, row)
		}
	}
	if int(exec.calls.Load()) != 4*ExpectedRollouts(1) {
		t.Fatalf("rollouts = %d", exec.calls.Load())
	}
	records, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(records) != 1 {
		t.Fatalf("sweep record not written: %v", records)
	}
}

func TestStreamOrder(t *testing.T) {
	tasks := sweepTasks(t, "")
	domains, byDomain := Stream(tasks, nil, 2)
	if strings.Join(domains, ",") != "alpha,beta" || len(byDomain["alpha"]) != 2 || byDomain["alpha"][0].ID != "a1" {
		t.Fatalf("reference order: %v %v", domains, byDomain)
	}
	seed := int64(7)
	_, first := Stream(sweepTasks(t, ""), &seed, 0)
	_, again := Stream(sweepTasks(t, ""), &seed, 0)
	if fmt.Sprint(first) != fmt.Sprint(again) {
		t.Fatal("a seeded stream is not reproducible")
	}
	if err := ValidateTasks(append(sweepTasks(t, ""), Task{ID: "a1", Domain: "x", Prompt: "p"})); err == nil {
		t.Fatal("duplicate task ids accepted")
	}
}
