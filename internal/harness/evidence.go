package harness

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/diff"
)

// The evidence tree an improver reads, porting harness2/core/context.py. It
// is the only way evidence reaches a model: files, not prompt text, so nothing
// is truncated to fit a prompt.
//
//	context/task.md                  the task as the agent received it
//	context/source/                  the workspace the task started from
//	context/decisions/<label>.md     earlier stages' notes
//	context/rollouts/<label>.md      full trajectories
//	context/deliverables/<label>.md  what each rollout produced
//	context/harness/<rel>            the tree being edited
//	context/library/INDEX.md         earlier tasks in this domain + their trees
//	context/pool/<label>/            candidate harness + its rollout

// RunRecord is a rollout whose evidence is on disk in Dir.
type RunRecord struct {
	Rollout
	Dir string `json:"-"`
}

// Evidence file names inside a RunRecord's Dir.
const (
	runJSON         = "run.json"
	trajectoryFile  = "trajectory.md"
	finalFile       = "final.md"
	deliverableFile = "deliverable.md"
)

// Trajectory reads the rendered session.
func (r RunRecord) Trajectory() string {
	return readOr(filepath.Join(r.Dir, trajectoryFile), "(trajectory unavailable)")
}

// Deliverable reads what the run produced: its final message and its change.
func (r RunRecord) Deliverable() string {
	return readOr(filepath.Join(r.Dir, deliverableFile), "(no deliverable produced)")
}

func readOr(path, fallback string) string {
	data, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(data)) == "" {
		return fallback
	}
	return string(data)
}

// PoolEntry is one candidate harness and, once rolled, its rollout.
type PoolEntry struct {
	Label      string
	HarnessDir string
	Run        *RunRecord
}

// EvidenceInput is everything one improver stage is shown.
type EvidenceInput struct {
	Task       Task
	Rollouts   []RunRecord
	HarnessDir string
	LibraryDir string
	Pool       []PoolEntry
	Decisions  map[string]string
	Extra      []ExtraEvidence
}

// ExtraEvidence is a read-only directory of reference files shown beside
// the evidence, e.g. `cloud-skills/`.
type ExtraEvidence struct {
	Dir         string
	Description string
	Files       Tree
}

// Layout is what actually landed in an evidence tree, so the mission names
// only what exists.
type Layout struct {
	Root        string
	Rollouts    []string
	Source      int
	Decisions   int
	Harness     int
	Library     bool
	Pool        []string
	Extra       []ExtraEvidence
	FailedReads []string
}

// String is the evidence listing that goes in the mission.
func (l Layout) String() string {
	lines := []string{"context/task.md                  the task as the agent received it"}
	if l.Source > 0 {
		lines = append(lines, "context/source/                 the workspace the task started from")
	}
	if l.Decisions > 0 {
		lines = append(lines, "context/decisions/              earlier edits and composition decisions")
	}
	if len(l.Rollouts) > 0 {
		shown := l.Rollouts
		more := ""
		if len(shown) > 6 {
			more = fmt.Sprintf(" (+%d more)", len(shown)-6)
			shown = shown[:6]
		}
		names := make([]string, len(shown))
		for i, label := range shown {
			names[i] = label + ".md"
		}
		lines = append(lines, "context/rollouts/               FULL trajectories: "+strings.Join(names, ", ")+more)
		lines = append(lines, "context/deliverables/<label>.md what each rollout produced: its final message and its change")
	}
	if l.Harness > 0 {
		lines = append(lines, fmt.Sprintf("context/harness/                the harness you are editing (%d files)", l.Harness))
	}
	if len(l.Pool) > 0 {
		lines = append(lines, fmt.Sprintf("context/pool/                   %d candidates, each with its harness and its rollout", len(l.Pool)))
	}
	if l.Library {
		lines = append(lines, "context/library/                earlier tasks in this area -- browse INDEX.md first, then open what looks relevant")
	}
	for _, extra := range l.Extra {
		lines = append(lines, fmt.Sprintf("context/%-24s%s", extra.Dir+"/", extra.Description))
	}
	return strings.Join(lines, "\n")
}

// maxSourceBytes bounds how much of the task workspace is copied into an
// evidence tree; past it the source is listed instead of copied, because
// every stage copies its evidence again.
const maxSourceBytes = 64 << 20

// BuildEvidence writes the evidence tree at dest (replacing it) and reports
// what landed. Ports context.build.
func BuildEvidence(dest string, in EvidenceInput) (Layout, error) {
	if err := os.RemoveAll(dest); err != nil {
		return Layout{}, err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return Layout{}, err
	}
	layout := Layout{Root: dest}
	if err := os.WriteFile(filepath.Join(dest, "task.md"), []byte(in.Task.Prompt+"\n"), 0o644); err != nil {
		return Layout{}, err
	}
	n, err := copySource(in.Task.Workspace, filepath.Join(dest, "source"))
	if err != nil {
		return Layout{}, err
	}
	layout.Source = n

	labels := make([]string, 0, len(in.Decisions))
	for label := range in.Decisions {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		notes := in.Decisions[label]
		if strings.TrimSpace(notes) == "" {
			notes = "No decision notes returned."
		}
		if err := writeFileAll(filepath.Join(dest, "decisions", label+".md"), notes); err != nil {
			return Layout{}, err
		}
		layout.Decisions++
	}

	for _, run := range in.Rollouts {
		if err := writeFileAll(filepath.Join(dest, "rollouts", run.Label+".md"), run.Trajectory()); err != nil {
			return Layout{}, err
		}
		if err := writeFileAll(filepath.Join(dest, "deliverables", run.Label+".md"), run.Deliverable()); err != nil {
			return Layout{}, err
		}
		if run.OK {
			layout.Rollouts = append(layout.Rollouts, run.Label)
		} else {
			layout.FailedReads = append(layout.FailedReads, run.Label+": "+run.Error)
		}
	}

	if in.HarnessDir != "" {
		count, err := copyTreeFiles(in.HarnessDir, filepath.Join(dest, "harness"))
		if err != nil {
			return Layout{}, err
		}
		layout.Harness = count
	}

	reference, _ := ReadTree(filepath.Join(dest, "harness"), nil)
	for _, entry := range in.Pool {
		dir := filepath.Join(dest, "pool", entry.Label)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Layout{}, err
		}
		if entry.HarnessDir != "" {
			if _, err := copyTreeFiles(entry.HarnessDir, filepath.Join(dir, "harness")); err != nil {
				return Layout{}, err
			}
			candidate, _ := ReadTree(entry.HarnessDir, nil)
			if err := os.WriteFile(filepath.Join(dir, "harness.diff"), []byte(TreeDiff(reference, candidate, "current", entry.Label)), 0o644); err != nil {
				return Layout{}, err
			}
		}
		if entry.Run != nil {
			if err := os.WriteFile(filepath.Join(dir, "rollout.md"), []byte(entry.Run.Trajectory()), 0o644); err != nil {
				return Layout{}, err
			}
			if err := os.WriteFile(filepath.Join(dir, "produced.md"), []byte(entry.Run.Deliverable()), 0o644); err != nil {
				return Layout{}, err
			}
		}
		layout.Pool = append(layout.Pool, entry.Label)
	}

	if in.LibraryDir != "" && hasFiles(in.LibraryDir) {
		if err := CopyDir(in.LibraryDir, filepath.Join(dest, "library")); err != nil {
			return Layout{}, err
		}
		layout.Library = true
	}

	for _, extra := range in.Extra {
		if len(extra.Files) == 0 {
			continue
		}
		for _, rel := range extra.Files.Paths() {
			clean, err := NormalizeRel(rel)
			if err != nil {
				continue
			}
			if err := writeFileAll(filepath.Join(dest, extra.Dir, filepath.FromSlash(clean)), extra.Files[rel]); err != nil {
				return Layout{}, err
			}
		}
		layout.Extra = append(layout.Extra, extra)
	}

	if err := AssertNoGraderVerdicts(dest); err != nil {
		return Layout{}, err
	}
	if len(in.Rollouts) > 0 && len(layout.Rollouts) == 0 {
		return Layout{}, fmt.Errorf("no trajectory could be rendered for ANY rollout -- the improver would be adapting on nothing: %s",
			strings.Join(layout.FailedReads, "; "))
	}
	return layout, nil
}

// TreeDiff renders a unified diff between two trees.
func TreeDiff(from, to Tree, fromLabel, toLabel string) string {
	paths := map[string]bool{}
	for rel := range from {
		paths[rel] = true
	}
	for rel := range to {
		paths[rel] = true
	}
	sorted := make([]string, 0, len(paths))
	for rel := range paths {
		sorted = append(sorted, rel)
	}
	sort.Strings(sorted)
	var out strings.Builder
	for _, rel := range sorted {
		if from[rel] == to[rel] {
			continue
		}
		out.WriteString(diff.Unified(fromLabel+"/"+rel, toLabel+"/"+rel, from[rel], to[rel]))
	}
	return out.String()
}

// Mission is the short pointer to the evidence on disk (context.mission).
func Mission(job string, layout Layout) string {
	return strings.Join([]string{
		strings.TrimSpace(job),
		"",
		"# YOUR EVIDENCE",
		"",
		"It is on disk, in `context/`. Read it -- do not answer from this message alone.",
		"",
		layout.String(),
		"",
		"Every trajectory EVENT is present: the trajectory was not clipped to fit this mission. An individual " +
			"observation may contain an explicit renderer-truncation marker; that marker is ours, not behavior by the " +
			"solver. Do not diagnose it as a solver failure.",
		"",
		"Read `task.md` and the current harness first. Inventory every available rollout or candidate and inspect " +
			"each at least once before deciding. For a candidate, compare what changed from its editing baseline, what " +
			"behavior changed in its rollout, what improved, and what regressed or remained unresolved. Then deep-read " +
			"the evidence relevant to any edit. Cite the specific trajectory moment behind every change; one you cannot " +
			"trace to a moment is speculation.",
	}, "\n")
}

// GraderVerdictMarkers are strings only a grader's own output carries
// (context.GRADER_VERDICT_MARKERS). Finding one in an evidence tree means a
// read path leaked it.
var GraderVerdictMarkers = []string{
	"deterministic_eval_summary",
	"deterministic_eval_result",
	"judge_scope_and_deterministic_status",
	"Judge findings on the rendered result",
	`"passed_count"`,
	`"pass_rate"`,
	`"test_pass_rate"`,
}

var markerScanSuffixes = map[string]bool{
	".md": true, ".txt": true, ".json": true, ".csv": true, ".tsv": true, ".yaml": true, ".yml": true,
	".html": true, ".htm": true, ".log": true, ".py": true, ".js": true, ".sh": true, ".xml": true,
	".ini": true, ".toml": true, ".patch": true, ".diff": true, "": true,
}

// AssertNoGraderVerdicts refuses a tree carrying a grader's output, by name
// or content. A tripwire, not the defence: if it fires, find the read path.
func AssertNoGraderVerdicts(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if err := markerIn(rel, "NAME", rel); err != nil {
			return err
		}
		if !markerScanSuffixes[strings.ToLower(filepath.Ext(rel))] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		return markerIn(rel, "content", string(data))
	})
}

// AssertTreeHasNoGraderVerdicts is the same tripwire on an agent-authored
// tree, before it can be rolled or stored.
func AssertTreeHasNoGraderVerdicts(tree Tree) error {
	for _, rel := range tree.Paths() {
		if err := markerIn(rel, "NAME", rel); err != nil {
			return err
		}
		if markerScanSuffixes[strings.ToLower(filepath.Ext(rel))] {
			if err := markerIn(rel, "content", tree[rel]); err != nil {
				return err
			}
		}
	}
	return nil
}

func markerIn(rel, where, body string) error {
	for _, marker := range GraderVerdictMarkers {
		if i := strings.Index(body, marker); i >= 0 {
			return fmt.Errorf("grader verdict would enter model context: %q contains marker %q in its %s at offset %d -- "+
				"this is a leak, not a false positive to widen around; find the read path that produced it", rel, marker, where, i)
		}
	}
	return nil
}

// copySource copies the task workspace into an evidence tree, skipping VCS
// and dependency directories; past maxSourceBytes it writes a file listing
// instead, so a large repository does not get copied once per stage.
func copySource(src, dst string) (int, error) {
	if src == "" {
		return 0, nil
	}
	var files []string
	var total int64
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != src && skipDiffDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		rel, _ := filepath.Rel(src, path)
		files = append(files, rel)
		return nil
	})
	if err != nil || len(files) == 0 {
		return 0, err
	}
	if total > maxSourceBytes {
		listing := fmt.Sprintf("# Source listing\n\nThe workspace is %d MB, too large to copy into every evidence tree. "+
			"These are its files; the trajectories show what the runs read from them.\n\n- %s\n",
			total>>20, strings.Join(files, "\n- "))
		return len(files), writeFileAll(filepath.Join(dst, "LISTING.md"), listing)
	}
	for _, rel := range files {
		info, err := os.Stat(filepath.Join(src, rel))
		if err != nil {
			return 0, err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, err
		}
		if err := copyFile(filepath.Join(src, rel), target, info.Mode().Perm()); err != nil {
			return 0, err
		}
	}
	return len(files), nil
}

// copyTreeFiles copies a component tree, dropping byte-compiled files.
func copyTreeFiles(src, dst string) (int, error) {
	n := 0
	err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "__pycache__" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".pyc") || !entry.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		n++
		return copyFile(path, target, 0o644)
	})
	return n, err
}

func hasFiles(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(_ string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

func writeFileAll(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAll(path, string(data)+"\n")
}

func readJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
