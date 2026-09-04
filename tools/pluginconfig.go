//go:build ignore

// pluginconfig adds or removes a plugin entry in the global gocode config.
//
// Installing a plugin copies it to plugin.InstallRoot(); it does *not* enable
// it. A plugin runs only when the config's `plugin` array names it, matching
// upstream — an installed-but-unlisted plugin is inert. This is what
// `make install-plugin` uses to close that gap, so an install is one step
// rather than a copy plus a hand edit.
//
//	go run tools/pluginconfig.go -add plugin-echo [-options '{"banner":"hi"}']
//	go run tools/pluginconfig.go -remove plugin-echo
//
// It is deliberately conservative: it preserves every other key, is idempotent,
// and refuses to rewrite a file containing comments rather than silently
// stripping them.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/global"
)

// candidates are the global config filenames, in the order the loader merges
// them (see internal/config/loader.go). The first that exists is edited; if
// none do, the middle one is created, since that is the name the loader and
// the docs both treat as canonical.
var candidates = []string{"config.json", "gocode.json", "gocode.jsonc"}

const defaultName = "gocode.json"

func main() {
	add := flag.String("add", "", "plugin reference to enable")
	remove := flag.String("remove", "", "plugin reference to disable")
	options := flag.String("options", "", "JSON object of plugin options, for -add")
	flag.Parse()

	if (*add == "") == (*remove == "") {
		fmt.Fprintln(os.Stderr, "usage: pluginconfig -add <ref> [-options '<json>'] | -remove <ref>")
		os.Exit(2)
	}
	if err := run(*add, *remove, *options); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(add, remove, options string) error {
	path, raw, err := load()
	if err != nil {
		return err
	}

	// A rewrite would drop comments and trailing commas, so a JSONC file the
	// user has commented is left alone and they are told what to add. Silently
	// deleting someone's comments is worse than doing nothing.
	stripped, err := config.StripJSONC(string(raw))
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(stripped) != strings.TrimSpace(string(raw)) {
		return fmt.Errorf("%s contains comments or trailing commas; add %q to its \"plugin\" array by hand", path, add+remove)
	}

	root := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(stripped); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}

	var specs []config.PluginSpec
	if existing, ok := root["plugin"]; ok {
		if err := json.Unmarshal(existing, &specs); err != nil {
			return fmt.Errorf(`%s: "plugin" is not a valid plugin array: %w`, path, err)
		}
	}

	var next []config.PluginSpec
	var changed bool
	switch {
	case remove != "":
		for _, spec := range specs {
			if spec.Ref == remove {
				changed = true
				continue
			}
			next = append(next, spec)
		}
		if !changed {
			fmt.Printf("%s: %q was not enabled\n", path, remove)
			return nil
		}
	default:
		parsed, err := parseOptions(options)
		if err != nil {
			return err
		}
		entry := config.PluginSpec{Ref: add, Options: parsed}
		for _, spec := range specs {
			if spec.Ref != add {
				next = append(next, spec)
				continue
			}
			// Already listed: replace it only when the options differ, so a
			// repeated install is a no-op rather than a spurious rewrite.
			if sameOptions(spec.Options, parsed) {
				fmt.Printf("%s: %q already enabled\n", path, add)
				return nil
			}
			changed = true
			next = append(next, entry)
		}
		if !changed {
			next = append(next, entry)
			changed = true
		}
	}

	if len(next) == 0 {
		delete(root, "plugin")
	} else {
		encoded, err := json.Marshal(next)
		if err != nil {
			return err
		}
		root["plugin"] = encoded
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	if err := write(path, append(out, '\n')); err != nil {
		return err
	}

	if remove != "" {
		fmt.Printf("%s: disabled %q\n", path, remove)
		return nil
	}
	fmt.Printf("%s: enabled %q\n", path, add)
	return nil
}

// load returns the config file to edit and its current contents, creating
// nothing on disk yet.
func load() (string, []byte, error) {
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
	return filepath.Join(dir, defaultName), []byte("{}"), nil
}

// write replaces the file atomically, so an interrupted run cannot leave the
// user with a truncated global config.
func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".pluginconfig-*")
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

func parseOptions(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("-options must be a JSON object: %w", err)
	}
	return parsed, nil
}

func sameOptions(a, b map[string]any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}
