// Package agent ports packages/core/src/agent.ts: the agent registry with
// default selection semantics.
package agent

import (
	"sort"
	"sync"

	"github.com/langazov/gocode-go/internal/permission"
)

const DefaultID = "build"

type ModelRef struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Variant    string `json:"variant,omitempty"`
}

type Info struct {
	ID          string             `json:"id"`
	Model       *ModelRef          `json:"model,omitempty"`
	System      string             `json:"system,omitempty"`
	Description string             `json:"description,omitempty"`
	Mode        string             `json:"mode"`
	Hidden      bool               `json:"hidden"`
	Color       string             `json:"color,omitempty"`
	Steps       int                `json:"steps,omitempty"`
	Permissions permission.Ruleset `json:"permissions"`
}

func Empty(id string) Info {
	return Info{ID: id, Mode: "all", Permissions: permission.Ruleset{}}
}

type Selection struct {
	ID   string
	Info *Info
}

type Registry struct {
	mu        sync.RWMutex
	agents    map[string]Info
	defaultID string
}

func NewRegistry() *Registry {
	return &Registry{agents: map[string]Info{}}
}

func (r *Registry) Update(info Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[info.ID] = info
}

func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agents, id)
}

func (r *Registry) SetDefault(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultID = id
}

func (r *Registry) Get(id string) (Info, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.agents[id]
	return info, ok
}

func (r *Registry) All() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.agents))
	for _, info := range r.agents {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Default returns the configured default agent, falling back to "build" and
// then the first selectable agent, matching AgentV2.default.
func (r *Registry) Default() (Info, bool) {
	selected := r.selectedDefault()
	if selected == nil {
		return Info{}, false
	}
	return *selected, true
}

// Resolve returns the agent for an id, or the default when id is empty.
func (r *Registry) Resolve(id string) (Info, bool) {
	if id != "" {
		return r.Get(id)
	}
	return r.Default()
}

// Select mirrors AgentV2.select: an explicit id always returns that id (info
// may be missing); otherwise the default selection or "build".
func (r *Registry) Select(id string) Selection {
	if id != "" {
		if info, ok := r.Get(id); ok {
			return Selection{ID: id, Info: &info}
		}
		return Selection{ID: id}
	}
	if info := r.selectedDefault(); info != nil {
		return Selection{ID: info.ID, Info: info}
	}
	return Selection{ID: DefaultID}
}

func (r *Registry) selectedDefault() *Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.defaultID != "" {
		if agent := selectable(r.agents[r.defaultID]); agent != nil {
			return agent
		}
	}
	if build := selectable(r.agents[DefaultID]); build != nil {
		return build
	}
	for _, id := range sortedIDs(r.agents) {
		if fallback := selectable(r.agents[id]); fallback != nil {
			return fallback
		}
	}
	return nil
}

func selectable(info Info) *Info {
	if info.ID == "" || info.Mode == "subagent" || info.Hidden {
		return nil
	}
	return &info
}

func sortedIDs(agents map[string]Info) []string {
	ids := make([]string, 0, len(agents))
	for id := range agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
