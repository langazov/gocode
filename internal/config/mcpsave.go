package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/configpaths"
	"github.com/langazov/gocode-go/internal/global"
)

// Saving entries of the `mcp` map, in whichever config file wins for that
// server — the write half of the same merge order LoadTraced reads (global
// candidates, then GOCODE_CONFIG, then project files, each layer overriding
// the last). Writing the winning file is what makes a disable stick: writing
// the global file under a project override would be silently undone by the
// project layer on the next load.
//
// The conservatism matches save.go: comments and trailing commas are refused
// rather than stripped, only the named entry is touched, and the write is
// atomic.

// MCPConfigFile reports the config file that effectively defines the `mcp`
// entry name in directory ("" when no file defines it). It mirrors
// LoadTracedFor's walk in merge order: layers merge with later files
// overriding earlier ones, so the winning definition is the LAST candidate
// that carries the name — walking must not stop at the first hit, or a
// project override would be edited into the global file it overrides.
func MCPConfigFile(directory, name string) (string, bool, error) {
	candidates, err := mcpCandidateFiles(directory)
	if err != nil {
		return "", false, err
	}
	winning := ""
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return "", false, err
		}
		has, err := mcpFileDefines(raw, name)
		if err != nil {
			// A broken later file must not make an earlier definition
			// unwritable; LoadTraced tolerates global-file errors too.
			continue
		}
		if has {
			winning = path
		}
	}
	return winning, winning != "", nil
}

// SetMCPEnabled rewrites the winning file's `enabled` key for the named
// server, preserving every other field of the entry (type, command,
// environment, url, headers, timeout) untouched. Enabling normalizes the
// key away entirely — `"enabled": true` never gets written, so a
// toggle-off/toggle-on round trip restores the original file byte for byte
// where the entry started implicit.
func SetMCPEnabled(directory, name string, enabled bool) (string, error) {
	path, found, err := MCPConfigFile(directory, name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("mcp server %q is not defined in any config file", name)
	}
	edited, err := editMCPEntry(path, name, func(entry map[string]any) {
		if enabled {
			delete(entry, "enabled")
		} else {
			entry["enabled"] = false
		}
	})
	if err != nil {
		return "", err
	}
	return edited, nil
}

// RemoveMCPServer deletes the named entry from the winning file, dropping
// the `mcp` key entirely when the last entry goes. Stored OAuth credentials
// are untouched — removing a server is not un-authenticating from it, and
// re-adding the same server reconnects without a new browser flow.
func RemoveMCPServer(directory, name string) (string, error) {
	path, found, err := MCPConfigFile(directory, name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("mcp server %q is not defined in any config file", name)
	}
	edited, err := editMCP(path, func(mcpSection map[string]any) {
		delete(mcpSection, name)
	})
	if err != nil {
		return "", err
	}
	return edited, nil
}

// editMCPEntry decodes path, hands the named entry to mutate (as a raw
// object, so unknown fields survive), and writes the file back.
func editMCPEntry(path, name string, mutate func(entry map[string]any)) (string, error) {
	return editMCP(path, func(mcpSection map[string]any) {
		entry, _ := mcpSection[name].(map[string]any)
		if entry == nil {
			entry = map[string]any{}
		}
		mutate(entry)
		mcpSection[name] = entry
	})
}

// editMCP is the shared write: parse (refusing JSONC), mutate the `mcp`
// object (creating it when absent and the mutation is an entry edit), and
// rewrite atomically. An emptied `mcp` object is dropped, never written as
// `"mcp": {}`.
func editMCP(path string, mutate func(mcpSection map[string]any)) (string, error) {
	root, err := mcpParseFile(path)
	if err != nil {
		return "", err
	}

	mcpSection := map[string]any{}
	if raw, ok := root["mcp"]; ok && strings.TrimSpace(string(raw)) != "null" {
		if err := json.Unmarshal(raw, &mcpSection); err != nil {
			return "", fmt.Errorf("%s: \"mcp\" is not a valid server map: %w", path, err)
		}
		if mcpSection == nil {
			mcpSection = map[string]any{}
		}
	}
	mutate(mcpSection)

	if len(mcpSection) == 0 {
		delete(root, "mcp")
	} else {
		encoded, err := json.Marshal(mcpSection)
		if err != nil {
			return "", err
		}
		root["mcp"] = encoded
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeConfigFile(path, append(out, '\n')); err != nil {
		return "", err
	}
	return path, nil
}

// mcpCandidateFiles lists the config files in LoadTraced's merge order for
// directory. The list contains files that may not exist; readers skip them.
func mcpCandidateFiles(directory string) ([]string, error) {
	var files []string
	for _, name := range []string{"config.json", "gocode.json", "gocode.jsonc"} {
		files = append(files, filepath.Join(global.Resolve().Config, name))
	}
	if path := os.Getenv("GOCODE_CONFIG"); path != "" {
		files = append(files, path)
	}
	if !disableProjectConfig() {
		worktree := configpaths.Worktree(directory)
		files = append(files, configpaths.Files("gocode", directory, worktree)...)
		for _, dir := range configpaths.Directories(directory, worktree) {
			if filepath.Base(dir) != ".gocode" && dir != os.Getenv("GOCODE_CONFIG_DIR") {
				continue
			}
			for _, name := range []string{"gocode.json", "gocode.jsonc"} {
				files = append(files, filepath.Join(dir, name))
			}
		}
	}
	if content := os.Getenv("GOCODE_CONFIG_CONTENT"); content != "" {
		_ = content // inline override: never a file, never writable here
	}
	return files, nil
}

// mcpParseFile reads and decodes a config file into its raw key map,
// refusing a JSONC file whose comments a rewrite would destroy.
func mcpParseFile(path string) (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	stripped, err := StripJSONC(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(stripped) != strings.TrimSpace(string(raw)) {
		return nil, fmt.Errorf("%s contains comments or trailing commas; edit its \"mcp\" section by hand", path)
	}
	root := map[string]json.RawMessage{}
	if trimmed := strings.TrimSpace(stripped); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return root, nil
}

// mcpFileDefines reports whether raw's `mcp` object carries name.
func mcpFileDefines(raw []byte, name string) (bool, error) {
	root, err := mcpParseFileBytes(raw)
	if err != nil {
		return false, err
	}
	mcpSection, _ := root["mcp"].(map[string]any)
	if mcpSection == nil {
		return false, nil
	}
	_, ok := mcpSection[name]
	return ok, nil
}

func mcpParseFileBytes(raw []byte) (map[string]any, error) {
	stripped, err := StripJSONC(string(raw))
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if trimmed := strings.TrimSpace(stripped); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}
