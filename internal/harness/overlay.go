package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstalledDir is where `gocode harness install` puts an adopted harness in a
// project, relative to the project root. bootStack loads it when present.
const InstalledDir = ".gocode/harness"

// Overlay applies one harness directory to a running stack. Its always-on
// files become one system block; its skills/ and agent/ directories are
// ordinary gocode skill and agent roots (bootStack passes Dir to
// skill.Discover and config.DiscoverAgents); hooks.json becomes the runner's
// tool guard.
//
// It ports the "always-on text appended to the agent prompt" install mode of
// harness2/substrate/opencode.py (workspace_agents_md) without copying
// anything into the workspace: scripts stay in the harness directory and the
// block tells the agent where `scripts/<name>` resolves, so a rollout's
// deliverable never contains harness files.
type Overlay struct {
	Dir string
}

// NewOverlay returns the overlay for dir, or nil when dir holds no harness.
func NewOverlay(dir string) *Overlay {
	if dir == "" || !IsTree(dir) {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	return &Overlay{Dir: abs}
}

// System renders the always-on text, read fresh each turn like instruction
// files. Nothing is rendered for an overlay whose always-on files are empty
// and which ships no scripts.
func (o *Overlay) System() []string {
	if o == nil {
		return nil
	}
	var parts []string
	for _, name := range AlwaysOn {
		data, err := os.ReadFile(filepath.Join(o.Dir, name))
		if err != nil {
			continue
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			parts = append(parts, text)
		}
	}
	scripts, _ := filepath.Glob(filepath.Join(o.Dir, "scripts", "*"))
	var names []string
	for _, path := range scripts {
		if base := filepath.Base(path); IsAllowed("scripts/" + base) {
			names = append(names, base)
		}
	}
	if len(names) > 0 {
		parts = append(parts, "# HELPER SCRIPTS\n"+
			"Available: "+strings.Join(names, ", ")+".\n"+
			"A path written as `scripts/<name>` or `skills/<skill>/scripts/<name>` in this harness "+
			"lives under `"+filepath.ToSlash(o.Dir)+"/`, not the working directory: run it as "+
			"`python3 "+filepath.ToSlash(o.Dir)+"/scripts/<name>.py`.")
	}
	if len(parts) == 0 {
		return nil
	}
	return []string{"# HARNESS\n\n" + strings.Join(parts, "\n\n---\n\n")}
}

// Hooks parses the overlay's hooks.json, or returns nil when there is none or
// it is malformed (Validate is where a malformed file is reported).
func (o *Overlay) Hooks() *Hooks {
	if o == nil {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(o.Dir, FileHooks))
	if err != nil {
		return nil
	}
	hooks, err := ParseHooks(string(data))
	if err != nil {
		return nil
	}
	return hooks
}

// Description is what a chat shows about the harness it runs under: where
// it lives, what it brings, and a fingerprint that changes whenever any of
// it does.
type Description struct {
	Dir         string
	Fingerprint string
	// AlwaysOn lists the always-on files with text in them.
	AlwaysOn []string
	Skills   []string
	Agents   []string
	Scripts  int
	Hooks    int
}

// Describe reads the overlay's tree. ok is false for a nil overlay or one
// whose directory no longer holds a harness.
func (o *Overlay) Describe() (Description, bool) {
	if o == nil || !IsTree(o.Dir) {
		return Description{}, false
	}
	tree, err := ReadTree(o.Dir, nil)
	if err != nil {
		return Description{}, false
	}
	d := Description{
		Dir:         o.Dir,
		Fingerprint: treeFingerprint(tree),
		Skills:      SkillNames(tree),
		Agents:      AgentNames(tree),
		Scripts:     ScriptCount(tree),
	}
	for _, name := range AlwaysOn {
		if strings.TrimSpace(tree[name]) != "" {
			d.AlwaysOn = append(d.AlwaysOn, name)
		}
	}
	if hooks, err := ParseHooks(tree[FileHooks]); err == nil && hooks != nil {
		d.Hooks = len(hooks.Rules)
	}
	return d, true
}

// treeFingerprint hashes every path and its content, in path order.
func treeFingerprint(tree Tree) string {
	h := sha256.New()
	for _, path := range tree.Paths() {
		fmt.Fprintf(h, "%s\x00%d\x00%s", path, len(tree[path]), tree[path])
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
