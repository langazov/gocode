package builtins

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/skill"
	"github.com/langazov/gocode-go/internal/tool"
)

// skillFileLimit caps how many supporting files are listed for a skill,
// matching the TypeScript tool's ripgrep limit. The listing is a sample, not
// an inventory.
const skillFileLimit = 10

// SkillTool injects a named skill's instructions into the conversation.
type SkillTool struct {
	registry *skill.Registry
}

func NewSkillTool(registry *skill.Registry) *SkillTool {
	return &SkillTool{registry: registry}
}

func (t *SkillTool) Name() string { return "skill" }

func (t *SkillTool) Description() string {
	return strings.Join([]string{
		"Load a specialized skill when the task at hand matches one of the skills listed in the system prompt.",
		"",
		"Use this tool to inject the skill's instructions and resources into current conversation. The output may contain detailed workflow guidance as well as references to scripts, files, etc in the same directory as the skill.",
		"",
		"The skill name must match one of the skills listed in your system prompt.",
	}, "\n")
}

func (t *SkillTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The name of the skill from available_skills",
			},
		},
		"required": []string{"name"},
	}
}

func (t *SkillTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	name := stringArg(input, "name")
	if name == "" {
		return "", fmt.Errorf("skill: name is required")
	}
	if t.registry == nil {
		return "", fmt.Errorf("skill %q not found. Available skills: none", name)
	}
	info, err := t.registry.Require(name)
	if err != nil {
		return "", err
	}

	dir := info.Dir()
	lines := []string{
		fmt.Sprintf("<skill_content name=%q>", info.Name),
		"# Skill: " + info.Name,
		"",
		strings.TrimSpace(info.Content),
		"",
		"Base directory for this skill: " + dir,
		"Relative paths in this skill (e.g., scripts/, reference/) are relative to this base directory.",
		"Note: file list is sampled.",
		"",
		"<skill_files>",
	}
	for _, file := range skillFiles(dir, skillFileLimit) {
		lines = append(lines, "<file>"+file+"</file>")
	}
	lines = append(lines, "</skill_files>", "</skill_content>")
	return strings.Join(lines, "\n"), nil
}

// skillFiles samples the skill's supporting files, excluding SKILL.md itself.
func skillFiles(dir string, limit int) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || strings.EqualFold(entry.Name(), "SKILL.md") {
			return nil
		}
		out = append(out, path)
		if len(out) >= limit {
			return fs.SkipAll
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// SkillPrompt renders the available-skills block for the system prompt. An
// empty registry renders nothing so the prompt is unchanged when no skills
// exist.
func SkillPrompt(registry *skill.Registry) string {
	if registry == nil {
		return ""
	}
	infos := registry.List()
	if len(infos) == 0 {
		return ""
	}
	lines := []string{"<available_skills>"}
	for _, info := range infos {
		if info.Description != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", info.Name, info.Description))
			continue
		}
		lines = append(lines, "- "+info.Name)
	}
	lines = append(lines, "</available_skills>",
		"Use the skill tool to load one of these when the task matches.")
	return strings.Join(lines, "\n")
}

var _ tool.Tool = (*SkillTool)(nil)
