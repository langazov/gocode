package main

import (
	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/session"
)

// registerPlanAgent adds the primary agent plan mode runs under, mirroring
// agent.ts's `plan`.
//
// Without it, plan_exit's counterpart plan_enter pins an agent id no registry
// entry answers to, and AgentRulesProvider.Configured falls through to
// permission.MissingAgentPermissions — deny-all — so the session survives the
// switch but every subsequent tool call is refused.
//
// The read-only constraint is two-layered, matching upstream: `edit` denied
// here (which covers edit/write/apply_patch, since they share one permission
// action) is the hard stop, and the plan system reminder
// (internal/session/reminders.go) is what tells the model about it — including
// the parts permissions cannot express, like "don't use bash to write files".
//
// userRules is the caller's own `permission` config, merged last so a user can
// still override plan mode's defaults, exactly as agent.ts merges `user` after
// each native agent's rules.
func registerPlanAgent(registry *agent.Registry, defaults, userRules permission.Ruleset) {
	registry.Update(agent.Info{
		ID:          session.PlanAgentID,
		Mode:        "primary",
		Description: "Plan mode. Disallows all edit tools.",
		Permissions: permission.Merge(defaults, permission.Ruleset{
			{Action: "plan_exit", Resource: "*", Effect: permission.Allow},
			{Action: "edit", Resource: "*", Effect: permission.Deny},
			// The general subagent can edit; delegating to it would be a hole
			// straight through the read-only constraint. explore stays allowed.
			{Action: "task", Resource: "general", Effect: permission.Deny},
		}, userRules),
	})
}

// registerBuiltinSubagents adds the agents the task tool can spawn, mirroring
// agent.ts's `general` and `explore`. agent.Registry already filters
// mode == "subagent" out of the user-selectable list, so these never become a
// session's primary agent — they exist only as spawn targets.
//
// Shared by bootStack and `gocode agent list` so both report the same set.
func registerBuiltinSubagents(registry *agent.Registry, defaults permission.Ruleset) {
	registry.Update(agent.Info{
		ID:   "general",
		Mode: "subagent",
		Description: "General-purpose agent for researching complex questions and executing " +
			"multi-step tasks. Use this agent to execute multiple units of work in parallel.",
		// todowrite is denied upstream because the todo list belongs to the
		// parent session. Memories are the same shape of state, only more
		// durable: a subagent spawned for one task must not silently leave
		// standing instructions behind in every future session.
		Permissions: permission.Merge(defaults, permission.Ruleset{
			{Action: "todowrite", Resource: "*", Effect: permission.Deny},
			{Action: "memory_write", Resource: "*", Effect: permission.Deny},
			{Action: "memory_delete", Resource: "*", Effect: permission.Deny},
		}),
	})
	registry.Update(agent.Info{
		ID:   "explore",
		Mode: "subagent",
		Description: "Fast agent specialized for exploring codebases. Use this when you need to " +
			"quickly find files by patterns, search code for keywords, or answer questions about " +
			"the codebase. Specify the desired thoroughness: \"quick\", \"medium\", or \"very thorough\".",
		Permissions: permission.Merge(defaults, permission.Ruleset{
			{Action: "*", Resource: "*", Effect: permission.Deny},
			{Action: "grep", Resource: "*", Effect: permission.Allow},
			{Action: "glob", Resource: "*", Effect: permission.Allow},
			{Action: "read", Resource: "*", Effect: permission.Allow},
			{Action: "bash", Resource: "*", Effect: permission.Allow},
			{Action: "webfetch", Resource: "*", Effect: permission.Allow},
		}),
	})
}
