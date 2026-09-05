package session

import (
	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/permission"
)

// SubagentDeniedTools are the tools a subagent may not use unless its own
// ruleset names them explicitly.
//
//   - task: a subagent that can spawn subagents defeats the depth limit, since
//     each level would re-derive its own budget.
//   - todowrite: the todo list belongs to the primary session; a subagent
//     writing to it corrupts the parent's plan.
//
// Mirrors childToolDenies in packages/opencode/src/tool/task.ts.
var SubagentDeniedTools = []string{"task", "todowrite"}

// DeriveSubagentPermissions builds the ruleset for a child session: the
// subagent's own capabilities first, then the parent's denies as a floor it
// cannot widen, then explicit denies for the tools a subagent must not reach.
//
// The order is the whole point, and it used to be the other way round. Under
// permission.Evaluate's last-match-wins, putting the subagent last let its own
// `"*": allow` baseline re-grant everything the parent had denied — so a
// plan-mode session could delegate to a subagent and have it write the files
// plan mode had just refused to write. Parent denies go last so a restriction
// survives delegation however many levels deep it goes.
//
// Ports deriveSubagentSessionPermission from
// packages/opencode/src/agent/subagent-permissions.ts, which carries the same
// parent denies (plus external_directory) for the same reason.
func DeriveSubagentPermissions(parent permission.Ruleset, subagent agent.Info) permission.Ruleset {
	out := permission.Merge(subagent.Permissions, parentFloor(parent))
	for _, denied := range SubagentDeniedTools {
		if mentions(subagent.Permissions, denied) {
			// The subagent was explicitly configured for this tool; respect it.
			continue
		}
		out = append(out, permission.Rule{Action: denied, Resource: "*", Effect: permission.Deny})
	}
	return out
}

// parentFloor is the part of a parent's ruleset a child inherits: what the
// parent was refused, and where it was allowed to reach outside the working
// directory.
//
// Allows are deliberately not carried down — a parent's capabilities are its
// own, and the subagent's ruleset decides what the subagent can do. Only the
// restrictions travel. The one exception is external_directory, whose allows
// come along because they are the parent's answer to "which directories may be
// touched at all", which a child cannot sensibly re-derive.
func parentFloor(parent permission.Ruleset) permission.Ruleset {
	var out permission.Ruleset
	for _, rule := range parent {
		if rule.Effect == permission.Deny || rule.Action == permission.ExternalDirectoryAction {
			out = append(out, rule)
		}
	}
	return out
}

// mentions reports whether a ruleset names an action explicitly. A wildcard
// action does not count: "*": allow is the default baseline every agent
// carries, and treating it as an opt-in would hand every subagent the task
// tool back.
func mentions(ruleset permission.Ruleset, action string) bool {
	for _, rule := range ruleset {
		if rule.Action == action {
			return true
		}
	}
	return false
}
