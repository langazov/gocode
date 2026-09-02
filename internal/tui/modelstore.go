package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/anomalyco/opencode-go/internal/global"
)

// modelRef identifies a model, matching the shape TS stores.
type modelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

func (m modelRef) label() string { return m.ProviderID + "/" + m.ModelID }

func (m modelRef) same(other modelRef) bool {
	return m.ProviderID == other.ProviderID && m.ModelID == other.ModelID
}

// recentLimit caps the recent list. TS keeps it unbounded in the store and
// slices at the display site; capping on write keeps the shared file from
// growing without bound while leaving far more than the dialog shows.
const recentLimit = 50

// modelStore ports the model half of packages/tui/src/context/local.tsx: the
// recent and favorite lists behind the model dialog's sections.
//
// It reads and writes <state>/model.json, the same path and format the
// TypeScript client uses, so the two binaries share one list — the precedent
// set by prompthistory.go for prompt-history.jsonl. The `variant` key is
// preserved verbatim on write even though this port does not read it, so
// round-tripping through the Go binary does not discard the TS client's
// variant selections.
type modelStore struct {
	mu       sync.Mutex
	path     string
	Recent   []modelRef `json:"recent"`
	Favorite []modelRef `json:"favorite"`
	variant  json.RawMessage
	loaded   bool
}

func newModelStore() *modelStore {
	return &modelStore{path: filepath.Join(global.Resolve().State, "model.json")}
}

func (s *modelStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return
	}
	s.loaded = true
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var parsed struct {
		Recent   []modelRef      `json:"recent"`
		Favorite []modelRef      `json:"favorite"`
		Variant  json.RawMessage `json:"variant"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return
	}
	s.Recent, s.Favorite, s.variant = parsed.Recent, parsed.Favorite, parsed.Variant
}

func (s *modelStore) save() {
	s.mu.Lock()
	payload := map[string]any{
		"recent":   orEmpty(s.Recent),
		"favorite": orEmpty(s.Favorite),
	}
	if len(s.variant) > 0 {
		payload["variant"] = s.variant
	} else {
		payload["variant"] = map[string]any{}
	}
	path := s.path
	s.mu.Unlock()

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	// Write atomically: the TS client reads this file concurrently, and a
	// half-written document there is a parse failure that silently drops the
	// user's favorites.
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(temp, path); err != nil {
		os.Remove(temp)
	}
}

func orEmpty(refs []modelRef) []modelRef {
	if refs == nil {
		return []modelRef{}
	}
	return refs
}

// recents returns the recent list, most recent first.
func (s *modelStore) recents() []modelRef {
	s.load()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]modelRef(nil), s.Recent...)
}

func (s *modelStore) favorites() []modelRef {
	s.load()
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]modelRef(nil), s.Favorite...)
}

func (s *modelStore) isFavorite(ref modelRef) bool {
	for _, item := range s.favorites() {
		if item.same(ref) {
			return true
		}
	}
	return false
}

// markRecent moves a model to the head of the recent list, porting
// recentModels() in local.tsx.
func (s *modelStore) markRecent(ref modelRef) {
	s.load()
	s.mu.Lock()
	out := []modelRef{ref}
	for _, item := range s.Recent {
		if item.same(ref) {
			continue
		}
		out = append(out, item)
		if len(out) >= recentLimit {
			break
		}
	}
	s.Recent = out
	s.mu.Unlock()
	s.save()
}

// toggleFavorite adds or removes a favorite, returning the new state. New
// favorites go to the head of the list, matching `[model, ...favorite]`.
func (s *modelStore) toggleFavorite(ref modelRef) bool {
	s.load()
	s.mu.Lock()
	var out []modelRef
	found := false
	for _, item := range s.Favorite {
		if item.same(ref) {
			found = true
			continue
		}
		out = append(out, item)
	}
	if !found {
		out = append([]modelRef{ref}, out...)
	}
	s.Favorite = out
	s.mu.Unlock()
	s.save()
	return !found
}
