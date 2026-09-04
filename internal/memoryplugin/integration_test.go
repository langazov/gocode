package memoryplugin

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/memory"
	"github.com/langazov/gocode-go/internal/plugin"
	"github.com/langazov/gocode-go/internal/session"
	"github.com/langazov/gocode-go/internal/tool"
)

// The end-to-end seam: a memory in the database must reach the system prompt
// of a real turn.
//
// The two halves of this are covered separately — the runner triggers
// SystemTransform (internal/session/runner_plugins_test.go) and this plugin's
// hook renders the block (plugin_test.go) — but nothing else wires the actual
// plugin through the actual runner. A regression in the boot wiring, the hook
// registration, or the scope resolution would leave both of those passing and
// the feature silently dead, which is exactly the failure this catches.

// capturingProvider records the requests it is asked to stream, so the test
// can inspect the system prompt the runner assembled.
type capturingProvider struct {
	mu       sync.Mutex
	requests []llm.Request
}

func (p *capturingProvider) Stream(_ context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "ok"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn", Usage: llm.Usage{Input: 1, Output: 1}})
	return nil
}

// system waits for the turn — which the execution runs on its own goroutine —
// and returns the system prompt of the first request the runner assembled.
func (p *capturingProvider) system(t *testing.T) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		if len(p.requests) > 0 {
			system := append([]string(nil), p.requests[0].System...)
			p.mu.Unlock()
			return system
		}
		p.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the provider was never called, so no system prompt was assembled")
	return nil
}

func TestMemoryReachesTheSystemPrompt(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()

	database, err := db.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	// The project id the boot wiring would resolve.
	project, err := session.EnsureProject(ctx, database, workdir)
	if err != nil {
		t.Fatal(err)
	}

	// Seed through the store, as the HTTP routes would.
	store := memory.New(database)
	if _, err := store.Create(ctx, memory.Memory{
		Scope: project, Content: "Always run make check before pushing", Origin: memory.OriginUser,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, memory.Memory{
		Scope: "prj_somewhere_else", Content: "Another project's rule", Origin: memory.OriginUser,
	}); err != nil {
		t.Fatal(err)
	}

	// Load the plugin the way bootStack does, through the real loader and the
	// native registry, rather than calling New directly.
	host, err := plugin.Load(ctx, plugin.LoadInput{
		Input: plugin.Input{
			Directory: workdir,
			Worktree:  workdir,
			Services:  plugin.Services{DB: database, ProjectID: project},
		},
		Report: &plugin.Report{
			Error: func(spec plugin.Spec, stage plugin.Stage, err error) {
				t.Errorf("plugin %s failed at %s: %v", spec.Ref, stage, err)
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !host.Registered(plugin.SystemTransform.Name()) {
		t.Fatal("the memory plugin did not register the system transform hook")
	}

	bus := event.NewBus(database)
	session.RegisterProjectors(bus)
	session.RegisterRunnerProjectors(bus)

	provider := &capturingProvider{}
	runner := &session.Runner{
		DB:       database,
		Bus:      bus,
		Messages: session.NewMessageStore(database),
		Provider: provider,
		Tools:    tool.NewRegistry(),
		Agent:    "build",
		System:   "You are gocode.",
		Model:    session.ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
		Plugins:  host,
	}
	service := session.NewService(database, bus)
	service.Execution = session.NewExecution(&session.DBSessionLookup{DB: database}, runner)

	info, err := service.Create(ctx, session.CreateInput{Directory: workdir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Prompt(ctx, info.ID, "hello", session.DeliveryQueue); err != nil {
		t.Fatal(err)
	}

	system := provider.system(t)
	joined := strings.Join(system, "\n")

	if !strings.Contains(joined, "Always run make check before pushing") {
		t.Errorf("the memory never reached the system prompt:\n%s", joined)
	}
	if !strings.Contains(joined, "<memories>") {
		t.Errorf("the memory block is missing its wrapper:\n%s", joined)
	}
	if strings.Contains(joined, "Another project's rule") {
		t.Errorf("another project's memory leaked into the prompt:\n%s", joined)
	}
	// The agent's own prompt must survive; the block is an addition, not a
	// replacement.
	if !strings.Contains(joined, "You are gocode.") {
		t.Errorf("the agent's system prompt was lost:\n%s", joined)
	}
	// Appended last, so a memory edit invalidates as little of a cached
	// prefix as possible.
	if system[len(system)-1] == "You are gocode." {
		t.Error("the memory block should be appended after the agent prompt")
	}
}

// With no memories stored, the plugin must leave the prompt exactly as it
// found it — an empty block on every request is pure overhead.
func TestNoMemoriesLeavesPromptUnchanged(t *testing.T) {
	ctx := context.Background()
	workdir := t.TempDir()

	database, err := db.OpenAndMigrate(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	project, err := session.EnsureProject(ctx, database, workdir)
	if err != nil {
		t.Fatal(err)
	}

	host, err := plugin.Load(ctx, plugin.LoadInput{
		Input: plugin.Input{
			Directory: workdir,
			Worktree:  workdir,
			Services:  plugin.Services{DB: database, ProjectID: project},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	bus := event.NewBus(database)
	session.RegisterProjectors(bus)
	session.RegisterRunnerProjectors(bus)

	provider := &capturingProvider{}
	runner := &session.Runner{
		DB:       database,
		Bus:      bus,
		Messages: session.NewMessageStore(database),
		Provider: provider,
		Tools:    tool.NewRegistry(),
		Agent:    "build",
		System:   "You are gocode.",
		Model:    session.ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"},
		Plugins:  host,
	}
	service := session.NewService(database, bus)
	service.Execution = session.NewExecution(&session.DBSessionLookup{DB: database}, runner)

	info, err := service.Create(ctx, session.CreateInput{Directory: workdir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Prompt(ctx, info.ID, "hello", session.DeliveryQueue); err != nil {
		t.Fatal(err)
	}

	system := provider.system(t)
	if len(system) != 1 || system[0] != "You are gocode." {
		t.Errorf("system = %v, want just the agent prompt", system)
	}
}
