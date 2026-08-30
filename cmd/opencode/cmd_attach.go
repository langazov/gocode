package main

import "github.com/anomalyco/opencode-go/internal/clix"

// attachCommand mirrors AttachCommand in cli/cmd/attach.ts ("attach <url>"):
// attach the TUI to a remote opencode server. Needs an HTTP SDK client
// against a remote server, which has no Go port yet (the Go TUI only
// attaches to servers it boots in-process, see cmd_tui.go).
func attachCommand() *clix.Command {
	flags := []clix.Flag{
		{Name: "dir", Kind: clix.KindString, Describe: "directory to run in"},
		{Name: "mini", Kind: clix.KindBool, Default: false, Describe: "start the minimal interactive interface"},
	}
	flags = append(flags, sessionSelectFlags()...)
	flags = append(flags, authHeaderFlags()...)
	flags = append(flags, replayFlags(nil, "", "disable mini session history replay on resume and after resize")...)
	return &clix.Command{
		Name:        "attach",
		Describe:    "attach to a running opencode server",
		Positionals: []clix.Positional{{Name: "url", Required: true, Describe: "http://localhost:4096"}},
		Flags:       flags,
		Run:         func(a *clix.Args) error { return notImplemented("opencode attach") },
	}
}
