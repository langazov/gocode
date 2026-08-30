// Package tool defines the core-owned tool registry used by the session
// runner to advertise and settle local tool calls.
package tool

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// ExecContext carries the session invocation context for a single tool call,
// mirroring the TypeScript tool execution context.
type ExecContext struct {
	SessionID          string
	Agent              string
	AssistantMessageID string
	CallID             string
}

// Tool is a local executable tool.
type Tool interface {
	Name() string
	Description() string
	InputSchema() map[string]any
	Execute(ctx context.Context, input map[string]any) (string, error)
}

// SessionAware is implemented by tools that need the session invocation
// context (for example todowrite). The registry routes such tools through
// ExecuteWithContext.
type SessionAware interface {
	ExecuteWithContext(ctx context.Context, input map[string]any, exec ExecContext) (string, error)
}

// Registry holds the set of tools available to a session run.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}}
}

func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[tool.Name()] = tool
}

func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// Names returns the sorted tool names for deterministic advertisement.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Execute runs a registered tool by name, returning its textual output.
// Session-aware tools receive the invocation context.
func (r *Registry) Execute(ctx context.Context, name string, input map[string]any, exec ExecContext) (string, error) {
	tool, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("tool: unknown tool %q", name)
	}
	if aware, ok := tool.(SessionAware); ok {
		return aware.ExecuteWithContext(ctx, input, exec)
	}
	return tool.Execute(ctx, input)
}
