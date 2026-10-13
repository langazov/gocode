package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/flock"
	"github.com/langazov/gocode-go/internal/global"
)

// UsageEntry is the persisted context-cost measurement for one configured
// server: how many tools it exposed the last time it was reachable, and the
// estimated tokens those tool definitions consume. Entries live in
// data/mcp-usage.json keyed by server name — a sidecar like mcp-auth.json,
// deliberately not the user's config file: a measurement is derived state,
// not configuration, and mcpsave.go rightly refuses to rewrite a config it
// cannot preserve (comments, trailing commas).
//
// It is what lets a server that boots disabled still show what enabling it
// would cost back: the number the last live connection measured.
type UsageEntry struct {
	// Type/URL/Command fingerprint the endpoint the measurement was taken
	// against; a server whose definition changed gets a fresh measurement,
	// not a stale number (the same identity rule StoreGet applies to
	// credentials).
	Type    string   `json:"type,omitempty"`
	URL     string   `json:"url,omitempty"`
	Command []string `json:"command,omitempty"`

	ToolCount int `json:"toolCount"`
	EstTokens int `json:"estTokens"`
}

// sameMeasurement reports whether two entries describe the same endpoint
// with the same figures.
func (e UsageEntry) sameMeasurement(o UsageEntry) bool {
	if e.Type != o.Type || e.URL != o.URL || e.ToolCount != o.ToolCount || e.EstTokens != o.EstTokens {
		return false
	}
	if len(e.Command) != len(o.Command) {
		return false
	}
	for i := range e.Command {
		if e.Command[i] != o.Command[i] {
			return false
		}
	}
	return true
}

// matches reports whether the entry was measured against cfg's endpoint —
// only the identity fields are compared, never the figures: the whole
// point of the lookup is to answer "is this number about this endpoint",
// and the figures are what is being looked up.
func (e UsageEntry) matches(cfg ServerConfig) bool {
	if e.Type != cfg.Type || e.URL != cfg.URL {
		return false
	}
	if len(e.Command) != len(cfg.Command) {
		return false
	}
	for i := range e.Command {
		if e.Command[i] != cfg.Command[i] {
			return false
		}
	}
	return true
}

func usagePath() string {
	return filepath.Join(global.Resolve().Data, "mcp-usage.json")
}

// usageAll reads the whole mcp-usage.json file, without locking — callers
// that mutate must hold the lock (see withUsageLock).
func usageAll() (map[string]UsageEntry, error) {
	data, err := os.ReadFile(usagePath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]UsageEntry{}, nil
		}
		return nil, err
	}
	var out map[string]UsageEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]UsageEntry{}, nil
	}
	if out == nil {
		out = map[string]UsageEntry{}
	}
	return out, nil
}

func usageWrite(data map[string]UsageEntry) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(usagePath()), 0o755); err != nil {
		return err
	}
	return os.WriteFile(usagePath(), encoded, 0o600)
}

// withUsageLock file-locks mcp-usage.json around a read-modify-write cycle,
// mirroring withStoreLock for mcp-auth.json.
func withUsageLock(fn func(map[string]UsageEntry) (map[string]UsageEntry, error)) error {
	if err := os.MkdirAll(filepath.Dir(usagePath()), 0o755); err != nil {
		return err
	}
	lock, err := flock.Lock(usagePath() + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := usageAll()
	if err != nil {
		return err
	}
	next, err := fn(data)
	if err != nil {
		return err
	}
	if next == nil {
		return nil
	}
	return usageWrite(next)
}

// UsageAll returns every persisted measurement.
func UsageAll() (map[string]UsageEntry, error) {
	var out map[string]UsageEntry
	err := withUsageLock(func(data map[string]UsageEntry) (map[string]UsageEntry, error) {
		out = data
		return nil, nil
	})
	return out, err
}

// UsageGet returns the stored measurement for name when it was taken
// against cfg's endpoint — a changed command or URL invalidates it, the
// same rule StoreGet applies to credentials.
func UsageGet(name string, cfg ServerConfig) (UsageEntry, bool) {
	all, err := UsageAll()
	if err != nil {
		return UsageEntry{}, false
	}
	entry, ok := all[name]
	if !ok || !entry.matches(cfg) {
		return UsageEntry{}, false
	}
	return entry, true
}

// UsageSet persists name's measurement.
func UsageSet(name string, entry UsageEntry) error {
	return withUsageLock(func(data map[string]UsageEntry) (map[string]UsageEntry, error) {
		data[name] = entry
		return data, nil
	})
}

// UsageRemove drops name's measurement — called when a server's config
// entry is deleted, so a re-added server with the same name measures fresh
// rather than showing the old number.
func UsageRemove(name string) error {
	return withUsageLock(func(data map[string]UsageEntry) (map[string]UsageEntry, error) {
		delete(data, name)
		return data, nil
	})
}
