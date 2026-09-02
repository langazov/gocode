package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/anomalyco/opencode-go/internal/llm"
	"github.com/anomalyco/opencode-go/internal/permission"
	"github.com/anomalyco/opencode-go/internal/tool"
	"github.com/anomalyco/opencode-go/internal/tool/builtins"
)

// TestExternalDirectoryAlwaysIsAskedOnce is the regression for "the access
// request is appearing very often": replying "always" to an external_directory
// prompt used to be indistinguishable from "once".
//
// Two things were missing and either one alone reproduces it. The runner never
// set Save on the permission input, so Engine.Reply's `len(Save) > 0` guard
// meant "always" saved nothing; and main.go passed a nil SavedStore, so there
// was nowhere to save it to. The prompt therefore returned on the next command
// touching the same directory, forever.
//
// The second command here writes into a *subdirectory* of the approved one,
// which is the case the user actually hits: approving a directory has to cover
// its subtree, not just files sitting directly in it.
func TestExternalDirectoryAlwaysIsAskedOnce(t *testing.T) {
	workdir := t.TempDir()
	external := t.TempDir()
	first := filepath.Join(external, "one.txt")
	nested := filepath.Join(external, "sub", "deeper", "two.txt")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}

	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID:    "call_one",
				Name:  "bash",
				Input: map[string]any{"command": "echo one > " + first},
			}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
				ID:    "call_two",
				Name:  "bash",
				Input: map[string]any{"command": "echo two > " + nested},
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
	var externalAsks []permission.Request
	var engine *permission.Engine
	engine = permission.NewEngine(
		permission.StaticRules{Rules: permission.Defaults()},
		NewSavedPermissions(runner.DB, workdir),
		permission.Hooks{OnAsked: func(request permission.Request) {
			if request.Action == permission.ExternalDirectoryAction {
				mu.Lock()
				externalAsks = append(externalAsks, request)
				mu.Unlock()
			}
			go engine.Reply(request.ID, permission.ReplyAlways, "")
		}},
		nil,
	)
	runner.Permissions = &EnginePermissionGate{Engine: engine}

	admitPrompt(t, bus, runner, "write two files outside the project")
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{first, nested} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("approved command did not run: %v", err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(externalAsks) != 1 {
		var saved []string
		for _, request := range externalAsks {
			saved = append(saved, request.Action+" "+strings.Join(request.Resources, ","))
		}
		t.Fatalf("external_directory asked %d times, want 1 — an \"always\" reply must cover the "+
			"directory and everything under it; asks: %v", len(externalAsks), saved)
	}
	if len(externalAsks[0].Save) == 0 {
		t.Error("the request carried no Save, so Engine.Reply had nothing to persist")
	}
}

// TestSavedPermissionsSurviveANewProcess: the grant has to be on disk, not in
// the engine. A store built fresh against the same database — which is what
// the next `opencode` run does — must see it.
func TestSavedPermissionsSurviveANewProcess(t *testing.T) {
	_, database := setup(t)
	workdir := t.TempDir()

	first := NewSavedPermissions(database, workdir)
	if err := first.Add(permission.ExternalDirectoryAction, []string{"/srv/data/*"}); err != nil {
		t.Fatal(err)
	}

	second := NewSavedPermissions(database, workdir)
	rules, err := second.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("a fresh store read %d rules, want 1: %+v", len(rules), rules)
	}
	want := permission.Rule{
		Action:   permission.ExternalDirectoryAction,
		Resource: "/srv/data/*",
		Effect:   permission.Allow,
	}
	if rules[0] != want {
		t.Fatalf("rule = %+v, want %+v", rules[0], want)
	}
}

// TestSavedPermissionsAreIdempotent: two tool calls reaching the same
// directory can both be approved, and the unique index must absorb the second
// write rather than fail the reply.
func TestSavedPermissionsAreIdempotent(t *testing.T) {
	_, database := setup(t)
	store := NewSavedPermissions(database, t.TempDir())

	for i := 0; i < 3; i++ {
		if err := store.Add("read", []string{"*"}); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	rules, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("re-approving stored %d rules, want 1: %+v", len(rules), rules)
	}
}

// TestSavedPermissionsAreScopedToTheProject: a grant made in one worktree must
// not silently apply to another. The table is keyed by project for exactly
// this reason.
func TestSavedPermissionsAreScopedToTheProject(t *testing.T) {
	_, database := setup(t)

	here := NewSavedPermissions(database, t.TempDir())
	elsewhere := NewSavedPermissions(database, t.TempDir())
	if err := here.Add(permission.ExternalDirectoryAction, []string{"/srv/data/*"}); err != nil {
		t.Fatal(err)
	}

	rules, err := elsewhere.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 0 {
		t.Fatalf("a grant leaked into another project: %+v", rules)
	}
}

// TestSavedExternalDirectoryCoversSubdirectories pins the glob semantics the
// whole feature rests on: the saved resource is `dir/*`, and `*` compiles to
// `.*`, which crosses path separators. Narrowing that to a single segment
// would silently reintroduce the repeated prompt.
func TestSavedExternalDirectoryCoversSubdirectories(t *testing.T) {
	saved := permission.Ruleset{{
		Action:   permission.ExternalDirectoryAction,
		Resource: "/srv/data/*",
		Effect:   permission.Allow,
	}}
	rules := permission.Merge(permission.Defaults(), saved)

	for _, resource := range []string{
		"/srv/data/*",                 // the directory itself, re-asked
		"/srv/data/sub/*",             // a subdirectory
		"/srv/data/sub/deeper/*",      // arbitrarily deep
		"/srv/data/sub/deeper/x.json", // a file, should a caller ask about one
	} {
		if got := permission.Evaluate(permission.ExternalDirectoryAction, resource, rules).Effect; got != permission.Allow {
			t.Errorf("Evaluate(%q) = %q, want allow — approving a directory must cover its subtree", resource, got)
		}
	}

	// A sibling is not covered: the grant is a subtree, not the whole disk.
	for _, resource := range []string{"/srv/other/*", "/srv/*", "/*"} {
		if got := permission.Evaluate(permission.ExternalDirectoryAction, resource, rules).Effect; got != permission.Ask {
			t.Errorf("Evaluate(%q) = %q, want ask — the grant must not widen past the approved directory", resource, got)
		}
	}
}
