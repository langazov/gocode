//go:build unix

package main

import (
	"os"

	"golang.org/x/sys/unix"
)

// isolateStdout hands the protocol a private copy of stdout and points file
// descriptor 1 at stderr. Reassigning os.Stdout alone only redirects this
// process's Go writes; a child that inherits descriptor 1 — an MCP server, a
// plugin, anything a tool spawns without its own pipe — would still write
// straight into the ACP stream and corrupt it.
func isolateStdout() *os.File {
	protocolFD, err := unix.Dup(int(os.Stdout.Fd()))
	if err != nil {
		out := os.Stdout
		os.Stdout = os.Stderr
		return out
	}
	unix.CloseOnExec(protocolFD)
	if err := unix.Dup2(int(os.Stderr.Fd()), int(os.Stdout.Fd())); err != nil {
		unix.Close(protocolFD)
		out := os.Stdout
		os.Stdout = os.Stderr
		return out
	}
	return os.NewFile(uintptr(protocolFD), "acp-stdout")
}
