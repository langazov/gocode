// Package configedit makes the narrow, surgical edits to the global config
// that an installer needs: enabling a plugin, or registering a language
// server.
//
// It exists because installing something does not enable it. A plugin runs
// only when the config's `plugin` array names it, and a language server whose
// binary is not on PATH is reachable only through the `lsp` section — so a
// package manager that drops files on disk and stops has done half a job. The
// logic lived in tools/pluginconfig.go, a `go run` script, which is
// unreachable from a Homebrew formula on a machine with no Go toolchain; this
// package is the same behaviour linked into the binary, where `gocode plugin
// enable` and `gocode lsp enable` can reach it.
//
// Every edit is deliberately conservative:
//
//   - unrelated keys are preserved, because this file is the user's, not ours;
//   - repeating an edit is a no-op rather than a rewrite, so a reinstall does
//     not churn the file or its mtime;
//   - a file containing comments or trailing commas is refused rather than
//     reformatted, since a rewrite would silently delete what the user wrote;
//   - the write is atomic, so an interrupted install cannot truncate the
//     global config.
package configedit

import (
	"encoding/json"
	"errors"
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
// none do, DefaultName is created, since that is the name the loader and the
// docs both treat as canonical.
var candidates = []string{"config.json", "gocode.json", "gocode.jsonc"}

// DefaultName is the file created when no global config exists yet.
const DefaultName = "gocode.json"

// Result reports what an edit did, so callers can print an accurate line
// rather than claiming a change that did not happen.
type Result struct {
	// Path is the config file that was inspected, whether or not it changed.
	Path string
	// Changed is false when the config already said what was asked.
	Changed bool
	// Summary is a one-line description of the outcome, without the path.
	Summary string
}

// CommentedError is returned for a config carrying comments or trailing
// commas. It is a distinct type so a caller can tell "I refuse to touch this"
// apart from "this file is broken", and print the manual instructions.
type CommentedError struct {
	Path string
	// Manual is the JSON the user should merge in by hand.
	Manual string
}

func (e *CommentedError) Error() string {
	return fmt.Sprintf("%s contains comments or trailing commas; rewriting it would delete them.\nAdd this by hand instead:\n%s", e.Path, e.Manual)
}

// EnablePlugin adds ref to the `plugin` array, replacing an existing entry
// only when its options differ.
func EnablePlugin(ref string, options map[string]any) (Result, error) {
	return edit(manualPlugin(ref, options), func(root map[string]json.RawMessage) (bool, string, error) {
		specs, err := readPlugins(root)
		if err != nil {
			return false, "", err
		}

		entry := config.PluginSpec{Ref: ref, Options: options}
		var next []config.PluginSpec
		replaced := false
		for _, spec := range specs {
			if spec.Ref != ref {
				next = append(next, spec)
				continue
			}
			// Already listed: replace it only when the options differ, so a
			// repeated install is a no-op rather than a spurious rewrite.
			if sameOptions(spec.Options, options) {
				return false, fmt.Sprintf("%q already enabled", ref), nil
			}
			replaced = true
			next = append(next, entry)
		}
		if !replaced {
			next = append(next, entry)
		}

		if err := writePlugins(root, next); err != nil {
			return false, "", err
		}
		if replaced {
			return true, fmt.Sprintf("updated options for %q", ref), nil
		}
		return true, fmt.Sprintf("enabled %q", ref), nil
	})
}

// DisablePlugin removes ref from the `plugin` array, leaving the installed
// files alone.
func DisablePlugin(ref string) (Result, error) {
	return edit("", func(root map[string]json.RawMessage) (bool, string, error) {
		specs, err := readPlugins(root)
		if err != nil {
			return false, "", err
		}

		var next []config.PluginSpec
		removed := false
		for _, spec := range specs {
			if spec.Ref == ref {
				removed = true
				continue
			}
			next = append(next, spec)
		}
		if !removed {
			return false, fmt.Sprintf("%q was not enabled", ref), nil
		}
		if err := writePlugins(root, next); err != nil {
			return false, "", err
		}
		return true, fmt.Sprintf("disabled %q", ref), nil
	})
}

// EnableLSP registers a language server under id in the `lsp` section.
//
// A server already on PATH and named in the built-in registry needs no config
// at all; this is for the other cases — a binary installed somewhere PATH does
// not reach, or a server the registry does not know.
func EnableLSP(id string, server config.LSPServer) (Result, error) {
	return edit(manualLSP(id, server), func(root map[string]json.RawMessage) (bool, string, error) {
		servers, err := readLSP(root, id)
		if err != nil {
			return false, "", err
		}

		if existing, ok := servers[id]; ok && sameLSPServer(existing, server) {
			return false, fmt.Sprintf("%q already configured", id), nil
		}
		_, replaced := servers[id]
		servers[id] = server

		encoded, err := json.Marshal(servers)
		if err != nil {
			return false, "", err
		}
		root["lsp"] = encoded
		if replaced {
			return true, fmt.Sprintf("updated language server %q", id), nil
		}
		return true, fmt.Sprintf("registered language server %q", id), nil
	})
}

// DisableLSP removes a server from the `lsp` section entirely. It is not the
// same as `"disabled": true`, which keeps the entry and suppresses a built-in.
func DisableLSP(id string) (Result, error) {
	return edit("", func(root map[string]json.RawMessage) (bool, string, error) {
		servers, err := readLSP(root, id)
		if err != nil {
			return false, "", err
		}
		if _, ok := servers[id]; !ok {
			return false, fmt.Sprintf("%q was not configured", id), nil
		}
		delete(servers, id)

		if len(servers) == 0 {
			// An empty object would read as "configured, with nothing in it".
			// Dropping the key restores the default, which is what removing
			// the last entry means.
			delete(root, "lsp")
		} else {
			encoded, err := json.Marshal(servers)
			if err != nil {
				return false, "", err
			}
			root["lsp"] = encoded
		}
		return true, fmt.Sprintf("removed language server %q", id), nil
	})
}

// edit applies mutate to the decoded global config and writes it back when
// mutate reports a change. manual is the hand-editable JSON quoted in the
// error for a commented file; it is empty for removals, which need no
// instructions beyond "delete the entry".
func edit(manual string, mutate func(map[string]json.RawMessage) (bool, string, error)) (Result, error) {
	path, raw, err := load()
	if err != nil {
		return Result{}, err
	}

	// A rewrite would drop comments and trailing commas, so a JSONC file the
	// user has commented is left alone and they are told what to add. Silently
	// deleting someone's comments is worse than doing nothing.
	stripped, err := config.StripJSONC(string(raw))
	if err != nil {
		return Result{Path: path}, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(stripped) != strings.TrimSpace(string(raw)) {
		if manual == "" {
			manual = "(remove the entry by hand)"
		}
		return Result{Path: path}, &CommentedError{Path: path, Manual: manual}
	}

	root := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(stripped); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
			return Result{Path: path}, fmt.Errorf("%s: %w", path, err)
		}
	}

	changed, summary, err := mutate(root)
	if err != nil {
		return Result{Path: path}, fmt.Errorf("%s: %w", path, err)
	}
	if !changed {
		return Result{Path: path, Changed: false, Summary: summary}, nil
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return Result{Path: path}, err
	}
	if err := write(path, append(out, '\n')); err != nil {
		return Result{Path: path}, err
	}
	return Result{Path: path, Changed: true, Summary: summary}, nil
}

func readPlugins(root map[string]json.RawMessage) ([]config.PluginSpec, error) {
	existing, ok := root["plugin"]
	if !ok {
		return nil, nil
	}
	var specs []config.PluginSpec
	if err := json.Unmarshal(existing, &specs); err != nil {
		return nil, fmt.Errorf(`"plugin" is not a valid plugin array: %w`, err)
	}
	return specs, nil
}

func writePlugins(root map[string]json.RawMessage, specs []config.PluginSpec) error {
	if len(specs) == 0 {
		delete(root, "plugin")
		return nil
	}
	encoded, err := json.Marshal(specs)
	if err != nil {
		return err
	}
	root["plugin"] = encoded
	return nil
}

// readLSP decodes the `lsp` section into an editable map. The section is a
// union — `false` switches every server off — and editing one server of a
// config that says `false` would quietly re-enable the rest, so that case is
// refused rather than guessed at.
func readLSP(root map[string]json.RawMessage, id string) (map[string]config.LSPServer, error) {
	existing, ok := root["lsp"]
	if !ok || strings.TrimSpace(string(existing)) == "null" {
		return map[string]config.LSPServer{}, nil
	}

	var flag bool
	if err := json.Unmarshal(existing, &flag); err == nil {
		if !flag {
			return nil, fmt.Errorf(`"lsp" is false, which disables every language server; remove it before configuring %q`, id)
		}
		// `true` means "the defaults", the same as absent.
		return map[string]config.LSPServer{}, nil
	}

	var servers map[string]config.LSPServer
	if err := json.Unmarshal(existing, &servers); err != nil {
		return nil, fmt.Errorf(`"lsp" is not a valid server map: %w`, err)
	}
	if servers == nil {
		servers = map[string]config.LSPServer{}
	}
	return servers, nil
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
	return filepath.Join(dir, DefaultName), []byte("{}"), nil
}

// write replaces the file atomically, so an interrupted run cannot leave the
// user with a truncated global config.
func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".configedit-*")
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

// ParseOptions decodes a JSON object of plugin options, rejecting anything
// that is not an object so a typo does not become a silent empty config.
func ParseOptions(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("options must be a JSON object: %w", err)
	}
	return parsed, nil
}

func sameOptions(a, b map[string]any) bool {
	return sameJSON(a, b)
}

func sameLSPServer(a, b config.LSPServer) bool {
	return sameJSON(a, b)
}

// sameJSON compares two values by their encoding. Marshalling sorts map keys,
// so this is order-insensitive where it should be and exact everywhere else.
func sameJSON(a, b any) bool {
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

// manualPlugin renders the snippet quoted at a user whose config we refuse to
// rewrite.
func manualPlugin(ref string, options map[string]any) string {
	entry := config.PluginSpec{Ref: ref, Options: options}
	encoded, err := json.Marshal([]config.PluginSpec{entry})
	if err != nil {
		return fmt.Sprintf(`  "plugin": [%q]`, ref)
	}
	return fmt.Sprintf(`  "plugin": %s`, encoded)
}

func manualLSP(id string, server config.LSPServer) string {
	encoded, err := json.Marshal(map[string]config.LSPServer{id: server})
	if err != nil {
		return fmt.Sprintf(`  "lsp": { %q: { ... } }`, id)
	}
	return fmt.Sprintf(`  "lsp": %s`, encoded)
}
