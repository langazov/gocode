// Package harness ports Harness² (github.com/google-research/harness2, Apache
// 2.0): recursive harness adaptation. A harness is the editable tree of
// components around the model — always-on prompt text, skills, subagents,
// scripts and tool rules. Task-level recursion refines it for one task through
// Propose–Probe–Compose; harnessing-level recursion keeps the adopted tree and
// an evidence-backed lesson in a per-domain experience library.
//
// This file ports harness2/core/tree.py: read, validate, write and diff the
// tree. gocode's surface uses gocode's own config layout, so a harness
// directory is directly a skill root (skill.Scan) and an agent root
// (config.LoadAgentMarkdown); see Surface.
package harness

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Tree maps a slash-separated relative path to its content. The edit
// contract is omitted-is-inherited: a proposal emits only what it changes.
type Tree map[string]string

// Clone returns an independent copy.
func (t Tree) Clone() Tree {
	out := make(Tree, len(t))
	for rel, content := range t {
		out[rel] = content
	}
	return out
}

// Paths returns the tree's paths, sorted.
func (t Tree) Paths() []string {
	out := make([]string, 0, len(t))
	for rel := range t {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// The always-on files, in prompt order. Every character here is in context on
// every turn, so their combined length is capped (AlwaysOnMax).
const (
	FileSystemPrompt = "systemprompt.md"
	FileGuardrails   = "guardrails.md"
	FileMemory       = "LongTermMEMORY.md"
	FileToolGuidance = "tool_guidance.md"
	// FileHooks holds the declarative tool rules (see hooks.go) — gocode's
	// stand-in for opencode's plugin/<name>.ts, which a Go binary cannot load.
	FileHooks = "hooks.json"
)

// AlwaysOn lists the always-on files in prompt order.
var AlwaysOn = []string{FileSystemPrompt, FileGuardrails, FileMemory, FileToolGuidance}

// AlwaysOnMax caps the always-on text, as OPENCODE_SURFACE.always_on_max does.
const AlwaysOnMax = 24_000

const alwaysOnCapReason = "every token here competes with the agent's own output budget"

// ReservedScripts are helper names a benchmark integration may provide, so a
// reference to one is never reported as missing (tree.RESERVED_SCRIPTS).
var ReservedScripts = map[string]bool{"read_doc.py": true, "pack.py": true, "unpack.py": true}

const namePattern = `[a-z0-9][a-z0-9_\-]*`

var (
	rxSkill       = regexp.MustCompile(`^skills/(` + namePattern + `)/SKILL\.md$`)
	rxSkillScript = regexp.MustCompile(`^skills/` + namePattern + `/scripts/[A-Za-z0-9_.\-]+$`)
	rxAgent       = regexp.MustCompile(`^agent/(` + namePattern + `)\.md$`)
	rxScript      = regexp.MustCompile(`^scripts/[a-z0-9_][a-z0-9_]*\.(?:py|sh)$`)
	rxScriptRef   = regexp.MustCompile(`(?:python3?|bash|sh)\s+((?:skills/[\w\-]+/)?scripts/[\w.\-]+\.(?:py|sh))`)
	rxDescription = regexp.MustCompile(`(?m)^description:\s*\S`)
	rxDescLine    = regexp.MustCompile(`(?m)^description:\s*(.+)$`)
	rxNameLine    = regexp.MustCompile(`(?m)^name:\s*(\S+)\s*$`)
	rxModeSub     = regexp.MustCompile(`(?m)^mode:\s*subagent\s*$`)
)

// Stubs are README seeds written beside an empty component directory so an
// editor knows how to add one. They are never part of a Tree.
var Stubs = map[string]string{
	"scripts/README.md": "<!-- deterministic helpers, run as `python3 scripts/<name>.py` or `sh scripts/<name>.sh`. " +
		"Add one by emitting scripts/<name>.py. -->\n",
	"agent/README.md": "<!-- delegated subagents, invoked with the `task` tool in their OWN context. " +
		"Add one as agent/<name>.md with frontmatter `mode: subagent` and `description:`. -->\n",
	"skills/README.md": "<!-- reusable workflows loaded on demand via the `skill` tool. Add one as " +
		"skills/<name>/SKILL.md with frontmatter `name:` matching the directory and a `description:`. -->\n",
}

// NormalizeRel cleans a relative path, rejecting one that escapes the tree
// before it is ever joined to a directory.
func NormalizeRel(rel string) (string, error) {
	r := strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/")
	if strings.HasPrefix(r, "/") {
		return "", fmt.Errorf("path escapes the harness: %q", rel)
	}
	for _, part := range strings.Split(r, "/") {
		if part == ".." {
			return "", fmt.Errorf("path escapes the harness: %q", rel)
		}
	}
	for strings.HasPrefix(r, "./") {
		r = r[2:]
	}
	return strings.Trim(r, "/"), nil
}

// IsAllowed reports whether rel is on the editable surface. It never fails.
func IsAllowed(rel string) bool {
	r, err := NormalizeRel(rel)
	if err != nil {
		return false
	}
	for _, name := range AlwaysOn {
		if r == name {
			return true
		}
	}
	return r == FileHooks || rxSkill.MatchString(r) || rxSkillScript.MatchString(r) ||
		rxAgent.MatchString(r) || rxScript.MatchString(r)
}

// SkillNames returns the skills in the tree, sorted.
func SkillNames(tree Tree) []string { return names(tree, rxSkill) }

// AgentNames returns the subagents in the tree, sorted.
func AgentNames(tree Tree) []string { return names(tree, rxAgent) }

func names(tree Tree, rx *regexp.Regexp) []string {
	var out []string
	for rel := range tree {
		if m := rx.FindStringSubmatch(rel); m != nil {
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// ScriptCount returns how many top-level scripts the tree ships.
func ScriptCount(tree Tree) int {
	n := 0
	for rel := range tree {
		if rxScript.MatchString(rel) {
			n++
		}
	}
	return n
}

// ReadTree reads every on-surface file under dir; stubs are excluded. Files
// present but off the surface are appended to rejected when it is non-nil.
// A missing directory is an empty tree.
func ReadTree(dir string, rejected *[]string) (Tree, error) {
	out := Tree{}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return out, nil
	}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "__pycache__" {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, stub := Stubs[rel]; stub {
			return nil
		}
		if !IsAllowed(rel) {
			if rejected != nil {
				*rejected = append(*rejected, rel)
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = strings.ToValidUTF8(string(data), "�")
		return nil
	})
	if rejected != nil {
		sort.Strings(*rejected)
	}
	return out, err
}

// IsTree reports whether dir holds a harness: at least one always-on file.
func IsTree(dir string) bool {
	for _, name := range AlwaysOn {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// WriteTree materialises tree at dest exactly: on-surface files already there
// are removed first, files off the surface are kept, every always-on file
// exists (empty when the tree omits it), and stubs are seeded when asked.
func WriteTree(tree Tree, dest string, stubs bool) (string, error) {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	var stale []string
	_ = filepath.WalkDir(dest, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dest, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, stub := Stubs[rel]; stub || IsAllowed(rel) {
			stale = append(stale, path)
		}
		return nil
	})
	for _, path := range stale {
		if err := os.Remove(path); err != nil {
			return "", err
		}
	}
	write := func(rel, content string) error {
		path := filepath.Join(dest, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}
	for _, name := range AlwaysOn {
		if _, ok := tree[name]; !ok {
			if err := write(name, ""); err != nil {
				return "", err
			}
		}
	}
	for _, rel := range tree.Paths() {
		clean, err := NormalizeRel(rel)
		if err != nil {
			return "", err
		}
		if err := write(clean, tree[rel]); err != nil {
			return "", err
		}
	}
	if stubs {
		for rel, body := range Stubs {
			path := filepath.Join(dest, filepath.FromSlash(rel))
			if _, err := os.Stat(path); err == nil {
				continue
			}
			if err := write(rel, body); err != nil {
				return "", err
			}
		}
	}
	return dest, nil
}

// AlwaysOnText is the always-on files joined in prompt order.
func AlwaysOnText(tree Tree) string {
	parts := make([]string, 0, len(AlwaysOn))
	for _, name := range AlwaysOn {
		parts = append(parts, tree[name])
	}
	return strings.Join(parts, "\n\n")
}

// AutoTrigger gives every skill a trigger line in guardrails.md when no
// always-on text names it, returning the names repaired. A line that would
// push the text over the cap is skipped, so validation still reports it.
func AutoTrigger(tree Tree) []string {
	var fixed []string
	for _, name := range SkillNames(tree) {
		if strings.Contains(AlwaysOnText(tree), name) {
			continue
		}
		head := frontmatterHead(tree["skills/"+name+"/SKILL.md"])
		desc := "the " + name + " workflow applies"
		if m := rxDescLine.FindStringSubmatch(head); m != nil {
			desc = strings.TrimRight(strings.TrimSpace(m[1]), ".")
		}
		line := "- When " + desc + ", load the `" + name + "` skill.\n"
		if len(AlwaysOnText(tree))+len(line)+2 > AlwaysOnMax {
			continue
		}
		current := tree[FileGuardrails]
		if strings.TrimSpace(current) != "" {
			tree[FileGuardrails] = strings.TrimRight(current, " \t\r\n") + "\n" + line
		} else {
			tree[FileGuardrails] = line
		}
		fixed = append(fixed, name)
	}
	return fixed
}

func frontmatterHead(content string) string {
	if len(content) > 600 {
		return content[:600]
	}
	return content
}

// Census summarises a validated tree.
type Census struct {
	AlwaysOnChars int `json:"alwaysOnChars"`
	Files         int `json:"files"`
	Skills        int `json:"skills"`
	Agents        int `json:"agents"`
	Scripts       int `json:"scripts"`
	Hooks         int `json:"hooks"`
}

// ValidationError lists every problem a tree has, so one retry can fix all
// of them at once.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string { return strings.Join(e.Problems, " | ") }

// ValidateOptions tunes Validate; the zero value is the default surface.
type ValidateOptions struct {
	// DisallowAgents rejects any subagent (a deployment without the task tool).
	DisallowAgents bool
	// Reserved adds script names a reference to which is never missing.
	Reserved map[string]bool
}

// Validate checks every surface rule, returning a *ValidationError listing
// them all. Ports tree.validate.
func Validate(tree Tree, opts ValidateOptions) (Census, error) {
	var errs []string
	body := AlwaysOnText(tree)
	if len(body) > AlwaysOnMax {
		errs = append(errs, fmt.Sprintf("always-on content is %d chars, over the %d cap; %s",
			len(body), AlwaysOnMax, alwaysOnCapReason))
	}

	skills := SkillNames(tree)
	for _, name := range skills {
		rel := "skills/" + name + "/SKILL.md"
		head := frontmatterHead(tree[rel])
		if !rxDescription.MatchString(head) {
			errs = append(errs, rel+" needs a `description:` in its frontmatter, or the skill is dropped SILENTLY at load")
		}
		if m := rxNameLine.FindStringSubmatch(head); m == nil || m[1] != name {
			errs = append(errs, fmt.Sprintf("%s needs frontmatter `name: %s` matching its directory (a mismatch is dropped SILENTLY at load)", rel, name))
		}
		if !strings.Contains(body, name) {
			errs = append(errs, fmt.Sprintf("skill '%s' is never named in the always-on text, so the agent will not know to invoke it — "+
				"an untriggered skill is dead weight. Fix it by adding a trigger line naming `%s` to systemprompt.md or guardrails.md, "+
				"e.g. \"When <the situation this skill handles>, load the `%s` skill.\"", name, name, name))
		}
	}

	agents := AgentNames(tree)
	if len(agents) > 0 && opts.DisallowAgents {
		errs = append(errs, fmt.Sprintf("subagents are not available on this deployment: %v", agents))
	}
	for _, name := range agents {
		rel := "agent/" + name + ".md"
		head := frontmatterHead(tree[rel])
		if !rxModeSub.MatchString(head) {
			errs = append(errs, rel+" needs frontmatter `mode: subagent`")
		}
		if !rxDescription.MatchString(head) {
			errs = append(errs, rel+" needs a `description:`")
		}
	}

	hooks := 0
	if src, ok := tree[FileHooks]; ok {
		hooks = 1
		if _, err := ParseHooks(src); err != nil {
			errs = append(errs, err.Error())
		}
	}

	errs = append(errs, missingScripts(tree, opts.Reserved)...)

	if len(errs) > 0 {
		return Census{}, &ValidationError{Problems: errs}
	}
	return Census{
		AlwaysOnChars: len(body),
		Files:         len(tree),
		Skills:        len(skills),
		Agents:        len(agents),
		Scripts:       ScriptCount(tree),
		Hooks:         hooks,
	}, nil
}

// missingScripts reports every script a skill or always-on file invokes but
// the tree does not ship.
func missingScripts(tree Tree, reserved map[string]bool) []string {
	have := map[string]bool{}
	for rel := range tree {
		if rxScript.MatchString(rel) || rxSkillScript.MatchString(rel) {
			have[rel] = true
		}
	}
	alwaysOn := map[string]bool{}
	for _, name := range AlwaysOn {
		alwaysOn[name] = true
	}
	var errs []string
	for _, rel := range tree.Paths() {
		if !rxSkill.MatchString(rel) && !alwaysOn[rel] {
			continue
		}
		for _, m := range rxScriptRef.FindAllStringSubmatch(tree[rel], -1) {
			ref := m[1]
			base := ref[strings.LastIndex(ref, "/")+1:]
			if ReservedScripts[base] || reserved[base] {
				continue
			}
			if !have[ref] {
				errs = append(errs, fmt.Sprintf("%s invokes `%s` but that file is not in the harness", rel, ref))
			}
		}
	}
	return errs
}

// Diff summarises how after differs from before.
type Diff struct {
	Added              []string `json:"added"`
	Removed            []string `json:"removed"`
	Modified           []string `json:"modified"`
	AlwaysOnCharsDelta int      `json:"alwaysOnCharsDelta"`
}

// Unchanged reports whether the trees are identical.
func (d Diff) Unchanged() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Modified) == 0
}

// DiffTrees ports tree.diff_summary.
func DiffTrees(before, after Tree) Diff {
	d := Diff{Added: []string{}, Removed: []string{}, Modified: []string{}}
	for _, rel := range after.Paths() {
		if old, ok := before[rel]; !ok {
			d.Added = append(d.Added, rel)
		} else if old != after[rel] {
			d.Modified = append(d.Modified, rel)
		}
	}
	for _, rel := range before.Paths() {
		if _, ok := after[rel]; !ok {
			d.Removed = append(d.Removed, rel)
		}
	}
	d.AlwaysOnCharsDelta = len(AlwaysOnText(after)) - len(AlwaysOnText(before))
	return d
}

// Similarity is the pairwise line-Jaccard over candidate trees, so a collapse
// of every composition to one body is visible.
type Similarity struct {
	N               int     `json:"n"`
	Distinct        int     `json:"distinct"`
	MeanLineJaccard float64 `json:"meanLineJaccard"`
}

// Diversity ports tree.diversity.
func Diversity(trees map[string]Tree) Similarity {
	labels := make([]string, 0, len(trees))
	for label := range trees {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	distinct := map[string]bool{}
	for _, tree := range trees {
		encoded, _ := json.Marshal(tree) // map keys marshal sorted
		distinct[string(encoded)] = true
	}
	if len(labels) < 2 {
		return Similarity{N: len(labels), Distinct: len(distinct), MeanLineJaccard: 1}
	}
	lines := func(t Tree) map[string]bool {
		out := map[string]bool{}
		for rel, content := range t {
			for _, line := range strings.Split(content, "\n") {
				if strings.TrimSpace(line) != "" {
					out[rel+":"+line] = true
				}
			}
		}
		return out
	}
	sets := make([]map[string]bool, len(labels))
	for i, label := range labels {
		sets[i] = lines(trees[label])
	}
	var sum float64
	pairs := 0
	for i := range sets {
		for j := i + 1; j < len(sets); j++ {
			inter, union := 0, len(sets[i])
			for line := range sets[j] {
				if sets[i][line] {
					inter++
				} else {
					union++
				}
			}
			if union == 0 {
				sum++
			} else {
				sum += float64(inter) / float64(union)
			}
			pairs++
		}
	}
	mean := float64(int(sum/float64(pairs)*10000+0.5)) / 10000
	return Similarity{N: len(labels), Distinct: len(distinct), MeanLineJaccard: mean}
}
