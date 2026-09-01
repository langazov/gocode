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

	"github.com/anomalyco/opencode-go/internal/markdown"
)

// Info is one discovered skill.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Slash marks a skill that is also exposed as a slash command.
	Slash bool `json:"slash,omitempty"`
	// Location is the absolute path of the skill's markdown file.
	Location string `json:"location"`
	// Content is the markdown body, without frontmatter.
	Content string `json:"content"`
}

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
type Registry struct {
	mu     sync.RWMutex
	skills map[string]Info
}

func NewRegistry() *Registry {
	return &Registry{skills: map[string]Info{}}
}

func (r *Registry) Add(info Info) {
	if info.Name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// First writer wins, so a project skill is not clobbered by a global one
	// discovered later in the scan order.
	if _, exists := r.skills[info.Name]; exists {
		return
	}
	r.skills[info.Name] = info
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
	doc, err := markdown.Parse(string(raw))
	if err != nil {
		return Info{}, fmt.Errorf("skill %s: %w", location, err)
	}
	name := doc.String("name")
	if name == "" {
		// A bare <name>.md directly in a scanned directory takes its name from
		// the filename; a SKILL.md must declare one.
		base := filepath.Base(location)
		if !strings.EqualFold(base, "SKILL.md") {
			name = strings.TrimSuffix(base, filepath.Ext(base))
		}
	}
	if name == "" {
		return Info{}, fmt.Errorf("skill %s: frontmatter has no name", location)
	}
	return Info{
		Name:        name,
		Description: doc.String("description"),
		Slash:       doc.Bool("slash"),
		Location:    location,
		Content:     doc.Content,
	}, nil
}

// Scan discovers skills under root, following the layout opencode uses:
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
// ones.
func Discover(roots ...string) *Registry {
	registry := NewRegistry()
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, info := range Scan(root) {
			registry.Add(info)
		}
	}
	return registry
}
