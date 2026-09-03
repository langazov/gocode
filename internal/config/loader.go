package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/configpaths"
	"github.com/langazov/gocode-go/internal/global"
)

// Source records one config file consulted during Load, in merge order
// (later sources override earlier ones).
type Source struct {
	Path  string `json:"path"`
	Kind  string `json:"kind"` // global | env-file | project | project-dir | content
	Found bool   `json:"found"`
	Error string `json:"error,omitempty"`
}

// LoadTraced merges every config source in the TypeScript order and returns
// the result together with the trace of consulted files:
//
//  1. global config.json -> gocode.json -> gocode.jsonc (errors tolerated)
//  2. GOCODE_CONFIG file
//  3. project gocode.json(c) discovered upward to the worktree root
//  4. .gocode dirs and GOCODE_CONFIG_DIR: gocode.json, gocode.jsonc
//  5. GOCODE_CONFIG_CONTENT inline override
func LoadTraced() (*Config, []Source, error) {
	merged := map[string]any{}
	var sources []Source

	mergeFile := func(path, kind string, tolerate bool) {
		source := Source{Path: path, Kind: kind}
		defer func() { sources = append(sources, source) }()
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return
			}
			source.Error = err.Error()
			if !tolerate {
				merged["_error"] = err
			}
			return
		}
		source.Found = true
		if err := mergeText(merged, string(data), filepath.Dir(path)); err != nil {
			source.Error = err.Error()
			if !tolerate {
				merged["_error"] = err
			}
		}
	}
	mergeContent := func(text, kind string) {
		sources = append(sources, Source{Path: "GOCODE_CONFIG_CONTENT", Kind: kind, Found: true})
		if err := mergeText(merged, text, ""); err != nil {
			sources[len(sources)-1].Error = err.Error()
		}
	}

	for _, name := range []string{"config.json", "gocode.json", "gocode.jsonc"} {
		mergeFile(filepath.Join(global.Resolve().Config, name), "global", true)
	}

	if path := os.Getenv("GOCODE_CONFIG"); path != "" {
		mergeFile(path, "env-file", false)
	}

	if !disableProjectConfig() {
		directory, _ := os.Getwd()
		worktree := configpaths.Worktree(directory)
		for _, file := range configpaths.Files("gocode", directory, worktree) {
			mergeFile(file, "project", false)
		}
		dirs := configpaths.Directories(directory, worktree)
		for _, dir := range dirs {
			if filepath.Base(dir) != ".gocode" && dir != os.Getenv("GOCODE_CONFIG_DIR") {
				continue
			}
			for _, name := range []string{"gocode.json", "gocode.jsonc"} {
				mergeFile(filepath.Join(dir, name), "project-dir", false)
			}
		}
	}

	if content := os.Getenv("GOCODE_CONFIG_CONTENT"); content != "" {
		mergeContent(content, "content")
	}

	if errText, ok := merged["_error"].(error); ok {
		return nil, sources, errText
	}
	delete(merged, "_error")

	var config Config
	encoded, err := json.Marshal(merged)
	if err != nil {
		return nil, sources, err
	}
	if err := json.Unmarshal(encoded, &config); err != nil {
		return nil, sources, fmt.Errorf("config: %w", err)
	}
	return &config, sources, nil
}

// Load merges every config source, failing on non-global errors.
func Load() (*Config, error) {
	config, _, err := LoadTraced()
	return config, err
}

func mergeText(merged map[string]any, text string, configDir string) error {
	expanded, err := substitute(text, configDir)
	if err != nil {
		return err
	}
	stripped, err := StripJSONC(expanded)
	if err != nil {
		return err
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stripped), &parsed); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if parsed == nil {
		return nil
	}
	deepMerge(merged, parsed)
	return nil
}

func disableProjectConfig() bool {
	value := os.Getenv("GOCODE_DISABLE_PROJECT_CONFIG")
	return value == "true" || value == "1"
}
