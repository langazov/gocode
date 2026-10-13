package modelsdev

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/flock"
)

// Saved limits are separate from the downloaded catalog so refreshes cannot
// discard provider-reported context windows. Only positive limits are recorded;
// missing wire fields must not erase known metadata.
type savedContextLimits map[string]map[string]Model

func (s *Service) contextLimitsPath() string { return s.Filepath + ".context-limits.json" }

func (s *Service) loadContextLimits() savedContextLimits {
	out := savedContextLimits{}
	if s.Filepath == "" {
		return out
	}
	data, err := os.ReadFile(s.contextLimitsPath())
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	if out == nil {
		out = savedContextLimits{}
	}
	return out
}

func applyContextLimits(catalog Catalog, saved savedContextLimits) Catalog {
	// Catalog maps may already be in use by readers. Never mutate them in place.
	out := make(Catalog, len(catalog))
	for id, entry := range catalog {
		out[id] = entry
	}
	for providerID, models := range saved {
		entry, ok := out[providerID]
		if !ok {
			continue
		}
		merged := make(map[string]Model, len(entry.Models)+len(models))
		for id, model := range entry.Models {
			merged[id] = model
		}
		for id, savedModel := range models {
			if savedModel.Limit.Context <= 0 {
				continue
			}
			model, exists := merged[id]
			if !exists {
				model = savedModel
			}
			model.Limit.Context = savedModel.Limit.Context
			merged[id] = model
		}
		entry.Models = merged
		out[providerID] = entry
	}
	return out
}

func (s *Service) withSavedContextLimits(catalog Catalog) Catalog {
	return applyContextLimits(catalog, s.loadContextLimits())
}

// SaveContextLimits updates both the shared in-memory catalog and its local
// overlay. New live models are retained too, allowing runtime budget lookup.
func (s *Service) SaveContextLimits(ctx context.Context, providerID string, models map[string]Model) error {
	if _, err := s.Get(ctx); err != nil {
		return err
	}
	entry := <-s.cache
	defer func() { s.cache <- entry }()
	saved := savedContextLimits{}
	if s.Filepath != "" {
		if err := os.MkdirAll(filepath.Dir(s.Filepath), 0o755); err != nil {
			return err
		}
		lock, err := flock.Lock(s.contextLimitsPath() + ".lock")
		if err != nil {
			return err
		}
		defer lock.Close()
		saved = s.loadContextLimits()
	}
	if saved[providerID] == nil {
		saved[providerID] = map[string]Model{}
	}
	changed := false
	for id, model := range models {
		if model.Limit.Context > 0 && saved[providerID][id].Limit.Context != model.Limit.Context {
			saved[providerID][id] = model
			changed = true
		}
	}
	if changed && s.Filepath != "" {
		data, err := json.Marshal(saved)
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(s.Filepath), ".context-limits-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		_, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(file.Name(), s.contextLimitsPath()); err != nil {
			return err
		}
	}
	entry.catalog = applyContextLimits(entry.catalog, saved)
	return nil
}
