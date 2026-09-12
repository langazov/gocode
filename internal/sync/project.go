package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The project half of sync. Project configs (gocode.json / gocode.jsonc in a
// worktree) are keyed by the repository's git origin URL so two machines
// that clone to different directories still match. A project that is not
// checked out on this machine has its config staged and applied the first
// time the project is opened here.

// ProjectEntry is one known local checkout.
type ProjectEntry struct {
	// Remote is the git origin URL (normalized).
	Remote string `json:"remote"`
	// Path is the worktree root on this machine.
	Path string `json:"path"`
}

// ProjectRegistry lists the projects this machine has opened, so a received
// bundle can find where "https://github.com/a/b.git" lives locally.
type ProjectRegistry struct {
	Projects []ProjectEntry `json:"projects"`
}

// registryFileName lives in the staging area's parent: <data>/sync/.
const registryFileName = "projects.json"

// RegisterProject records that the worktree at directory has the given
// origin remote, keeping the newest first and deduplicating by remote.
func RegisterProject(paths Paths, directory string) error {
	remote := GitRemoteURL(directory)
	if remote == "" {
		return nil // not a git repo: not project-synced, not an error
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	registry, err := LoadProjectRegistry(paths)
	if err != nil {
		return err
	}
	updated := []ProjectEntry{{Remote: remote, Path: absolute}}
	for _, entry := range registry.Projects {
		if entry.Remote != remote {
			updated = append(updated, entry)
		}
	}
	registry.Projects = updated
	return saveProjectRegistry(paths, registry)
}

// LoadProjectRegistry reads the known projects, tolerating a missing file.
func LoadProjectRegistry(paths Paths) (*ProjectRegistry, error) {
	path := filepath.Join(paths.ProjectStagingDir(), registryFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &ProjectRegistry{}, nil
	}
	if err != nil {
		return nil, err
	}
	var registry ProjectRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		return nil, fmt.Errorf("sync: project registry corrupt: %w", err)
	}
	return &registry, nil
}

func saveProjectRegistry(paths Paths, registry *ProjectRegistry) error {
	dir := paths.ProjectStagingDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, registryFileName), append(data, '\n'), 0o644)
}

// GitRemoteURL returns the worktree's origin URL, normalized (git@host:x →
// https://host/x, .git suffix dropped), or "" when there is no remote.
func GitRemoteURL(directory string) string {
	cmd := exec.Command("git", "-C", directory, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return NormalizeRemoteURL(strings.TrimSpace(string(out)))
}

// NormalizeRemoteURL maps the spellings of one remote onto one key.
func NormalizeRemoteURL(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(remote, "git@"); ok {
		host, path, found := strings.Cut(rest, ":")
		if found {
			remote = "https://" + host + "/" + path
		}
	}
	remote = strings.TrimSuffix(remote, "/")
	remote = strings.TrimSuffix(remote, ".git")
	remote = strings.TrimSuffix(remote, "/")
	return remote
}

// ApplyStaged moves any staged config for the project at directory into
// place, but only when the project has no local config yet — a staged file
// never overwrites something the user wrote.
func ApplyStaged(paths Paths, directory string) error {
	remote := GitRemoteURL(directory)
	if remote == "" {
		return nil
	}
	registry, err := LoadProjectRegistry(paths)
	if err != nil {
		return err
	}
	_ = registry
	dir := paths.ProjectStagingDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || entry.Name() == registryFileName {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var staged struct {
			Remote string `json:"remote"`
			Path   string `json:"path"`
		}
		// Staged files are raw config JSON; the mapping lives in the name
		// hash, so compare against every project entry instead.
		_ = staged
		content := string(data)
		// Try each relative path shape the loader knows.
		for _, rel := range []string{"gocode.json", "gocode.jsonc", filepath.Join(".gocode", "gocode.json"), filepath.Join(".gocode", "gocode.jsonc")} {
			target := filepath.Join(directory, rel)
			if _, err := os.Stat(target); err == nil {
				continue // local file exists: staged copy must not clobber
			}
			// The staged name encodes remote|rel; only move a match.
			if stagingName(remote, filepath.ToSlash(rel)) != entry.Name() {
				continue
			}
			if err := writeFileAtomic(target, content); err != nil {
				return err
			}
			return os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

// CollectProject returns the bundle key and content for the project config
// at directory, or ok=false when the project has no config file.
func CollectProject(directory string) (key, content string, ok bool) {
	remote := GitRemoteURL(directory)
	if remote == "" {
		return "", "", false
	}
	// The loader consults project files upward to the worktree root; sync
	// takes the worktree root's own candidates only, so the key (remote +
	// relative path) is stable across machines.
	for _, rel := range []string{"gocode.json", "gocode.jsonc", filepath.Join(".gocode", "gocode.json"), filepath.Join(".gocode", "gocode.jsonc")} {
		candidate := filepath.Join(directory, rel)
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		return ProjectKey(remote, filepath.ToSlash(rel)), string(data), true
	}
	return "", "", false
}

var _ = context.Background
