package main

import "github.com/anomalyco/opencode-go/internal/clix"

// consoleCommand mirrors ConsoleCommand in cli/cmd/account.ts ("console",
// describe: false — hidden in the TS CLI too). The cloud account/org system
// it manages is server-side and has no Go port; flags are still recognized.
func consoleCommand() *clix.Command {
	return &clix.Command{
		Name:   "console",
		Hidden: true,
		Demand: true,
		Sub: []*clix.Command{
			{Name: "login", Describe: "log in to console", Positionals: []clix.Positional{{Name: "url", Describe: "server URL"}},
				Run: func(a *clix.Args) error { return notImplemented("opencode console login") }},
			{Name: "logout", Describe: "log out from console", Positionals: []clix.Positional{{Name: "email", Describe: "account email to log out from"}},
				Run: func(a *clix.Args) error { return notImplemented("opencode console logout") }},
			{Name: "switch", Describe: "switch active org", Run: func(a *clix.Args) error { return notImplemented("opencode console switch") }},
			{Name: "orgs", Describe: "list orgs", Run: func(a *clix.Args) error { return notImplemented("opencode console orgs") }},
			{Name: "open", Describe: "open active console account", Run: func(a *clix.Args) error { return notImplemented("opencode console open") }},
		},
	}
}
