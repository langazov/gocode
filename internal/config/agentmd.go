package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/anomalyco/opencode-go/internal/markdown"
)

// AgentMarkdown is one agent defined by a markdown file: YAML frontmatter for
// the settings, body for the system prompt.
//
// Ports the markdown half of the TypeScript agent loader — an agent can be
// declared either in opencode.json's `agent` map or as `.opencode/agent/<name>.md`.
type AgentMarkdown struct {
	Name  string
	Agent Agent
	// Location is the file the agent was read from.
	Location string
}

// ParseAgentMarkdown reads an agent definition from markdown content. The
// name falls back to the filename when frontmatter does not set one.
func ParseAgentMarkdown(location, content string) (AgentMarkdown, error) {
	doc, err := markdown.Parse(content)
	if err != nil {
		return AgentMarkdown{}, err
	}
	name := doc.String("name")
	if name == "" {
		base := filepath.Base(location)
		name = strings.TrimSuffix(base, filepath.Ext(base))
	}

	agent := Agent{
		Model:       doc.String("model"),
		Variant:     doc.String("variant"),
		Prompt:      strings.TrimSpace(doc.Content),
		Description: doc.String("description"),
		Mode:        doc.String("mode"),
		Hidden:      doc.Bool("hidden"),
		Color:       doc.String("color"),
	}
	if steps, ok := doc.Int("steps"); ok {
		agent.Steps = steps
	}
	if steps, ok := doc.Int("maxSteps"); ok {
		agent.MaxSteps = steps
	}
	if temperature, ok := doc.Float("temperature"); ok {
		agent.Temperature = &temperature
	}
	// Frontmatter permissions use the same shape as the JSON config, so they
	// go through the same Permission decoding rather than a parallel path.
	if raw, ok := doc.Frontmatter["permission"]; ok && raw != nil {
		agent.Permission = permissionFromAny(raw)
	}
	if agent.Mode == "" {
		agent.Mode = "all"
	}
	return AgentMarkdown{Name: name, Agent: agent, Location: location}, nil
}

// LoadAgentMarkdown reads every `<root>/agent/*.md` definition, sorted by name
// for deterministic registration. A file that cannot be parsed is skipped
// rather than failing the load, so one bad agent does not hide the others.
func LoadAgentMarkdown(root string) []AgentMarkdown {
	dir := filepath.Join(root, "agent")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []AgentMarkdown
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".md") {
			continue
		}
		location := filepath.Join(dir, entry.Name())
		raw, err := os.ReadFile(location)
		if err != nil {
			continue
		}
		parsed, err := ParseAgentMarkdown(location, string(raw))
		if err != nil || parsed.Name == "" {
			continue
		}
		out = append(out, parsed)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DiscoverAgents merges markdown agents from every root into a config's agent
// map. Existing JSON-configured agents win, and earlier roots beat later ones,
// so precedence reads config > project markdown > global markdown.
func (c *Config) DiscoverAgents(roots ...string) {
	if c.Agent == nil {
		c.Agent = map[string]Agent{}
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		for _, found := range LoadAgentMarkdown(root) {
			if _, exists := c.Agent[found.Name]; exists {
				continue
			}
			c.Agent[found.Name] = found.Agent
		}
	}
}

// WriteAgentMarkdown creates `<root>/agent/<name>.md`. It refuses to overwrite
// an existing definition — replacing an agent should be an explicit edit.
//
// The header is produced by the YAML marshaller rather than assembled by hand,
// so values and keys needing quoting (a description with a colon, a `*`
// permission pattern, which YAML would otherwise read as an alias) are escaped
// correctly.
func WriteAgentMarkdown(root, name string, agent Agent) (string, error) {
	dir := filepath.Join(root, "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	location := filepath.Join(dir, name+".md")
	if _, err := os.Stat(location); err == nil {
		return "", &fs.PathError{Op: "create", Path: location, Err: fs.ErrExist}
	}

	// yaml.Node preserves key order; a plain map would emit them sorted,
	// which reads worse for a hand-edited file.
	header := &yaml.Node{Kind: yaml.MappingNode}
	appendField(header, "description", agent.Description != "", agent.Description)
	appendField(header, "mode", agent.Mode != "", agent.Mode)
	appendField(header, "model", agent.Model != "", agent.Model)
	appendField(header, "variant", agent.Variant != "", agent.Variant)
	if agent.Temperature != nil {
		appendField(header, "temperature", true, *agent.Temperature)
	}
	if steps := agent.EffectiveSteps(); steps > 0 {
		appendField(header, "steps", true, steps)
	}
	if rules := flatPermissions(agent.Permission); rules != nil {
		header.Content = append(header.Content, scalarNode("permission"), rules)
	}

	encoded, err := yaml.Marshal(header)
	if err != nil {
		return "", err
	}
	body := agent.Prompt
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	content := "---\n" + string(encoded) + "---\n" + body
	if err := os.WriteFile(location, []byte(content), 0o644); err != nil {
		return "", err
	}
	return location, nil
}

// flatPermissions extracts the `action: effect` pairs from a permission
// config. A nested per-resource ruleset has no flat representation and is
// skipped rather than round-tripped imprecisely.
func flatPermissions(p Permission) *yaml.Node {
	if len(p.Raw) == 0 {
		return nil
	}
	actions := make([]string, 0, len(p.Raw))
	for action := range p.Raw {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	out := &yaml.Node{Kind: yaml.MappingNode}
	for _, action := range actions {
		var effect string
		if err := json.Unmarshal(p.Raw[action], &effect); err != nil {
			continue
		}
		out.Content = append(out.Content, scalarNode(action), scalarNode(effect))
	}
	if len(out.Content) == 0 {
		return nil
	}
	return out
}

// scalarNode builds a string scalar, letting the encoder decide on quoting —
// which is the point of going through the marshaller: a "*" key or a value
// containing a colon is escaped correctly rather than by hand.
func scalarNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value}
}

// appendField adds a key/value pair when present is true.
func appendField(node *yaml.Node, key string, present bool, value any) {
	if !present {
		return
	}
	child := &yaml.Node{}
	if err := child.Encode(value); err != nil {
		return
	}
	node.Content = append(node.Content, scalarNode(key), child)
}

// permissionFromAny reuses the JSON permission decoder for frontmatter, so
// markdown and opencode.json accept exactly the same shapes rather than
// drifting through two parallel parsers.
func permissionFromAny(raw any) Permission {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return Permission{}
	}
	var out Permission
	if err := out.UnmarshalJSON(encoded); err != nil {
		return Permission{}
	}
	return out
}
