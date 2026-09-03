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
