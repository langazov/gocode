// Package skill discovers and loads skills — markdown files whose frontmatter
// names a capability the model can pull into context on demand.
//
// Ports the discovery half of packages/opencode/src/skill/index.ts and
// packages/core/src/skill.ts.
package skill

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Info is one discovered skill.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Slash marks a skill that is also exposed as a slash command.
	Slash bool `json:"slash,omitempty"`
	// Location is the absolute path of the skill's markdown file. For an
	// external skill it is a descriptive, non-filesystem reference (e.g.
	// "gocoder.org library: .skills/go-dev").
	Location string `json:"location"`
	// Content is the markdown body, without frontmatter.
	Content string `json:"content"`
	// Source names the plugin an external skill was registered by
	// (SetExternal); empty for skills on disk and built-ins. An external
	// skill has no directory on this machine.
	Source string `json:"source,omitempty"`
}

// IsExternal reports whether the skill was registered by a plugin rather
// than discovered on disk or compiled in.
func (i Info) IsExternal() bool { return i.Source != "" }

// Dir is the directory holding the skill's supporting files.
func (i Info) Dir() string { return filepath.Dir(i.Location) }

// NotFoundError reports a skill the registry does not know.
type NotFoundError struct {
	Name      string
	Available []string
}

func (e *NotFoundError) Error() string {
	available := strings.Join(e.Available, ", ")
	if available == "" {
		available = "none"
	}
	return fmt.Sprintf("Skill %q not found. Available skills: %s", e.Name, available)
}

// Registry holds the skills discovered for a workspace.
//
// It merges three layers, first writer wins on a name collision: skills on
// disk (Discover's roots, plus anything Add-ed), then external skills
// plugins register (SetExternal — library-plugin's gocoder.org Library
// skills), then the built-ins. So a skill the user has on disk always
// shadows a same-named remote one, and both shadow a built-in.
type Registry struct {
	mu     sync.RWMutex
	skills map[string]Info // the merged view every reader sees

	disk     map[string]Info
	external map[string][]Info // by source
	builtins bool
	// roots are the directories Discover scanned, kept so Rescan can
	// repeat the same discovery. Nil for a registry built by hand.
	roots []string
}

func NewRegistry() *Registry {
	return &Registry{skills: map[string]Info{}, disk: map[string]Info{}, external: map[string][]Info{}}
}

func (r *Registry) Add(info Info) {
	if info.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// First writer wins, so a project skill is not clobbered by a global one
	// discovered later in the scan order.
	if _, exists := r.disk[info.Name]; exists {
		return
	}
	r.disk[info.Name] = info
	r.merge()
}

// SetExternal replaces every skill registered by source with infos (an
// empty list clears them). Each is stamped with source, and ranks below
// skills on disk and above built-ins.
func (r *Registry) SetExternal(source string, infos []Info) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(infos) == 0 {
		delete(r.external, source)
	} else {
		stamped := make([]Info, 0, len(infos))
		for _, info := range infos {
			if info.Name == "" {
				continue
			}
			info.Source = source
			stamped = append(stamped, info)
		}
		r.external[source] = stamped
	}
	r.merge()
}

// merge rebuilds the merged view from the layers. Caller holds r.mu.
func (r *Registry) merge() {
	merged := make(map[string]Info, len(r.disk))
	for name, info := range r.disk {
		merged[name] = info
	}
	sources := make([]string, 0, len(r.external))
	for source := range r.external {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		for _, info := range r.external[source] {
			if _, taken := merged[info.Name]; !taken {
				merged[info.Name] = info
			}
		}
	}
	if r.builtins {
		// Built-ins register last, so the first-writer-wins rule makes them
		// the floor of the precedence order rather than a participant in it.
		for _, info := range Builtins() {
			if _, taken := merged[info.Name]; !taken {
				merged[info.Name] = info
			}
		}
	}
	r.skills = merged
}

func (r *Registry) Get(name string) (Info, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.skills[name]
	return info, ok
}

// Require returns a skill or a NotFoundError naming what is available.
func (r *Registry) Require(name string) (Info, error) {
	if info, ok := r.Get(name); ok {
		return info, nil
	}
	return Info{}, &NotFoundError{Name: name, Available: r.Names()}
}

// List returns every skill, ordered by name.
func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.skills))
	for _, info := range r.skills {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Names returns every skill name, sorted.
func (r *Registry) Names() []string {
	infos := r.List()
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		out = append(out, info.Name)
	}
	return out
}

// Load reads one skill markdown file. A file whose frontmatter carries no
// usable name is not a skill and is reported as such.
func Load(location string) (Info, error) {
	raw, err := os.ReadFile(location)
	if err != nil {
		return Info{}, err
	}
	// A bare <name>.md directly in a scanned directory takes its name from
	// the filename; a SKILL.md must declare one.
	base := filepath.Base(location)
	if strings.EqualFold(base, "SKILL.md") {
		base = ""
	} else {
		base = strings.TrimSuffix(base, filepath.Ext(base))
	}
	info, err := parseSkill(string(raw), base)
	if err != nil {
		return Info{}, fmt.Errorf("skill %s: %w", location, err)
	}
	info.Location = location
	return info, nil
}

// Scan discovers skills under root, following the layout gocode uses:
// `<root>/skill/**/SKILL.md`, `<root>/skills/**/SKILL.md`, and top-level
// `<root>/*.md`. Unreadable files are skipped rather than failing the scan —
// a single malformed skill must not hide every other one.
func Scan(root string) []Info {
	var out []Info
	seen := map[string]bool{}

	for _, name := range []string{"skill", "skills"} {
		base := filepath.Join(root, name)
		info, err := os.Stat(base)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				// An unreadable subtree is skipped, not fatal.
				if entry != nil && entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !strings.EqualFold(entry.Name(), "SKILL.md") {
				return nil
			}
			if seen[path] {
				return nil
			}
			seen[path] = true
			if skill, err := Load(path); err == nil {
				out = append(out, skill)
			}
			return nil
		})
	}

	// Top-level markdown files in the root itself.
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return out
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if seen[path] {
			continue
		}
		seen[path] = true
		if skill, err := Load(path); err == nil {
			out = append(out, skill)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Location < out[j].Location })
	return out
}

// Discover scans every root in order and returns a populated registry.
// Earlier roots win on a name collision, so project skills override global
// ones. Skills compiled into the binary sit at the bottom of that order:
// they are the default everyone starts from, and anything a user writes on
// disk — project or global — replaces them for that name.
func Discover(roots ...string) *Registry {
	registry := NewRegistry()
	registry.roots = append([]string(nil), roots...)
	registry.builtins = true
	registry.disk = scanRoots(roots)
	registry.merge()
	return registry
}

// Rescan repeats the disk discovery this registry was built by and
// replaces that layer in place, so every holder of the pointer — the
// available-skills prompt, the skill tool, slash commands, the HTTP API —
// sees a skill written to disk after boot without a restart. External
// skills are kept. It reports the names that appeared and disappeared. A
// registry not built by Discover has nothing to rescan and is left
// unchanged.
func (r *Registry) Rescan() (added, removed []string) {
	r.mu.RLock()
	roots := r.roots
	r.mu.RUnlock()
	if roots == nil {
		return nil, nil
	}
	disk := scanRoots(roots)

	r.mu.Lock()
	defer r.mu.Unlock()
	before := r.skills
	r.disk = disk
	r.merge()
	for name := range r.skills {
		if _, ok := before[name]; !ok {
			added = append(added, name)
		}
	}
	for name := range before {
		if _, ok := r.skills[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// scanRoots scans roots in order, earlier roots winning a name collision.
func scanRoots(roots []string) map[string]Info {
	out := map[string]Info{}
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, info := range Scan(root) {
			if info.Name == "" {
				continue
			}
			if _, exists := out[info.Name]; !exists {
				out[info.Name] = info
			}
		}
	}
	return out
}
