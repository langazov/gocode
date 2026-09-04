package plugin

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/langazov/gocode-go/internal/configpaths"
)

// Discovery: what is installed, as opposed to what is configured.
//
// [Resolve] answers "where is this configured plugin?". This answers the
// question the plugins dialog asks instead: "what could be configured?" —
// every runnable thing sitting in a `plugin/` folder under one of the config
// directories, whether or not the `plugin` array names it.
//
// The runnable test is [entrypoint], the same one the loader applies, so a
// discovered entry is exactly an entry the loader would be able to start: an
// executable file, or a directory holding a gocode-plugin.json manifest or an
// executable named `plugin`. A README, a source tree, a half-built directory
// with a non-executable file in it — none of those are offered, because
// enabling them could only fail.

// PluginDirName is the folder, inside each config directory, that holds
// installed plugins. The global one is [InstallRoot].
const PluginDirName = "plugin"

// Available is one plugin found on disk by [Discover].
type Available struct {
	// Name is the entry's base name, the plugin's identity in the interface.
	Name string
	// Ref is what the `plugin` array would have to say to load it: the bare
	// name for the global install root (the form `make install-plugin` and
	// the loader's own lookup use), an absolute path anywhere else, since
	// bare names resolve against the global root only.
	Ref string
	// Path is the discovered directory or file itself.
	Path string
	// Root is the plugin folder it was found in, for reporting where a
	// duplicate name came from.
	Root string
}

// Discover lists the runnable plugins installed under every config
// directory's `plugin/` folder, in config-directory order (global first,
// project directories after). directory is the session's working directory,
// which is what the project-level `.gocode` search walks up from; empty means
// the process's own.
//
// A name found in more than one folder is reported once, from the first
// folder that had it — the same first-wins rule config-directory order
// implies everywhere else.
func Discover(directory string) []Available {
	if directory == "" {
		directory, _ = os.Getwd()
	}
	root := InstallRoot()
	var out []Available
	seen := map[string]bool{}
	for _, dir := range configpaths.Directories(directory, configpaths.Worktree(directory)) {
		folder := filepath.Join(dir, PluginDirName)
		for _, found := range discoverIn(folder, folder == root) {
			if seen[found.Name] {
				continue
			}
			seen[found.Name] = true
			out = append(out, found)
		}
	}
	return out
}

// discoverIn lists the runnable entries of one plugin folder. A missing or
// unreadable folder yields nothing: not having installed any plugins is the
// normal case, not an error to report.
func discoverIn(folder string, isInstallRoot bool) []Available {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var out []Available
	for _, entry := range entries {
		name := entry.Name()
		// Dotfiles are bookkeeping (.DS_Store, .gitkeep), never plugins.
		if name == "" || name[0] == '.' {
			continue
		}
		path := filepath.Join(folder, name)
		// The loader's own runnability test, symlinks followed — a plugin
		// directory symlinked into place is how you develop one.
		if _, _, err := entrypoint(path); err != nil {
			continue
		}
		ref := path
		if isInstallRoot {
			ref = name
		}
		out = append(out, Available{Name: name, Ref: ref, Path: path, Root: folder})
	}
	return out
}

// Installed reports the discovered plugins the given specs do not already
// name, which is what the interface shows as installed-but-disabled. A spec
// is matched against a discovery by the path it resolves to, so the same
// plugin written as a bare name in one config and as a path in another is
// recognized either way.
func Installed(specs []Spec, directory string) []Available {
	configured := map[string]bool{}
	for _, spec := range specs {
		resolved, _, err := Resolve(spec, directory)
		if err != nil {
			// An unresolvable spec still occupies its ref: the dialog shows
			// it as "not loaded" from the config side, and a discovery must
			// not double it.
			configured[spec.Ref] = true
			continue
		}
		configured[resolved.Target] = true
		configured[spec.Ref] = true
	}
	var out []Available
	for _, found := range Discover(directory) {
		if configured[found.Path] || configured[found.Ref] || configured[found.Name] {
			continue
		}
		out = append(out, found)
	}
	return out
}
