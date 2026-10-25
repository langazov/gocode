package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/harness"
	"github.com/langazov/gocode-go/internal/session"
)

// fakeHarnessModel is an OpenAI-compatible endpoint that plays every part of
// a Harness² run. A request whose system prompt carries the improver charter
// is an improver session; anything else is a rollout. Every session takes
// two turns: a write tool call, then a final text once the tool result is in.
type fakeHarnessModel struct {
	mu       sync.Mutex
	rollouts int
	editors  int
	systems  []string
	// firstCall, when set, is the rollout's first tool call instead of the
	// write; toolResults records every tool result a rollout got back.
	firstCall *struct {
		name string
		args map[string]any
	}
	toolResults []string
}

func (f *fakeHarnessModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &request)
	system, user, hasTool := "", "", false
	for _, m := range request.Messages {
		text, _ := m.Content.(string)
		if parts, ok := m.Content.([]any); ok {
			for _, p := range parts {
				if part, ok := p.(map[string]any); ok {
					if s, ok := part["text"].(string); ok {
						text += s
					}
				}
			}
		}
		switch m.Role {
		case "system":
			system += text
		case "user":
			if user == "" {
				user = text
			}
		case "tool":
			hasTool = true
			if !improver(system) {
				fakeResultsMu.Lock()
				lastToolResult = text
				fakeResultsMu.Unlock()
			}
		}
	}
	isImprover := improver(system)
	if hasTool && !isImprover {
		fakeResultsMu.Lock()
		f.mu.Lock()
		f.toolResults = append(f.toolResults, lastToolResult)
		f.mu.Unlock()
		fakeResultsMu.Unlock()
	}

	var events []map[string]any
	switch {
	case !hasTool && isImprover && strings.Contains(user, "You are reading the complete record"):
		events = textEvents("<edits>\ncomponent: systemprompt.md\nverdict: helped\n</edits>\n<experience>\n" +
			"cue: a new file the task names\nlesson: systemprompt.md should say to create named files first\n" +
			"evidence: inv1 created it\nconfidence: low\n</experience>")
	case !hasTool && isImprover:
		f.mu.Lock()
		f.editors++
		f.mu.Unlock()
		events = toolEvents("write", map[string]any{"path": "context/harness/systemprompt.md", "content": "Create the files the task names first.\n"})
	case !hasTool:
		f.mu.Lock()
		f.rollouts++
		f.systems = append(f.systems, system)
		first := f.firstCall
		f.mu.Unlock()
		if first != nil {
			events = toolEvents(first.name, first.args)
		} else {
			events = toolEvents("write", map[string]any{"path": "notes.txt", "content": "notes\n"})
		}
	case isImprover:
		events = textEvents("<notes>systemprompt.md: the base rollouts created notes.txt late</notes>")
	default:
		events = textEvents("Created notes.txt.")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		encoded, _ := json.Marshal(e)
		w.Write([]byte("data: " + string(encoded) + "\n\n"))
	}
	w.Write([]byte("data: [DONE]\n\n"))
}

var (
	fakeResultsMu  sync.Mutex
	lastToolResult string
)

func improver(system string) bool { return strings.Contains(system, "one stage of Harness²") }

func textEvents(text string) []map[string]any {
	return []map[string]any{
		{"choices": []map[string]any{{"delta": map[string]any{"content": text}}}},
		{"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "stop"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 5}},
	}
}

func toolEvents(name string, args map[string]any) []map[string]any {
	encoded, _ := json.Marshal(args)
	return []map[string]any{
		{"choices": []map[string]any{{"delta": map[string]any{"tool_calls": []map[string]any{{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": name, "arguments": string(encoded)},
		}}}}}},
		{"choices": []map[string]any{{"delta": map[string]any{}, "finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 5}},
	}
}

func setupFakeHarnessModel(t *testing.T) *fakeHarnessModel {
	t.Helper()
	fake := &fakeHarnessModel{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	for _, key := range []string{"XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("GOCODE_TEST_HOME", t.TempDir())
	t.Setenv("GOCODE_DISABLE_MODELS_FETCH", "true")
	t.Setenv("GOCODE_AUTH_CONTENT", `{"faketest":{"type":"api","key":"k"}}`)
	t.Setenv("GOCODE_CONFIG_CONTENT", `{"model":"faketest/fake-model"}`)
	catalogPath := filepath.Join(t.TempDir(), "models.json")
	catalog := `{"faketest":{"id":"faketest","npm":"@ai-sdk/openai-compatible","api":"` + srv.URL + `","env":["FAKETEST_API_KEY"],
	  "models":{"fake-model":{"id":"fake-model","name":"Fake","release_date":"2026-01-01","attachment":false,"reasoning":false,"temperature":false,"tool_call":true,"limit":{"context":100000,"output":10000}}}}}`
	if err := os.WriteFile(catalogPath, []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCODE_MODELS_PATH", catalogPath)
	return fake
}

// A full k=1 run over real stacks: rollouts edit a copy of the workspace,
// editors edit context/harness/ in their evidence workspace, the composed
// harness reaches later rollouts' system prompts, and the lesson is stored.
func TestHarnessRunEndToEnd(t *testing.T) {
	fake := setupFakeHarnessModel(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("project rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	original, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(original) })
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st := bootStackT(t, ctx, "")
	engine, detection, err := newHarnessEngine(ctx, st, harnessOptions{Library: "auto", Width: 2})
	if err != nil {
		t.Fatal(err)
	}
	if detection.Backend != harness.BackendLocal {
		t.Fatalf("backend = %+v, want local without library-plugin", detection)
	}
	task := harness.Task{ID: "notes", Domain: "docs", Prompt: "Create notes.txt.", Workspace: project}
	relayed, unsubscribe := st.Bus.Subscribe(4096)
	defer unsubscribe()
	summary, err := engine.Run(ctx, task, harness.RunOptions{K: 1, Mode: harness.ModeParallel})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Status != "ok" {
		t.Fatalf("status %s, errors %v", summary.Status, summary.Errors)
	}
	for _, stage := range summary.Stages {
		if stage.FellBack {
			t.Errorf("stage %s fell back: %s", stage.Label, stage.Error)
		}
	}
	if fake.rollouts != harness.ExpectedRollouts(1) || fake.editors != harness.ExpectedSessions(1) {
		t.Fatalf("rollouts %d, editor sessions %d", fake.rollouts, fake.editors)
	}
	if _, err := os.Stat(filepath.Join(project, "notes.txt")); err == nil {
		t.Fatal("a rollout wrote into the project itself")
	}
	adopted, err := summary.AdoptedTree()
	if err != nil || !strings.Contains(adopted[harness.FileSystemPrompt], "Create the files") {
		t.Fatalf("adopted = %v, %v", adopted, err)
	}
	// The base rollouts ran without a harness block; the candidates and the
	// final with one. Every rollout read the project's AGENTS.md.
	withHarness := 0
	for _, system := range fake.systems {
		if !strings.Contains(system, "project rules") {
			t.Errorf("a rollout ran without the project's instructions: %q", clipLine(system, 200))
		}
		if strings.Contains(system, "# HARNESS") && strings.Contains(system, "Create the files the task names first") {
			withHarness++
		}
	}
	if withHarness != 3 {
		t.Fatalf("%d rollouts carried the adapted harness, want 3", withHarness)
	}
	deliverable, _ := os.ReadFile(filepath.Join(engine.Dir("notes", 1), harness.ModeParallel, "runs", "agg1", "deliverable.md"))
	if !strings.Contains(string(deliverable), "+notes") || !strings.Contains(string(deliverable), "Created notes.txt.") {
		t.Fatalf("deliverable:\n%s", deliverable)
	}
	if !summary.Stored {
		t.Fatalf("lesson not stored: %s", summary.StoreError)
	}

	// The rollouts ran on their own stacks; their events were relayed to the
	// launching stack's bus, so a client of it streams them live.
	aggSession := summary.Rollouts["agg1"].SessionID
	sawAgg := false
drain:
	for {
		select {
		case payload := <-relayed:
			if id, _ := payload.Data["sessionID"].(string); id == aggSession && payload.Type == "session.next.tool.called" {
				sawAgg = true
			}
		default:
			break drain
		}
	}
	if aggSession == "" || !sawAgg {
		t.Fatalf("agg1's tool call (session %q) was not relayed", aggSession)
	}
	// The run log names every session: four rollouts and three editors.
	events, err := harness.ReadLog(filepath.Join(engine.Dir("notes", 1), harness.ModeParallel, harness.LogFile), 0)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]int{}
	for _, ev := range events {
		if ev.Type == harness.EventSession {
			roles[ev.Role]++
		}
		if ev.Time == 0 {
			t.Fatalf("an event without a time: %+v", ev)
		}
	}
	if roles["rollout"] != 4 || roles[harness.RoleEditor] != 3 || roles[harness.RoleReflector] != 1 {
		t.Fatalf("session events by role = %v", roles)
	}

	result, err := installHarness(ctx, project, summary, harness.InstallRequest{To: "project"}, nil)
	if err != nil || !result.Written || !harness.IsTree(filepath.Join(project, ".gocode", "harness")) {
		t.Fatalf("install: %+v, %v", result, err)
	}
	// A second, different harness needs force.
	summary.Adopted = filepath.Join(t.TempDir(), "other")
	if _, err := harness.WriteTree(harness.Tree{harness.FileSystemPrompt: "other"}, summary.Adopted, false); err != nil {
		t.Fatal(err)
	}
	if _, err := installHarness(ctx, project, summary, harness.InstallRequest{To: "project"}, nil); err == nil {
		t.Fatal("a different installed harness was replaced without force")
	}
	if dry, err := installHarness(ctx, project, summary, harness.InstallRequest{To: "project", DryRun: true}, nil); err != nil || dry.Written || !dry.Conflict {
		t.Fatalf("dry run: %+v, %v", dry, err)
	}
	// Every rollout and editor session lives under the run directory, so
	// pruning it leaves none behind — and nothing else goes with them.
	var before, outside int
	st.Database.QueryRow(ctx, `SELECT count(*) FROM session`).Scan(&before)
	runs := filepath.Join(harnessRoot(), "runs")
	pruned, err := session.DeleteUnder(ctx, st.Database, runs)
	if err != nil || pruned != int64(harness.ExpectedRollouts(1)+harness.ExpectedSessions(1)+1) {
		t.Fatalf("pruned %d of %d sessions: %v", pruned, before, err)
	}
	st.Database.QueryRow(ctx, `SELECT count(*) FROM session`).Scan(&outside)
	if outside != before-int(pruned) {
		t.Fatalf("%d sessions left, want %d", outside, before-int(pruned))
	}

	// A new stack in the project now carries the installed harness.
	again := bootStackT(t, ctx, "")
	if again.Runner.Harness == nil {
		t.Fatal("installed harness was not overlaid")
	}
	t.Setenv("GOCODE_DISABLE_HARNESS", "1")
	if off := bootStackT(t, ctx, ""); off.Runner.Harness != nil {
		t.Fatal("GOCODE_DISABLE_HARNESS did not turn the overlay off")
	}
}

// A closed stack stops its language servers. Harness² boots and closes one
// stack per rollout and editor session; each used to leave the servers its
// edits had started running for the life of the process.
func TestStackCloseShutsDownLanguageServers(t *testing.T) {
	setupFakeHarnessModel(t)
	original, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(original) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	st, err := bootStack(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if st.LSP.Closed() {
		t.Fatal("a fresh stack's language servers are already shut down")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if !st.LSP.Closed() {
		t.Fatal("stack.Close left the language servers running")
	}
}

// rolloutHarness is a harnessStacks over st, for driving one rollout.
func rolloutHarness(st *stack, timeout time.Duration) *harnessStacks {
	return &harnessStacks{
		Database: st.Database, Catalog: st.Models, Model: "faketest/fake-model",
		Relay: st.Bus, Live: st.liveHarnessStacks(), RolloutTimeout: timeout,
	}
}

func rolloutRequest(t *testing.T, onSession func(string)) harness.RolloutRequest {
	return harness.RolloutRequest{
		Task:  harness.Task{ID: "t", Domain: "d", Prompt: "Do the task."},
		Label: "base1", HarnessDir: t.TempDir(), Workspace: t.TempDir(), OnSession: onSession,
	}
}

func bootInTemp(t *testing.T) *stack {
	t.Helper()
	original, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(original) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	return bootStackT(t, context.Background(), "")
}

// A rollout whose model asks the user a question used to park forever:
// nobody can answer it. The tool is denied, the model gets that back as the
// result, and the rollout finishes.
func TestRolloutNeverWaitsOnAQuestion(t *testing.T) {
	fake := setupFakeHarnessModel(t)
	fake.firstCall = &struct {
		name string
		args map[string]any
	}{"question", map[string]any{"questions": []any{map[string]any{"question": "Which file?", "header": "File", "options": []any{map[string]any{"label": "a"}}}}}}
	st := bootInTemp(t)
	done := make(chan harness.Rollout, 1)
	go func() {
		result, _ := rolloutHarness(st, time.Minute).Rollout(context.Background(), rolloutRequest(t, nil))
		done <- result
	}()
	select {
	case result := <-done:
		if !result.OK || result.Final == "" {
			t.Fatalf("rollout = %+v", result)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the rollout is waiting on a question nobody can answer")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.toolResults) == 0 || !strings.Contains(strings.ToLower(fake.toolResults[0]), "denied") &&
		!strings.Contains(strings.ToLower(fake.toolResults[0]), "blocked") {
		t.Fatalf("question tool result = %q, want a refusal", fake.toolResults)
	}
}

func sleepingRollout(fake *fakeHarnessModel) {
	fake.firstCall = &struct {
		name string
		args map[string]any
	}{"bash", map[string]any{"command": "sleep 30", "description": "wait"}}
}

// A rollout that outlives its limit is stopped and recorded as failed.
func TestRolloutStopsAtItsTimeout(t *testing.T) {
	sleepingRollout(setupFakeHarnessModel(t))
	st := bootInTemp(t)
	started := time.Now()
	result, err := rolloutHarness(st, time.Second).Rollout(context.Background(), rolloutRequest(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 15*time.Second {
		t.Fatalf("a 1s limit took %s to stop the rollout", took)
	}
	if result.OK || !strings.Contains(result.Error, "did not finish in time") {
		t.Fatalf("rollout = %+v", result)
	}
}

// Esc in the interface interrupts the launching server's session service,
// which does not own the rollout; the interrupt must reach the stack that
// does.
func TestInterruptReachesARollout(t *testing.T) {
	sleepingRollout(setupFakeHarnessModel(t))
	st := bootInTemp(t)
	sessions := make(chan string, 1)
	done := make(chan harness.Rollout, 1)
	go func() {
		result, _ := rolloutHarness(st, time.Minute).Rollout(context.Background(), rolloutRequest(t, func(id string) { sessions <- id }))
		done <- result
	}()
	id := <-sessions
	time.Sleep(500 * time.Millisecond) // into the sleeping tool call
	st.Service.Interrupt(id)
	select {
	case result := <-done:
		if result.OK {
			t.Fatalf("an interrupted rollout reported ok: %+v", result)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the interrupt did not reach the rollout's stack")
	}
}
