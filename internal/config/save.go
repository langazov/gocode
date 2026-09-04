package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/global"
)

// Saving the `plugin` array of the global config file.
//
// This is the write half of the loader: the same file candidates, in merge
// order, with the same conservatism — a file containing comments or trailing
// commas is refused rather than silently stripped of them, and the rewrite is
// atomic so an interrupted save cannot truncate the user's config.
//
// tools/pluginconfig.go and the interface's plugins dialog both save through
// here, so "enable" means the same thing from a terminal and from the TUI.

// candidates are the global config filenames, in the order LoadTraced merges
// them. The first that exists is edited; if none do, the middle one is
// created, since that is the name the loader and the docs treat as canonical.
var candidates = []string{"config.json", "gocode.json", "gocode.jsonc"}

const configDefaultName = "gocode.json"

// SavePlugins rewrites the global config's `plugin` array to exactly specs,
// preserving every other key. A nil/empty slice removes the key entirely,
// which is what keeps a disabled-everything save from writing `"plugin": []`.
func SavePlugins(specs []PluginSpec) error {
	path, raw, err := pluginConfigFile()
	if err != nil {
		return err
	}
	root, err := parsePluginConfigFile(path, raw)
	if err != nil {
		return err
	}

	if len(specs) == 0 {
		delete(root, "plugin")
	} else {
		encoded, err := json.Marshal(specs)
		if err != nil {
			return err
		}
		root["plugin"] = encoded
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return writeConfigFile(path, append(out, '\n'))
}

// pluginConfigFile returns the global config file to edit and its contents,
// creating nothing on disk yet.
func pluginConfigFile() (string, []byte, error) {
	dir := global.Resolve().Config
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err == nil {
			return path, raw, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", nil, err
		}
	}
	return filepath.Join(dir, configDefaultName), []byte("{}"), nil
}

// parsePluginConfigFile decodes the config into a raw key map, refusing a
// JSONC file whose comments a rewrite would destroy.
func parsePluginConfigFile(path string, raw []byte) (map[string]json.RawMessage, error) {
	stripped, err := StripJSONC(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(stripped) != strings.TrimSpace(string(raw)) {
		return nil, fmt.Errorf("%s contains comments or trailing commas; edit its \"plugin\" array by hand", path)
	}
	root := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(stripped); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return root, nil
}

// writeConfigFile replaces the file atomically, so an interrupted write
// cannot leave a truncated config behind.
func writeConfigFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, path)
}
