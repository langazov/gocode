package main

import (
	"fmt"
	"os"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/session"
)

// networkFlags mirrors withNetworkOptions() in cli/network.ts: shared by
// serve, web, acp and the root tui thread command.
func networkFlags() []clix.Flag {
	return []clix.Flag{
		{Name: "port", Kind: clix.KindNumber, Default: float64(0), Describe: "port to listen on"},
		{Name: "hostname", Kind: clix.KindString, Default: "127.0.0.1", Describe: "hostname to listen on"},
		{Name: "mdns", Kind: clix.KindBool, Default: false, Describe: "enable mDNS service discovery (defaults hostname to 0.0.0.0)"},
		{Name: "mdns-domain", Kind: clix.KindString, Default: "gocode.local", Describe: "custom domain name for mDNS service (default: gocode.local)"},
		{Name: "cors", Kind: clix.KindStringArray, Describe: "additional domains to allow for CORS"},
	}
}

// replayFlags mirrors the --replay/--no-replay/--replay-limit trio shared by
// run, tui and attach. "replay" is hidden (yargs auto-negates it to
// --no-replay); handlers should read replayState() below.
func replayFlags(defaultReplay any, replayDescribe, noReplayDescribe string) []clix.Flag {
	flags := []clix.Flag{
		{Name: "replay", Kind: clix.KindBool, Default: defaultReplay, Hidden: true, Describe: replayDescribe},
		{Name: "replay-limit", Kind: clix.KindNumber, Describe: "cap visible replay to the newest N messages"},
	}
	if noReplayDescribe != "" {
		flags = append(flags, clix.Flag{Name: "no-replay", Kind: clix.KindBool, Describe: noReplayDescribe})
	}
	return flags
}

// authHeaderFlags mirrors the basic-auth --password/--username pair used by
// run --attach and attach.
func authHeaderFlags() []clix.Flag {
	return []clix.Flag{
		{Name: "password", Aliases: []string{"p"}, Kind: clix.KindString, Describe: "basic auth password (defaults to GOCODE_SERVER_PASSWORD)"},
		{Name: "username", Aliases: []string{"u"}, Kind: clix.KindString, Describe: "basic auth username (defaults to GOCODE_SERVER_USERNAME or 'gocode')"},
	}
}

// sessionSelectFlags mirrors the --continue/--session/--fork trio used by
// run, tui and attach to pick or fork a session.
func sessionSelectFlags() []clix.Flag {
	return []clix.Flag{
		{Name: "continue", Aliases: []string{"c"}, Kind: clix.KindBool, Describe: "continue the last session"},
		{Name: "session", Aliases: []string{"s"}, Kind: clix.KindString, Describe: "session id to continue"},
		{Name: "fork", Kind: clix.KindBool, Describe: "fork the session when continuing (use with --continue or --session)"},
	}
}

// notImplemented reports a command whose TypeScript backend (MCP client, LSP
// client, cloud account service, ACP, plugin installer, ...) has no Go port
// yet. The flags are still fully parsed, matching go-port-gaps.md.
func notImplemented(path string) error {
	return &usageError{msg: path + ": not yet implemented in the Go port (see specs/go-port-gaps.md)"}
}

// applyPermissionBypass implements the bypass tiers from the permissions
// recommendations (§11). The flags mean different things, and conflating them
// was a real bug: --auto's help text promised "auto-approve permissions that
// are not explicitly denied" while the implementation removed the gate
// entirely — denies included — so a pipeline stating
// `"edit": {"*.env": "deny"}` had no protection at all under it.
//
// The tiers, weakest to strongest:
//
//	auto                          asks are answered once automatically;
//	                              configured denies still enforced
//	yolo / dangerously-skip-…     gate removed entirely (dangerous!)
//
// --auto keeps the engine running behind an AutoAnswerGate, so no pending
// request is ever created (nothing parks, nothing needs answering) while the
// deny gate keeps firing. The full-bypass tier sets Permissions = nil, which
// disables denies too — that is the flag's documented meaning, and it prints a
// warning, because a silent security downgrade is how flags end up in every
// Makefile.
func applyPermissionBypass(runner *session.Runner, engine *permission.Engine, auto, bypass bool) {
	switch {
	case bypass:
		runner.Permissions = nil
		fmt.Fprintln(os.Stderr, "warning: --dangerously-skip-permissions: permission gate disabled, deny rules will not be enforced")
	case auto:
		if engine == nil {
			// No engine to answer with (a degenerate boot): behave as the
			// weaker tier rather than leaving the default gate parking on a
			// user who asked for automation.
			runner.Permissions = nil
			return
		}
		runner.Permissions = &session.AutoAnswerGate{Engine: engine}
	}
}

type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }
