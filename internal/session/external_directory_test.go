package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/tool"
	"github.com/langazov/gocode-go/internal/tool/builtins"
)

// TestShellWritingOutsideWorkdirIsAsked is the regression for the reported
// hole: the write tool refuses a path outside the working directory, so the
// model runs `cat > /tmp/...` instead — and that used to execute with no
// prompt at all, because bash is allowed by default and nothing inspected the
// command.
func TestShellWritingOutsideWorkdirIsAsked(t *testing.T) {
	workdir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID:    "call_bash",
				Name:  "bash",
				Input: map[string]any{"command": "cat > " + outside + " << 'EOF'\npackage main\nEOF"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}

	registry := tool.NewRegistry()
	builtins.Register(registry, workdir, nil)
	runner, bus := newRunnerFixture(t, provider, registry)

	// Record what the engine is asked for, and refuse it.
	var mu sync.Mutex
	var asked []permission.Request
	// Declared first: the ask hook replies through the engine it belongs to.
	var engine *permission.Engine
	engine = permission.NewEngine(
		permission.StaticRules{Rules: permission.Defaults()},
		nil,
		permission.Hooks{OnAsked: func(request permission.Request) {
			mu.Lock()
			asked = append(asked, request)
			mu.Unlock()
			// Refuse it, so the command must not run.
			go engine.Reply(request.ID, permission.ReplyReject, "no")
		}},
		nil,
	)
	runner.Permissions = &EnginePermissionGate{Engine: engine}

	admitPrompt(t, bus, runner, "write a file outside the project")
	runner.Run(context.Background(), RunInput{SessionID: "ses_1"})

	mu.Lock()
	defer mu.Unlock()
	var sawExternal bool
	for _, request := range asked {
		if request.Action == permission.ExternalDirectoryAction {
			sawExternal = true
		}
	}
	if !sawExternal {
		t.Fatalf("writing outside the working directory via the shell must ask for %q; asked=%+v",
			permission.ExternalDirectoryAction, asked)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Error("the command ran despite the approval being refused")
	}
}

// TestShellInsideWorkdirIsNotAsked: the gate must not fire for ordinary
// commands, or every shell call becomes a prompt.
func TestShellInsideWorkdirIsNotAsked(t *testing.T) {
	workdir := t.TempDir()

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID:    "call_bash",
				Name:  "bash",
				Input: map[string]any{"command": "echo hello > inside.txt"},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventTextDelta, Text: "done"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		},
	}}

	registry := tool.NewRegistry()
	builtins.Register(registry, workdir, nil)
	runner, bus := newRunnerFixture(t, provider, registry)

	var mu sync.Mutex
	var asked []permission.Request
	// Declared first: the ask hook replies through the engine it belongs to.
	var engine *permission.Engine
	engine = permission.NewEngine(
		permission.StaticRules{Rules: permission.Defaults()},
		nil,
		permission.Hooks{OnAsked: func(request permission.Request) {
			mu.Lock()
			asked = append(asked, request)
			mu.Unlock()
			go engine.Reply(request.ID, permission.ReplyOnce, "")
		}},
		nil,
	)
	runner.Permissions = &EnginePermissionGate{Engine: engine}

	admitPrompt(t, bus, runner, "write a file in the project")
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, request := range asked {
		if request.Action == permission.ExternalDirectoryAction {
			t.Errorf("a command staying inside the working directory must not prompt: %+v", request)
		}
	}
	if data, err := os.ReadFile(filepath.Join(workdir, "inside.txt")); err != nil {
		t.Errorf("the command should have run: %v", err)
	} else if !strings.Contains(string(data), "hello") {
		t.Errorf("unexpected content %q", data)
	}
}
