package session

import (
	"context"
	"errors"
	"testing"

	"github.com/langazov/gocode-go/internal/permission"
)

// TestAutoAnswerGateAnswersAsksAndKeepsDenies pins the --auto tier (§11): an
// ask resolves without waiting on a user, while a configured deny still fails
// the call. This is the pair of defects the old implementation had —
// Runner.Permissions = nil removed both — and the flag's help text promised
// the deny half all along.
func TestAutoAnswerGateAnswersAsksAndKeepsDenies(t *testing.T) {
	rules := permission.StaticRules{Rules: permission.Ruleset{
		{Action: "edit", Resource: "*", Effect: permission.Ask},
		{Action: "edit", Resource: "*.env", Effect: permission.Deny},
		{Action: "bash", Resource: "*", Effect: permission.Ask},
	}}
	engine := permission.NewEngine(rules, &permission.MemorySaved{}, permission.Hooks{}, nil)
	gate := &AutoAnswerGate{Engine: engine}
	input := ToolPermissionInput{
		SessionID: "s",
		Agent:     "build",
		Action:    "edit",
		Resources: []string{"src/main.go"},
		Save:      []string{"*"},
	}
	if err := gate.Assert(context.Background(), input); err != nil {
		t.Fatalf("ask should be answered once: %v", err)
	}
	// A denied resource still fails, through the same BlockedError the
	// default gate returns.
	deniedInput := input
	deniedInput.Resources = []string{"prod.env"}
	err := gate.Assert(context.Background(), deniedInput)
	if err == nil {
		t.Fatal("configured deny must survive --auto")
	}
	var blocked *permission.BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("want BlockedError, got %T: %v", err, err)
	}
	// No grant is ever written: answering once must not persist anything.
	saved := &permission.MemorySaved{}
	engine = permission.NewEngine(rules, saved, permission.Hooks{}, nil)
	gate = &AutoAnswerGate{Engine: engine}
	for i := 0; i < 3; i++ {
		if err := gate.Assert(context.Background(), input); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if rules, err := saved.List(); err != nil || len(rules) != 0 {
		t.Fatalf("--auto wrote %d grant(s), want 0 (err=%v)", len(rules), err)
	}
	// And nothing parks: the engine's pending map stays empty.
	if pending := engine.List(); len(pending) != 0 {
		t.Fatalf("AutoAnswerGate left %d pending request(s)", len(pending))
	}
}
