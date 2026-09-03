// Package command implements the slash-command system, porting
// packages/opencode/src/command/index.ts and the template substitution in
// session/prompt.ts.
//
// A command is a named prompt template. Running "/review main" expands the
// review template with "main" substituted into its placeholders and sends the
// result as the prompt.
package command

import (
	_ "embed"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/markdown"
	"github.com/langazov/gocode-go/internal/skill"
)

// Source records where a command came from, matching the `source` field.
const (
	SourceCommand = "command"
	SourceSkill   = "skill"
	SourceMCP     = "mcp"
)

// Info is one command.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Agent overrides the agent the command runs as.
	Agent string `json:"agent,omitempty"`
	// Model overrides the model, as "provider/model".
	Model string `json:"model,omitempty"`
	// Subtask runs the command in a subagent rather than the main session.
	Subtask bool `json:"subtask,omitempty"`
	// Template is the prompt text, before substitution.
	Template string `json:"template"`
	// Source is where the command was defined.
	Source string `json:"source,omitempty"`
	// Hints lists the placeholders the template uses, for the completion UI.
	Hints []string `json:"hints,omitempty"`
}

// Built-in command names.
const (
	NameInit   = "init"
	NameReview = "review"
)

//go:embed template/initialize.txt
var initializeTemplate string

//go:embed template/review.txt
var reviewTemplate string

// Registry holds the commands available in a session.
type Registry struct {
	byName map[string]Info
}

// Load assembles the registry from every source, in the same precedence order
// as the TypeScript service: built-ins, then config, then markdown files, then
// skills — with skills yielding to any command that already claimed the name.
//
// MCP prompts are a source upstream and are not one here: this port's MCP
// client does not implement prompts/list.
func Load(cfg *config.Config, workdir string, skills *skill.Registry, configDirs []string) *Registry {
	registry := &Registry{byName: map[string]Info{}}

	registry.add(Info{
		Name:        NameInit,
		Description: "guided AGENTS.md setup",
		Source:      SourceCommand,
		Template:    strings.ReplaceAll(initializeTemplate, "${path}", workdir),
	})
	registry.add(Info{
		Name:        NameReview,
		Description: "review changes [commit|branch|pr], defaults to uncommitted",
		Source:      SourceCommand,
		Subtask:     true,
		Template:    strings.ReplaceAll(reviewTemplate, "${path}", workdir),
	})

	if cfg != nil {
		for name, entry := range configCommands(cfg) {
			entry.Name = name
			entry.Source = SourceCommand
			registry.add(entry)
		}
	}

	// Markdown definitions override config entries of the same name, matching
	// the load order upstream uses for agents.
	for _, dir := range configDirs {
		for _, entry := range loadMarkdown(dir) {
			registry.add(entry)
		}
	}

	if skills != nil {
		for _, item := range skills.List() {
			if _, taken := registry.byName[item.Name]; taken {
				continue
			}
			registry.add(Info{
				Name:        item.Name,
				Description: item.Description,
				Source:      SourceSkill,
				Template:    skillTemplate(item),
			})
		}
	}
	return registry
}

// skillTemplate ports the skill branch: the body, plus a note about where its
// relative paths resolve from.
func skillTemplate(item skill.Info) string {
	if item.Location == "" || item.Location == "<built-in>" {
		return item.Content
	}
	return strings.Join([]string{
		item.Content,
		"",
		"Base directory for this skill: " + item.Dir(),
		"Relative paths in this skill (e.g., scripts/, references/) are relative to this base directory.",
	}, "\n")
}

func (r *Registry) add(info Info) {
	info.Hints = Hints(info.Template)
	r.byName[info.Name] = info
}

// Get returns a command by name.
func (r *Registry) Get(name string) (Info, bool) {
	if r == nil {
		return Info{}, false
	}
	info, ok := r.byName[name]
	return info, ok
}

// List returns every command, ordered by name.
func (r *Registry) List() []Info {
	if r == nil {
		return nil
	}
	out := make([]Info, 0, len(r.byName))
	for _, info := range r.byName {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Hints reports the placeholders a template uses, porting hints(): the
// distinct `$N` in ascending order, then `$ARGUMENTS` if present.
func Hints(template string) []string {
	seen := map[int]bool{}
	for _, match := range placeholderPattern.FindAllStringSubmatch(template, -1) {
		if value, err := atoi(match[1]); err == nil {
			seen[value] = true
		}
	}
	numbers := make([]int, 0, len(seen))
	for value := range seen {
		numbers = append(numbers, value)
	}
	sort.Ints(numbers)

	out := make([]string, 0, len(numbers)+1)
	for _, value := range numbers {
		out = append(out, "$"+itoa(value))
	}
	if strings.Contains(template, "$ARGUMENTS") {
		out = append(out, "$ARGUMENTS")
	}
	return out
}

// configCommands decodes the `command` config section, which is held as
// map[string]any so the loader's generic merge can work on it.
func configCommands(cfg *config.Config) map[string]Info {
	out := map[string]Info{}
	for name, raw := range cfg.Commands {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		info := Info{
			Template:    stringField(entry, "template"),
			Description: stringField(entry, "description"),
			Agent:       stringField(entry, "agent"),
			Model:       stringField(entry, "model"),
		}
		if subtask, ok := entry["subtask"].(bool); ok {
			info.Subtask = subtask
		}
		if info.Template == "" {
			continue
		}
		out[name] = info
	}
	return out
}

func stringField(entry map[string]any, key string) string {
	value, _ := entry[key].(string)
	return value
}

// loadMarkdown reads `command/**/*.md` and `commands/**/*.md` under a config
// directory, porting ConfigCommand.load.
//
// The name comes from the path with the prefix and extension stripped, so
// `command/git/commit.md` is "/git/commit" — nested directories namespace a
// command rather than flattening into a collision.
func loadMarkdown(dir string) []Info {
	var out []Info
	for _, prefix := range []string{"command", "commands"} {
		root := filepath.Join(dir, prefix)
		filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
				return nil
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			name := strings.TrimSuffix(filepath.ToSlash(relative), filepath.Ext(relative))
			// The same frontmatter parser the agent markdown loader uses, so
			// both definition styles behave identically.
			doc, err := markdown.Parse(string(contents))
			if err != nil {
				return nil
			}
			info := Info{
				Name:        name,
				Template:    strings.TrimSpace(doc.Content),
				Description: doc.String("description"),
				Agent:       doc.String("agent"),
				Model:       doc.String("model"),
				Subtask:     doc.Bool("subtask"),
				Source:      SourceCommand,
			}
			if info.Template == "" {
				return nil
			}
			out = append(out, info)
			return nil
		})
	}
	return out
}

func atoi(value string) (int, error) {
	result := 0
	if value == "" {
		return 0, errEmpty
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errEmpty
		}
		result = result*10 + int(r-'0')
	}
	return result, nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

type constError string

func (e constError) Error() string { return string(e) }

const errEmpty = constError("command: not a number")
