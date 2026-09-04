package plugin

import (
	"context"
	"fmt"

	"github.com/langazov/gocode-go/internal/tool"
)

// ToolContext is what a plugin tool is told about the call it is serving,
// porting `ToolContext` in packages/plugin/src/tool.ts.
//
// TypeScript also hands over an `abort` signal and `metadata`/`ask` callbacks.
// Cancellation arrives through the Go context instead; metadata comes back on
// [ToolResult] rather than through a callback, because a callback cannot cross
// the process boundary a process plugin sits behind.
type ToolContext struct {
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID,omitempty"`
	Agent     string `json:"agent,omitempty"`
	CallID    string `json:"callID,omitempty"`
	// Directory is the session's working directory. A tool resolving a
	// relative path must use this, not the process's cwd.
	Directory string `json:"directory"`
	// Worktree is the project root, for stable relative paths.
	Worktree string `json:"worktree"`
}

// ToolResult is what a plugin tool returns, porting the object form of
// `ToolResult`.
type ToolResult struct {
	Title    string         `json:"title,omitempty"`
	Output   string         `json:"output"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Tool is a tool contributed by a plugin, porting an entry of the `tool`
// record and the `tool()` helper that builds it.
//
// Parameters is a JSON Schema object. TypeScript builds it from a zod shape;
// there is no equivalent inference in Go, so the schema is given directly —
// the same thing every built-in tool in internal/tool/builtins does.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Execute     func(ctx context.Context, args map[string]any, tc ToolContext) (ToolResult, error)
}

// Registrar is the subset of *tool.Registry the bridge needs, so a caller can
// pass a test double.
type Registrar interface {
	Register(tool.Tool)
}

// RegisterTools installs every plugin-contributed tool into the runtime's tool
// registry. It is the seam that makes a plugin tool indistinguishable from a
// built-in one at the point of use: the session runner advertises and executes
// it through the same [tool.Tool] interface.
//
// directory and worktree fill the parts of [ToolContext] the registry does not
// carry, since [tool.ExecContext] is per-call and these are per-host.
func RegisterTools(registry Registrar, host *Host, directory, worktree string) {
	if registry == nil || host == nil {
		return
	}
	for _, t := range host.Tools() {
		registry.Register(&bridgedTool{tool: t, directory: directory, worktree: worktree})
	}
}

// bridgedTool adapts a plugin [Tool] to the registry's [tool.Tool].
type bridgedTool struct {
	tool      Tool
	directory string
	worktree  string
}

func (b *bridgedTool) Name() string        { return b.tool.Name }
func (b *bridgedTool) Description() string { return b.tool.Description }

func (b *bridgedTool) InputSchema() map[string]any {
	if b.tool.Parameters != nil {
		return b.tool.Parameters
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// Execute serves a call made without session context. The registry only takes
// this path for a tool that does not implement [tool.SessionAware], which a
// bridged tool always does, so it exists to satisfy the interface.
func (b *bridgedTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	return b.ExecuteWithContext(ctx, input, tool.ExecContext{})
}

// ExecuteWithContext runs the plugin's tool with the call's session context.
//
// The registry's contract is a plain string, so the title and metadata a
// plugin returns are dropped here rather than being invented into the output.
// They stay on [ToolResult] for the tool.execute.after hook, which is where
// TypeScript surfaces them too.
func (b *bridgedTool) ExecuteWithContext(ctx context.Context, input map[string]any, exec tool.ExecContext) (string, error) {
	if b.tool.Execute == nil {
		return "", fmt.Errorf("plugin: tool %q has no executor", b.tool.Name)
	}
	result, err := b.tool.Execute(ctx, input, ToolContext{
		SessionID: exec.SessionID,
		MessageID: exec.AssistantMessageID,
		Agent:     exec.Agent,
		CallID:    exec.CallID,
		Directory: b.directory,
		Worktree:  b.worktree,
	})
	if err != nil {
		return "", err
	}
	return result.Output, nil
}
