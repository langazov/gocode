package session

import (
	"testing"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/tool"
	"github.com/langazov/gocode-go/internal/tool/builtins"
)

// TestPermissionResourcesMatchTheToolInput pins the field each tool's
// permission resource is read from, against the TypeScript tool that passes it
// to permission.assert.
//
// Four of these were reading input["path"], a field the tool does not have.
// The resource fell through to "*", which is not a wildcard when it appears on
// the *input* side of a match: Evaluate("webfetch", "*", rules) matches
// neither `{"https://docs.example/*": "allow"}` nor a deny written the same
// way, so every URL-, query- or path-scoped rule stopped applying.
func TestPermissionResourcesMatchTheToolInput(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  string
		// origin names the TypeScript tool the mapping is taken from.
		origin string
	}{
		{"read", map[string]any{"path": "src/a.go"}, "src/a.go", "read.ts:[target.resource]"},
		{"edit", map[string]any{"path": "src/a.go"}, "src/a.go", "edit.ts:[target.resource]"},
		{"write", map[string]any{"path": "src/a.go"}, "src/a.go", "write.ts:[target.resource]"},
		{"glob", map[string]any{"pattern": "**/*.go"}, "**/*.go", "glob.ts:[input.pattern]"},
		{"grep", map[string]any{"pattern": "TODO"}, "TODO", "grep.ts:[input.pattern]"},
		{"bash", map[string]any{"command": "ls -la"}, "ls -la", "bash.ts:[input.command]"},
		{"webfetch", map[string]any{"url": "https://x.example/y"}, "https://x.example/y", "webfetch.ts:[input.url]"},
		{"websearch", map[string]any{"query": "how to go"}, "how to go", "websearch.ts:[input.query]"},
		{"skill", map[string]any{"name": "deploy"}, "deploy", "skill.ts:[skill.name]"},
		{"todowrite", map[string]any{"todos": []any{}}, "*", "todowrite.ts:[\"*\"]"},
		{"task", map[string]any{"subagent_type": "explore"}, "explore", "task patterns:[subagent_type]"},
	}
	for _, c := range cases {
		got := permissionResources(c.tool, c.input)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: resources = %v, want [%q] (%s)", c.tool, got, c.want, c.origin)
		}
	}
}

// TestBashAlwaysSavesArityPrefix pins the §4.4 save contract end to end:
// approving "always" on `git commit -m a` must let `git commit -m b` through
// without re-asking (the arity prefix covers the subcommand) while `git push`
// still asks (the prefix covers nothing else). This is the exact scenario the
// recommendations describe — saving the literal command made the next commit
// re-prompt, and users read the prompt as broken.
func TestBashAlwaysSavesArityPrefix(t *testing.T) {
	saved := &permission.MemorySaved{}
	engine := permission.NewEngine(
		permission.StaticRules{Rules: permission.Ruleset{
			{Action: "bash", Resource: "*", Effect: permission.Ask},
		}}, saved, permission.Hooks{}, nil)

	// Approve-always `git commit -m a` through the same derivation the
	// runner uses.
	resources := permissionResources("bash", map[string]any{"command": "git commit -m a"})
	savePatterns := permissionSave("bash", resources)
	if len(savePatterns) != 1 || savePatterns[0] != "git commit *" {
		t.Fatalf("save patterns = %v, want [git commit *]", savePatterns)
	}
	if err := saved.Add("bash", savePatterns); err != nil {
		t.Fatal(err)
	}

	// `git commit -m b` is covered: no ask, no pending request.
	if _, effect, err := engine.Ask(permission.AssertInput{
		SessionID: "s", Action: "bash", Resources: []string{"git commit -m b"},
	}); err != nil || effect != permission.Allow {
		t.Fatalf("git commit -m b: effect=%v err=%v, want allow (the saved prefix covers it)", effect, err)
	}
	if pending := engine.List(); len(pending) != 0 {
		t.Fatalf("git commit -m b parked on the user: %d pending", len(pending))
	}
	// `git push` is not: it must still ask.
	if _, effect, err := engine.Ask(permission.AssertInput{
		SessionID: "s", Action: "bash", Resources: []string{"git push"},
	}); err != nil || effect != permission.Ask {
		t.Fatalf("git push: effect=%v err=%v, want ask (the prefix covers nothing else)", effect, err)
	}
}

// TestPermissionSaveMatchesTypeScript pins what "allow always" persists.
//
// Saving too little means the prompt returns on the next call; saving too much
// silently grants more than the person agreed to. skill was doing the latter —
// approving one skill saved "*", which allows every skill.
func TestPermissionSaveMatchesTypeScript(t *testing.T) {
	cases := []struct {
		tool  string
		input map[string]any
		want  string
	}{
		// The file tools save "*": the question answered is "may you read
		// files", and saving the path would re-ask for the next one.
		{"read", map[string]any{"path": "a.go"}, "*"},
		{"edit", map[string]any{"path": "a.go"}, "*"},
		{"glob", map[string]any{"pattern": "*.go"}, "*"},
		{"webfetch", map[string]any{"url": "https://x.example"}, "*"},
		{"websearch", map[string]any{"query": "q"}, "*"},
		// These two save what was approved, in the narrowest durable form:
		// bash saves the arity prefix (git commit -m a → "git commit *"), so
		// one approval covers the subcommand without covering every command;
		// skill saves the skill's name.
		{"bash", map[string]any{"command": "git status"}, "git status *"},
		{"skill", map[string]any{"name": "deploy"}, "deploy"},
	}
	for _, c := range cases {
		got := permissionSave(c.tool, permissionResources(c.tool, c.input))
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: save = %v, want [%q]", c.tool, got, c.want)
		}
	}
}

// TestApplyPatchIsEvaluatedAgainstItsTargets is the regression for a
// permission bypass: apply_patch's input is a patch, not a path, so the
// resource collapsed to "*" and every path-scoped edit rule stopped applying.
//
// The edit tool refused to touch .env under this exact ruleset while the
// identical change went through as a patch.
func TestApplyPatchIsEvaluatedAgainstItsTargets(t *testing.T) {
	rules := permission.Ruleset{
		{Action: "*", Resource: "*", Effect: permission.Allow},
		{Action: "edit", Resource: "*.env", Effect: permission.Deny},
	}

	registry := tool.NewRegistry()
	builtins.Register(registry, t.TempDir(), nil)
	runner := &Runner{Tools: registry}

	patchText := "*** Begin Patch\n*** Update File: .env\n@@\n-A=1\n+A=2\n*** End Patch"
	resources := runner.permissionResourcesFor(llm.ToolCall{
		Name:  "apply_patch",
		Input: map[string]any{"patchText": patchText},
	})

	if len(resources) != 1 || resources[0] != ".env" {
		t.Fatalf("resources = %v, want [\".env\"] — the patch's targets, not \"*\"", resources)
	}
	if got := permission.Evaluate("edit", resources[0], rules).Effect; got != permission.Deny {
		t.Errorf("apply_patch on .env = %q, want deny; the edit tool is denied for the same path, "+
			"so a patch must not be a way around it", got)
	}
}

// TestApplyPatchAsksForEveryFileItTouches: a patch is not one file, and a move
// writes two. Approving the first target must not carry the rest.
func TestApplyPatchAsksForEveryFileItTouches(t *testing.T) {
	registry := tool.NewRegistry()
	builtins.Register(registry, t.TempDir(), nil)
	runner := &Runner{Tools: registry}

	patchText := "*** Begin Patch\n" +
		"*** Add File: a.txt\n+one\n" +
		"*** Delete File: b.txt\n" +
		"*** Update File: c.txt\n*** Move to: d.txt\n@@\n-x\n+y\n" +
		"*** End Patch"
	got := runner.permissionResourcesFor(llm.ToolCall{
		Name:  "apply_patch",
		Input: map[string]any{"patchText": patchText},
	})

	want := map[string]bool{"a.txt": true, "b.txt": true, "c.txt": true, "d.txt": true}
	if len(got) != len(want) {
		t.Fatalf("resources = %v, want one per touched file %v", got, want)
	}
	for _, resource := range got {
		if !want[resource] {
			t.Errorf("unexpected resource %q in %v", resource, got)
		}
	}
}
