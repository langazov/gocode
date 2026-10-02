//go:build !unix

package main

import "os"

// isolateStdout hands the protocol the real stdout and sends every other Go
// write in this process to stderr. Without descriptor duplication a spawned
// child could still reach the original stream; see acp_stdout_unix.go.
func isolateStdout() *os.File {
	out := os.Stdout
	os.Stdout = os.Stderr
	return out
}
