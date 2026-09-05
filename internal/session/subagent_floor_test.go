package session

import (
	"testing"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/permission"
)

// A restriction on the parent has to survive delegation, or `task` is a way
// around every rule: a plan-mode session could spawn a subagent and have it
// write the files plan mode had just refused to write.
//
// The subagent's own `"*": allow` baseline used to win, because the merge put
// it last. Parent denies go last now.
func TestSubagentCannotWidenAParentDeny(t *testing.T) {
	parent := permission.Merge(permission.Defaults(), permission.Ruleset{
		{Action: "edit", Resource: "*", Effect: permission.Deny},
	})

	for _, sub := range []agent.Info{
		{ID: "general", Permissions: permission.Merge(permission.Defaults(), permission.Ruleset{
			{Action: "todowrite", Resource: "*", Effect: permission.Deny}})},
		{ID: "custom", Permissions: permission.Defaults()},
		// Even one that explicitly grants itself edit: a subagent's ruleset
		// says what it may do, not what its parent may be talked out of.
		{ID: "eager", Permissions: permission.Merge(permission.Defaults(), permission.Ruleset{
			{Action: "edit", Resource: "*", Effect: permission.Allow}})},
	} {
		derived := DeriveSubagentPermissions(parent, sub)
		if got := permission.Evaluate("edit", "main.go", derived).Effect; got != permission.Deny {
			t.Errorf("subagent %q escaped the parent's edit deny: %q", sub.ID, got)
		}
	}
}

// The floor is denies, not the parent's capabilities: a subagent's own ruleset
// still decides what it can do.
func TestSubagentKeepsItsOwnCapabilities(t *testing.T) {
	parent := permission.Merge(permission.Defaults(), permission.Ruleset{
		{Action: "edit", Resource: "*", Effect: permission.Deny},
	})
	explore := agent.Info{ID: "explore", Permissions: permission.Merge(permission.Defaults(), permission.Ruleset{
		{Action: "*", Resource: "*", Effect: permission.Deny},
		{Action: "read", Resource: "*", Effect: permission.Allow},
	})}
	derived := DeriveSubagentPermissions(parent, explore)
	if got := permission.Evaluate("read", "main.go", derived).Effect; got != permission.Allow {
		t.Errorf("explore should still read, got %q", got)
	}

	// And a parent with no restrictions imposes none.
	open := DeriveSubagentPermissions(permission.Defaults(), agent.Info{ID: "custom", Permissions: permission.Defaults()})
	if got := permission.Evaluate("edit", "main.go", open).Effect; got != permission.Allow {
		t.Errorf("an unrestricted parent should not restrict its child, got %q", got)
	}
}
