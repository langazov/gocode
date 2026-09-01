package session

import (
	"github.com/anomalyco/opencode-go/internal/agent"
	"github.com/anomalyco/opencode-go/internal/permission"
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
// parent session's grants first, then the subagent's own ruleset (later rules
// win under permission.Evaluate's last-match-wins), then explicit denies for
// the tools a subagent must not reach.
//
// Ports deriveSubagentSessionPermission from
// packages/opencode/src/agent/subagent-permissions.ts.
func DeriveSubagentPermissions(parent permission.Ruleset, subagent agent.Info) permission.Ruleset {
	out := permission.Merge(parent, subagent.Permissions)
	for _, denied := range SubagentDeniedTools {
		if mentions(subagent.Permissions, denied) {
			// The subagent was explicitly configured for this tool; respect it.
			continue
		}
		out = append(out, permission.Rule{Action: denied, Resource: "*", Effect: permission.Deny})
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
