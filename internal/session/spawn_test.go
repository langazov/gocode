package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/tool"
)

// gatedProvider serves a scripted turn per session, blocking each session's
// first turn until its gate is released. That is how these tests prove two
// subagent sessions are genuinely in flight at the same time.
type gatedProvider struct {
	mu      sync.Mutex
	started map[string]chan struct{}
	release map[string]chan struct{}
	replies map[string]string
	seen    map[string]int
}

func newGatedProvider() *gatedProvider {
	return &gatedProvider{
		started: map[string]chan struct{}{},
		release: map[string]chan struct{}{},
		replies: map[string]string{},
		seen:    map[string]int{},
	}
}

// script registers the text a session's agent replies with, plus its gate.
func (p *gatedProvider) script(agentID, reply string) (started, release chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	started = make(chan struct{})
	release = make(chan struct{})
	p.started[agentID] = started
	p.release[agentID] = release
	p.replies[agentID] = reply
	return started, release
}

func (p *gatedProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	// The runner puts the agent's system prompt first; tests use it to tell
	// which session is calling.
	agentID := ""
	if len(request.System) > 0 {
		agentID = request.System[0]
	}
	p.mu.Lock()
	started, release := p.started[agentID], p.release[agentID]
	reply := p.replies[agentID]
	p.seen[agentID]++
	first := p.seen[agentID] == 1
	p.mu.Unlock()

	if first && started != nil {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: reply})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
	return nil
}

func newSpawnFixture(t *testing.T, provider llm.StreamClient) (*Spawner, *Service, *Runner) {
	t.Helper()
	// setup already registers the session projectors.
	bus, database := setup(t)
	RegisterRunnerProjectors(bus)

	agents := agent.NewRegistry()
	agents.Update(agent.Info{ID: "build", Mode: "primary", System: "build",
		Permissions: permission.Defaults()})
	agents.Update(agent.Info{ID: "general", Mode: "subagent", System: "general",
		Permissions: permission.Defaults()})
	agents.Update(agent.Info{ID: "explore", Mode: "subagent", System: "explore",
		Permissions: permission.Defaults()})

	runner := &Runner{
		DB:       database,
		Bus:      bus,
		Messages: NewMessageStore(database),
		Provider: provider,
		Tools:    tool.NewRegistry(),
		Agents:   agents,
		Agent:    "build",
		Model:    ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
	}
	execution := NewExecution(&DBSessionLookup{DB: database}, runner)
	service := NewService(database, bus)
	service.Execution = execution
	service.DefaultModel = runner.Model
	return NewSpawner(service, execution, agents, 1), service, runner
}

func newRootSession(t *testing.T, service *Service) Info {
	t.Helper()
	root, err := service.Create(context.Background(), CreateInput{Directory: t.TempDir(), Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestSubagentsRunConcurrently is the phase 2 acceptance check: two spawns
// from one parent must both be in flight before either completes.
func TestSubagentsRunConcurrently(t *testing.T) {
	provider := newGatedProvider()
	generalStarted, generalRelease := provider.script("general", "general finished")
	exploreStarted, exploreRelease := provider.script("explore", "explore finished")

	spawner, service, _ := newSpawnFixture(t, provider)
	root := newRootSession(t, service)
	ctx := context.Background()

	_, generalDone, err := spawner.Spawn(ctx, tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "research", Prompt: "go research",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, exploreDone, err := spawner.Spawn(ctx, tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "explore", Description: "search", Prompt: "go search",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Both children must reach the provider before either is allowed to finish.
	for name, started := range map[string]chan struct{}{"general": generalStarted, "explore": exploreStarted} {
		select {
		case <-started:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s subagent never started: spawns are not concurrent", name)
		}
	}
	close(exploreRelease)
	close(generalRelease)

	results := map[string]string{}
	for range 2 {
		select {
		case r := <-generalDone:
			if r.Err != nil {
				t.Fatalf("general failed: %v", r.Err)
			}
			results["general"] = r.Text
			generalDone = nil
		case r := <-exploreDone:
			if r.Err != nil {
				t.Fatalf("explore failed: %v", r.Err)
			}
			results["explore"] = r.Text
			exploreDone = nil
		case <-time.After(10 * time.Second):
			t.Fatal("subagents did not report back")
		}
	}
	if results["general"] != "general finished" || results["explore"] != "explore finished" {
		t.Fatalf("unexpected subagent results: %+v", results)
	}

	children, err := service.Children(ctx, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 child sessions under the parent, got %d", len(children))
	}
}

// TestSubagentDepthLimit checks that a subagent cannot spawn its own subagent.
func TestSubagentDepthLimit(t *testing.T) {
	provider := newGatedProvider()
	provider.script("general", "done")
	spawner, service, _ := newSpawnFixture(t, provider)
	root := newRootSession(t, service)
	ctx := context.Background()

	child, err := service.Create(ctx, CreateInput{
		Directory: root.Directory, Title: "child", ParentID: root.ID, Agent: "general",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = spawner.Spawn(ctx, tool.SpawnRequest{
		ParentSessionID: child.ID, AgentID: "general", Description: "nested", Prompt: "go deeper",
	})
	if err == nil {
		t.Fatal("a subagent was allowed to spawn a nested subagent at depth 1")
	}
	if !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestSpawnRejectsPrimaryAgent guards the mode check.
func TestSpawnRejectsPrimaryAgent(t *testing.T) {
	spawner, service, _ := newSpawnFixture(t, newGatedProvider())
	root := newRootSession(t, service)
	_, _, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "build", Description: "nope", Prompt: "x",
	})
	if err == nil || !strings.Contains(err.Error(), "primary agent") {
		t.Fatalf("expected a primary-agent rejection, got %v", err)
	}
}

// TestSubagentPermissionsDenyTask pins the derived ruleset: a subagent may not
// use task or todowrite unless its own ruleset names them.
func TestSubagentPermissionsDenyTask(t *testing.T) {
	sub := agent.Info{ID: "general", Mode: "subagent", Permissions: permission.Defaults()}
	derived := DeriveSubagentPermissions(nil, sub)

	for _, action := range []string{"task", "todowrite"} {
		if rule := permission.Evaluate(action, "*", derived); rule.Effect != permission.Deny {
			t.Fatalf("%s effect = %q, want deny", action, rule.Effect)
		}
	}
	// A tool the subagent is allowed to use is unaffected.
	if rule := permission.Evaluate("read", "main.go", derived); rule.Effect != permission.Allow {
		t.Fatalf("read effect = %q, want allow", rule.Effect)
	}
	// An agent that names the tool explicitly keeps it.
	opted := agent.Info{ID: "lead", Mode: "subagent", Permissions: permission.Merge(
		permission.Defaults(),
		permission.Ruleset{{Action: "task", Resource: "*", Effect: permission.Allow}},
	)}
	if rule := permission.Evaluate("task", "*", DeriveSubagentPermissions(nil, opted)); rule.Effect != permission.Allow {
		t.Fatalf("explicit task grant was overridden: %q", rule.Effect)
	}
}

// TestSubagentSessionCarriesDerivedRuleset checks the wiring end to end: the
// child session stores its derived ruleset and the rules provider prefers it.
func TestSubagentSessionCarriesDerivedRuleset(t *testing.T) {
	provider := newGatedProvider()
	_, release := provider.script("general", "done")
	close(release)
	spawner, service, runner := newSpawnFixture(t, provider)
	root := newRootSession(t, service)
	ctx := context.Background()

	childID, done, err := spawner.Spawn(ctx, tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "work", Prompt: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subagent did not finish")
	}

	rules := &AgentRulesProvider{Agents: runner.Agents, Sessions: service}
	configured, err := rules.Configured(childID, "general")
	if err != nil {
		t.Fatal(err)
	}
	if rule := permission.Evaluate("task", "*", configured); rule.Effect != permission.Deny {
		t.Fatalf("child session ruleset allows task: %q", rule.Effect)
	}
	// The parent is untouched.
	parentRules, err := rules.Configured(root.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if rule := permission.Evaluate("task", "*", parentRules); rule.Effect == permission.Deny {
		t.Fatal("parent session lost the task tool")
	}
}

// The gatedProvider keys its scripts by the agent system prompt, so a
// model-observing provider needs its own capture. modelProbeProvider records
// the provider/model of every request so spawn tests can assert which model
// a child actually ran on.
type modelProbeProvider struct {
	mu      sync.Mutex
	seen    []ModelRef
	release chan struct{}
}

func newModelProbeProvider() *modelProbeProvider {
	return &modelProbeProvider{release: make(chan struct{})}
}

func (p *modelProbeProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.mu.Lock()
	p.seen = append(p.seen, ModelRef{ProviderID: request.ProviderID, ID: request.ModelID})
	p.mu.Unlock()
	<-p.release
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "ok"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
	return nil
}

func (p *modelProbeProvider) finish() { close(p.release) }

func (p *modelProbeProvider) models() []ModelRef {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ModelRef(nil), p.seen...)
}

// TestSpawnChildRunsOnInheritedModel is the end-to-end check: the llm request
// the child's first turn sends must carry the parent's switched model, not
// just the session row. A pinned column no runner read would pass the row
// assertions above while every request still went to the boot default.
func TestSpawnChildRunsOnInheritedModel(t *testing.T) {
	probe := newModelProbeProvider()
	spawner, service, _ := newSpawnFixture(t, probe)
	root := newRootSession(t, service)

	switched := ModelRef{ProviderID: "anthropic", ID: "claude-opus-4-8"}
	if err := service.SetModel(context.Background(), root.ID, switched); err != nil {
		t.Fatal(err)
	}

	_, done, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "work", Prompt: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	probe.finish()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subagent did not finish")
	}

	models := probe.models()
	if len(models) == 0 {
		t.Fatal("the child never reached the provider")
	}
	for _, ref := range models {
		if ref != switched {
			t.Fatalf("child ran on %+v, want the parent's switched model %+v", ref, switched)
		}
	}
}

// TestSpawnChildInheritsParentModel is the regression test for the model
// switch bug: a parent that pins a model mid-session must hand it to its
// children. Before resolveChildModel the child's model column was never
// written and every subagent resolved to the runner's boot default.
func TestSpawnChildInheritsParentModel(t *testing.T) {
	provider := newGatedProvider()
	_, release := provider.script("general", "done")
	close(release)
	spawner, service, runner := newSpawnFixture(t, provider)
	root := newRootSession(t, service)

	switched := ModelRef{ProviderID: "anthropic", ID: "claude-opus-4-8", Variant: "high"}
	if err := service.SetModel(context.Background(), root.ID, switched); err != nil {
		t.Fatal(err)
	}

	childID, done, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "work", Prompt: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subagent did not finish")
	}

	// The child's row carries the parent's current model, variant included.
	child, err := service.Get(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Model == nil {
		t.Fatal("child session has no model pinned; it would fall back to the boot default")
	}
	if *child.Model != switched {
		t.Fatalf("child model = %+v, want the parent's current model %+v", *child.Model, switched)
	}
	// The parent's own resolution is unchanged.
	if runner.Model.ProviderID != "anthropic" || runner.Model.ID != "claude-sonnet-4-5" {
		t.Fatalf("runner boot default unexpectedly changed: %+v", runner.Model)
	}
}

// TestSpawnModelArgumentOverridesInheritance pins the per-call override: the
// model parameter beats both the subagent's own configured model and the
// parent's current model.
func TestSpawnModelArgumentOverridesInheritance(t *testing.T) {
	provider := newGatedProvider()
	_, release := provider.script("general", "done")
	close(release)
	spawner, service, _ := newSpawnFixture(t, provider)
	// The subagent configures its own model; the argument must still win.
	spawner.Agents.Update(agent.Info{
		ID: "general", Mode: "subagent", System: "general",
		Model:       &agent.ModelRef{ProviderID: "anthropic", ID: "claude-haiku-4-5"},
		Permissions: permission.Defaults(),
	})
	root := newRootSession(t, service)
	if err := service.SetModel(context.Background(), root.ID, ModelRef{ProviderID: "anthropic", ID: "claude-opus-4-8"}); err != nil {
		t.Fatal(err)
	}

	childID, done, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "work", Prompt: "go",
		Model: "openai/gpt-5.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subagent did not finish")
	}

	child, err := service.Get(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Model == nil || child.Model.ProviderID != "openai" || child.Model.ID != "gpt-5.2" {
		t.Fatalf("child model = %+v, want the explicit openai/gpt-5.2", child.Model)
	}
	if child.Model != nil && child.Model.Variant != "" {
		t.Fatalf("an explicit model must not inherit a variant, got %q", child.Model.Variant)
	}
}

// TestSpawnKeepsSubagentConfiguredModel checks precedence step 2: a subagent
// with its own model config keeps it even when the parent switched — nothing
// is pinned on the child that could shadow the agent's model.
func TestSpawnKeepsSubagentConfiguredModel(t *testing.T) {
	provider := newGatedProvider()
	_, release := provider.script("reviewer", "done")
	close(release)
	spawner, service, _ := newSpawnFixture(t, provider)
	configured := &agent.ModelRef{ProviderID: "anthropic", ID: "claude-haiku-4-5"}
	spawner.Agents.Update(agent.Info{
		ID: "reviewer", Mode: "subagent", System: "reviewer", Model: configured,
		Permissions: permission.Defaults(),
	})
	root := newRootSession(t, service)
	if err := service.SetModel(context.Background(), root.ID, ModelRef{ProviderID: "anthropic", ID: "claude-opus-4-8"}); err != nil {
		t.Fatal(err)
	}

	childID, done, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "reviewer", Description: "review", Prompt: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subagent did not finish")
	}

	child, err := service.Get(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Model != nil {
		t.Fatalf("child pinned %+v; a subagent with its own model config must resolve through its agent", *child.Model)
	}
}

// TestSpawnWithoutSwitchInheritsBootDefault covers the no-switch case: a
// parent with no pinned model hands its children nothing, and the child
// resolves through the runner default — the same model the parent runs.
func TestSpawnWithoutSwitchInheritsBootDefault(t *testing.T) {
	provider := newGatedProvider()
	_, release := provider.script("general", "done")
	close(release)
	spawner, service, _ := newSpawnFixture(t, provider)
	root := newRootSession(t, service)

	childID, done, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "work", Prompt: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subagent did not finish")
	}

	child, err := service.Get(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Model != nil {
		t.Fatalf("child pinned %+v; with no switch anywhere the child should resolve via the runner default", *child.Model)
	}
}

// TestSpawnRejectsMalformedModel checks the argument validation surface.
func TestSpawnRejectsMalformedModel(t *testing.T) {
	spawner, service, _ := newSpawnFixture(t, newGatedProvider())
	root := newRootSession(t, service)
	for _, raw := range []string{"no-slash", "/leading", "trailing/", "/"} {
		_, _, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
			ParentSessionID: root.ID, AgentID: "general", Description: "work", Prompt: "go",
			Model: raw,
		})
		if err == nil || !strings.Contains(err.Error(), "Invalid model") {
			t.Fatalf("model %q: expected an invalid-model error, got %v", raw, err)
		}
	}
}

// TestSpawnResumeModelArgument checks the resume path: an explicit model
// follows a resumed child; a resume without one keeps the child's own model
// even after the parent switched again.
func TestSpawnResumeModelArgument(t *testing.T) {
	provider := newGatedProvider()
	_, release := provider.script("general", "done")
	close(release)
	spawner, service, _ := newSpawnFixture(t, provider)
	root := newRootSession(t, service)

	first, done, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "work", Prompt: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("first run did not finish")
	}

	// The parent switches after the first run.
	if err := service.SetModel(context.Background(), root.ID, ModelRef{ProviderID: "anthropic", ID: "claude-opus-4-8"}); err != nil {
		t.Fatal(err)
	}

	// A plain resume must not re-resolve inheritance: the child keeps its
	// own state, not the parent's later switch.
	_, done, err = spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "more", Prompt: "again",
		ResumeSessionID: first,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("plain resume did not finish")
	}
	resumed, err := service.Get(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Model != nil {
		t.Fatalf("plain resume pinned %+v; inheritance must not follow a resumed child", *resumed.Model)
	}

	// An explicit model does follow it.
	_, done, err = spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "more", Prompt: "again",
		ResumeSessionID: first, Model: "openai/gpt-5.2",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("model resume did not finish")
	}
	resumed, err = service.Get(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Model == nil || resumed.Model.ProviderID != "openai" || resumed.Model.ID != "gpt-5.2" {
		t.Fatalf("resume with model: got %+v, want openai/gpt-5.2", resumed.Model)
	}
}

// TestSpawnChildInheritsParentAgentModel covers a parent that never pinned a
// model but runs under an agent that configured one: the child inherits the
// parent agent's model, matching what the parent actually runs.
func TestSpawnChildInheritsParentAgentModel(t *testing.T) {
	provider := newGatedProvider()
	_, release := provider.script("general", "done")
	close(release)
	spawner, service, _ := newSpawnFixture(t, provider)
	// The build agent configures a model of its own.
	build := agent.Info{ID: "build", Mode: "primary", System: "build",
		Model:       &agent.ModelRef{ProviderID: "anthropic", ID: "claude-3-7-sonnet"},
		Permissions: permission.Defaults()}
	spawner.Agents.Update(build)
	root := newRootSession(t, service)

	childID, done, err := spawner.Spawn(context.Background(), tool.SpawnRequest{
		ParentSessionID: root.ID, AgentID: "general", Description: "work", Prompt: "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("subagent did not finish")
	}

	child, err := service.Get(context.Background(), childID)
	if err != nil {
		t.Fatal(err)
	}
	if child.Model == nil || child.Model.ID != "claude-3-7-sonnet" {
		t.Fatalf("child model = %+v, want the parent agent's claude-3-7-sonnet", child.Model)
	}
}
