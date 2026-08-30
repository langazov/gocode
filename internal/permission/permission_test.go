package permission

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		input   string
		pattern string
		want    bool
	}{
		{"bash", "bash", true},
		{"bash", "bas", false},
		{"bash", "b*", true},
		{"edit", "*", true},
		{"src/foo.ts", "src/*.ts", true},
		{"src/deep/foo.ts", "src/*.ts", true},
		{"a.txt", "a.t?t", true},
		{"a.txt", "a.tx?", true},
		{"read file.txt", "read *", true},
		{"read file.txt", "read", false},
		{"bash rm -rf /", "bash rm *", true},
		{"bash", "bash rm *", false},
	}
	for _, c := range cases {
		if got := Match(c.input, c.pattern); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.input, c.pattern, got, c.want)
		}
	}
}

func TestMatchTrailingSpaceStar(t *testing.T) {
	// "bash *" should match "bash" alone (the TS " .*" -> "( .*)?" rule)
	if !Match("bash", "bash *") {
		t.Fatal(`Match("bash", "bash *") should be true`)
	}
	if !Match("bash ls", "bash *") {
		t.Fatal(`Match("bash ls", "bash *") should be true`)
	}
}

func TestEvaluateLastMatchWins(t *testing.T) {
	rules := Ruleset{
		{Action: "*", Resource: "*", Effect: Deny},
		{Action: "bash", Resource: "*", Effect: Allow},
	}
	got := Evaluate("bash", "ls", rules)
	if got.Effect != Allow {
		t.Fatalf("expected last matching rule to win, got %v", got.Effect)
	}
	got = Evaluate("edit", "file", rules)
	if got.Effect != Deny {
		t.Fatalf("expected deny, got %v", got.Effect)
	}
}

func TestEvaluateDefaultsToAsk(t *testing.T) {
	got := Evaluate("bash", "ls")
	if got.Effect != Ask {
		t.Fatalf("expected default ask, got %v", got.Effect)
	}
}

func TestAssertAllow(t *testing.T) {
	engine := NewEngine(StaticRules{Rules: Ruleset{{Action: "*", Resource: "*", Effect: Allow}}}, nil, Hooks{}, nil)
	err := engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}})
	if err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
}

func TestAssertDeny(t *testing.T) {
	engine := NewEngine(StaticRules{Rules: Ruleset{{Action: "bash", Resource: "*", Effect: Deny}}}, nil, Hooks{}, nil)
	err := engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}})
	var blocked *BlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("expected BlockedError, got %v", err)
	}
	if len(blocked.Rules) == 0 {
		t.Fatal("expected relevant rules in BlockedError")
	}
}

func TestAssertAskWaitsForReply(t *testing.T) {
	engine := NewEngine(StaticRules{Rules: Ruleset{{Action: "bash", Resource: "*", Effect: Ask}}}, nil, Hooks{}, nil)
	done := make(chan error, 1)
	go func() {
		done <- engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}})
	}()
	var requestID string
	for i := 0; i < 100 && requestID == ""; i++ {
		if pending := engine.List(); len(pending) > 0 {
			requestID = pending[0].ID
		}
		time.Sleep(time.Millisecond)
	}
	if requestID == "" {
		t.Fatal("expected a pending permission request")
	}
	if err := engine.Reply(requestID, ReplyOnce, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected approval, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("assert did not resolve after reply")
	}
}

func TestReplyRejectDeclines(t *testing.T) {
	engine := NewEngine(StaticRules{Rules: Ruleset{{Action: "bash", Resource: "*", Effect: Ask}}}, nil, Hooks{}, nil)
	done := make(chan error, 1)
	go func() {
		done <- engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}})
	}()
	var requestID string
	for i := 0; i < 100 && requestID == ""; i++ {
		if pending := engine.List(); len(pending) > 0 {
			requestID = pending[0].ID
		}
		time.Sleep(time.Millisecond)
	}
	if err := engine.Reply(requestID, ReplyReject, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrDeclined) {
			t.Fatalf("expected declined, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("assert did not resolve after reject")
	}
}

func TestReplyRejectWithFeedback(t *testing.T) {
	engine := NewEngine(StaticRules{Rules: Ruleset{{Action: "bash", Resource: "*", Effect: Ask}}}, nil, Hooks{}, nil)
	done := make(chan error, 1)
	go func() {
		done <- engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}})
	}()
	var requestID string
	for i := 0; i < 100 && requestID == ""; i++ {
		if pending := engine.List(); len(pending) > 0 {
			requestID = pending[0].ID
		}
		time.Sleep(time.Millisecond)
	}
	if err := engine.Reply(requestID, ReplyReject, "use a safer command"); err != nil {
		t.Fatal(err)
	}
	err := <-done
	var corrected *CorrectedError
	if !errors.As(err, &corrected) {
		t.Fatalf("expected CorrectedError, got %v", err)
	}
	if corrected.Feedback != "use a safer command" {
		t.Fatalf("unexpected feedback: %s", corrected.Feedback)
	}
}

func TestRejectCascadesWithinSession(t *testing.T) {
	engine := NewEngine(StaticRules{Rules: Ruleset{{Action: "*", Resource: "*", Effect: Ask}}}, nil, Hooks{}, nil)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		first <- engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"a"}})
	}()
	go func() {
		second <- engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "edit", Resources: []string{"b"}})
	}()
	var requestID string
	for i := 0; i < 100 && requestID == ""; i++ {
		if pending := engine.List(); len(pending) > 0 {
			requestID = pending[0].ID
		}
		time.Sleep(time.Millisecond)
	}
	if err := engine.Reply(requestID, ReplyReject, ""); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []chan error{first, second} {
		select {
		case err := <-ch:
			if !errors.Is(err, ErrDeclined) {
				t.Fatalf("expected cascade decline, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("pending request did not resolve after cascade reject")
		}
	}
}

func TestAlwaysSavesAndCascades(t *testing.T) {
	saved := &MemorySaved{}
	engine := NewEngine(StaticRules{Rules: Ruleset{}}, saved, Hooks{}, nil)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		first <- engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}, Save: []string{"ls"}})
	}()
	go func() {
		second <- engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}})
	}()
	var requestID string
	for i := 0; i < 100 && requestID == ""; i++ {
		for _, pending := range engine.List() {
			if len(pending.Save) > 0 {
				requestID = pending.ID
			}
		}
		time.Sleep(time.Millisecond)
	}
	if requestID == "" {
		t.Fatal("expected pending request with save")
	}
	if err := engine.Reply(requestID, ReplyAlways, ""); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []chan error{first, second} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("expected always approval to resolve both, got %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("pending request did not resolve after always reply")
		}
	}
	rules, _ := saved.List()
	if len(rules) != 1 || rules[0].Resource != "ls" {
		t.Fatalf("expected saved allow rule, got %v", rules)
	}
}

func TestAskReturnsEffectWithoutWaiting(t *testing.T) {
	engine := NewEngine(StaticRules{Rules: Ruleset{{Action: "bash", Resource: "*", Effect: Ask}}}, nil, Hooks{}, nil)
	id, effect, err := engine.Ask(AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}})
	if err != nil {
		t.Fatal(err)
	}
	if effect != Ask || id == "" {
		t.Fatalf("expected ask effect with id, got %v %s", effect, id)
	}
	if engine.Get(id) == nil {
		t.Fatal("expected pending request from ask")
	}
}

func TestSavedAllowOverridesAsk(t *testing.T) {
	saved := &MemorySaved{}
	saved.Add("bash", []string{"ls"})
	engine := NewEngine(StaticRules{Rules: Ruleset{{Action: "*", Resource: "*", Effect: Ask}}}, saved, Hooks{}, nil)
	err := engine.Assert(context.Background(), AssertInput{SessionID: "s", Action: "bash", Resources: []string{"ls"}})
	if err != nil {
		t.Fatalf("expected saved allow to win, got %v", err)
	}
}

// TestDefaultsAllowsEverythingExceptEnvFiles guards the TS-parity fix: an
// unlisted action (edit, bash, todowrite, any MCP tool name, ...) must be
// allowed by Defaults()'s "*" catch-all, matching agent.ts's real default —
// not fall through to Evaluate's unmatched-rule "ask" the way it did before
// this port had any "*" rule of its own (see go-port-gaps.md).
func TestDefaultsAllowsEverythingExceptEnvFiles(t *testing.T) {
	defaults := Defaults()
	cases := []struct {
		action, resource string
		want             Effect
	}{
		{"bash", "rm -rf /", Allow},
		{"edit", "main.go", Allow},
		{"write", "main.go", Allow},
		{"todowrite", "*", Allow},
		{"chrome-devtools_navigate", "*", Allow}, // an MCP tool's namespaced action
		{"read", "main.go", Allow},
		{"read", "config/.env", Ask},
		{"read", "config/.env.local", Ask},
		{"read", "config/.env.example", Allow},
	}
	for _, c := range cases {
		if got := Evaluate(c.action, c.resource, defaults).Effect; got != c.want {
			t.Errorf("Evaluate(%q, %q, Defaults()) = %v, want %v", c.action, c.resource, got, c.want)
		}
	}
}
