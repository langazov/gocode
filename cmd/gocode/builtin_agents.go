package main

import (
	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/permission"
)

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
