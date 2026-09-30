package tool

import (
	"context"
	"sync"
	"time"
)

// SessionEnv widens or redirects what the built-in tools do for one session.
//
// It exists for hosts that attach a client to a session and let that client
// shape the tools' environment — today only the ACP agent (internal/acp),
// whose editor clients may add workspace roots and may offer their own file
// system and terminal (agentclientprotocol.com/protocol/v1/file-system,
// /terminals). A session with no environment registered runs exactly as it
// always has: rooted at the runtime's directory, on the local disk.
type SessionEnv struct {
	// Roots are extra absolute directories the file tools may reach besides
	// the runtime root — the session's additional workspace roots.
	Roots []string
	// FS, when set, serves text file reads and writes instead of the local
	// disk, so the agent sees (and edits) the editor's unsaved buffers.
	FS ClientFS
	// Terminal, when set, runs shell commands instead of a local process, so
	// the output streams into the editor's own terminal.
	Terminal ClientTerminal
}

// ClientFS reads and writes text files through a client.
type ClientFS interface {
	// CanRead and CanWrite report which halves the client offered; a client
	// may advertise one without the other.
	CanRead() bool
	CanWrite() bool
	ReadTextFile(ctx context.Context, path string) (string, error)
	WriteTextFile(ctx context.Context, path, content string) error
}

// ClientTerminal runs a shell command through a client.
type ClientTerminal interface {
	Run(ctx context.Context, request TerminalRequest) (TerminalResult, error)
}

// TerminalRequest is one command to run.
type TerminalRequest struct {
	// SessionID and CallID tie the terminal to the tool call it serves, so
	// the client can show it inline.
	SessionID string
	CallID    string
	// Command is a shell command line; the client runs it through a shell.
	Command string
	Cwd     string
	Timeout time.Duration
	// OutputLimit bounds the bytes the client retains; zero means its own
	// default.
	OutputLimit int
}

// TerminalResult is how a command ended.
type TerminalResult struct {
	Output    string
	Truncated bool
	// ExitCode is nil when the process was ended by a signal.
	ExitCode *int
	Signal   string
	TimedOut bool
}

var (
	envMu sync.RWMutex
	envs  = map[string]*SessionEnv{}
)

// SetSessionEnv registers env for sessionID, replacing any previous one; nil
// removes it.
func SetSessionEnv(sessionID string, env *SessionEnv) {
	envMu.Lock()
	defer envMu.Unlock()
	if env == nil {
		delete(envs, sessionID)
		return
	}
	envs[sessionID] = env
}

// EnvFor returns the environment of the session a tool call belongs to, or
// nil when the call carries no session or its session has none.
func EnvFor(ctx context.Context) *SessionEnv {
	exec, ok := ExecFrom(ctx)
	if !ok || exec.SessionID == "" {
		return nil
	}
	envMu.RLock()
	defer envMu.RUnlock()
	return envs[exec.SessionID]
}

type execKey struct{}

// WithExec attaches a tool call's invocation context to ctx. The registry
// does this for every call, so a tool that only implements Execute can still
// learn which session it runs for.
func WithExec(ctx context.Context, exec ExecContext) context.Context {
	return context.WithValue(ctx, execKey{}, exec)
}

// ExecFrom returns the invocation context attached by WithExec.
func ExecFrom(ctx context.Context) (ExecContext, bool) {
	exec, ok := ctx.Value(execKey{}).(ExecContext)
	return exec, ok
}
