package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Task-level recursion, porting harness2/core/recursion.py. Each mode
// schedules 4k rollouts and 3k improver stages, including the k shared base
// executions. An invalid proposal gets one retry with the validation error
// appended; after that the stage inherits the current harness.

const (
	stageAttempts     = 2 // recursion.STAGE_ATTEMPTS
	transportAttempts = 2 // config.TRANSPORT_ATTEMPTS
)

// Stage is one improver stage: its harness, decision notes and cost.
type Stage struct {
	Label    string  `json:"label"`
	Dim      string  `json:"dim"`
	KeptBase bool    `json:"keptBase"`
	FellBack bool    `json:"fellBack"`
	Error    string  `json:"error,omitempty"`
	CostUSD  float64 `json:"costUSD"`
	Attempts int     `json:"attempts"`
	Cached   bool    `json:"cached"`
	Notes    string  `json:"notes,omitempty"`
	Tree     Tree    `json:"-"`
}

// Ledger counts what one mode spent on one task (core/ledger.py).
type Ledger struct {
	mu       sync.Mutex
	Rollouts []string `json:"rollouts"`
	Sessions []string `json:"sessions"`
	CostUSD  float64  `json:"costUSD"`
}

func (l *Ledger) rollout(label string, usd float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Rollouts = append(l.Rollouts, label)
	l.CostUSD += usd
}

func (l *Ledger) session(label string, usd float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.Sessions = append(l.Sessions, label)
	l.CostUSD += usd
}

func (l *Ledger) spend(usd float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.CostUSD += usd
}

// assertIdentity checks the budget identity R = 4k, P = 3k.
func (l *Ledger) assertIdentity(k int) error {
	if len(l.Rollouts) != ExpectedRollouts(k) || len(l.Sessions) != ExpectedSessions(k) {
		return fmt.Errorf("ledger off budget: %d rollouts (want %d), %d sessions (want %d)",
			len(l.Rollouts), ExpectedRollouts(k), len(l.Sessions), ExpectedSessions(k))
	}
	return nil
}

// modeRun is the state of one mode over one task.
type modeRun struct {
	engine *Engine
	task   Task
	k      int
	mode   string
	dir    string // <task>/k<k>/<mode>
	ledger *Ledger
	log    *runLog
	extra  []ExtraEvidence

	mu       sync.Mutex
	stages   []*Stage
	trees    map[string]Tree
	rollouts map[string]RunRecord
	finals   []string
}

func (m *modeRun) harnessDir(label string) string { return filepath.Join(m.dir, "harnesses", label) }

func (m *modeRun) emit(ev Event) {
	m.log.emit(ev)
}

// acceptTree repairs then validates a proposed tree: kept reports an
// unchanged proposal, err the reason it is rejected.
func (m *modeRun) acceptTree(proposed, fallback Tree) (bool, error) {
	AutoTrigger(proposed)
	kept := DiffTrees(fallback, proposed).Unchanged()
	if _, err := Validate(proposed, m.engine.Validate); err != nil {
		return kept, err
	}
	return kept, AssertTreeHasNoGraderVerdicts(proposed)
}

var notesRx = regexp.MustCompile(`(?s)<notes>(.*?)</notes>`)

// runStage builds evidence, asks the editor for an edit, and retries an
// invalid proposal once (recursion._run_stage).
func (m *modeRun) runStage(ctx context.Context, label, dim, job string, fallback Tree, in EvidenceInput) *Stage {
	m.emit(Event{Type: EventStageStarted, Phase: phaseOf(dim), Label: label})
	stage := m.stage(ctx, label, dim, job, fallback, in)
	m.emit(Event{Type: EventStageEnded, Phase: phaseOf(dim), Label: label, OK: !stage.FellBack, Detail: stageDetail(stage), CostUSD: stage.CostUSD})
	return stage
}

func phaseOf(dim string) string {
	if dim == "INV" || dim == "DOM" {
		return "probe"
	}
	return "compose"
}

func stageDetail(s *Stage) string {
	switch {
	case s.FellBack:
		return "fell back: " + s.Error
	case s.KeptBase:
		return "kept base"
	}
	return ""
}

func (m *modeRun) stage(ctx context.Context, label, dim, job string, fallback Tree, in EvidenceInput) *Stage {
	evidence := filepath.Join(m.dir, "context", label)
	in.Extra = m.extra
	layout, err := BuildEvidence(evidence, in)
	if err != nil {
		return &Stage{Label: label, Dim: dim, Tree: fallback.Clone(), FellBack: true,
			Error: clip(fmt.Sprintf("could not build evidence: %v", err), 400)}
	}
	mission := Mission(job, layout)
	stage := &Stage{Label: label, Dim: dim, Cached: true}
	rejection := ""
	for attempt := 1; attempt <= stageAttempts; attempt++ {
		if attempt > 1 {
			// Every rejection path lands here — empty reply, off-surface
			// files, a failed validation — so the log names each one.
			m.emit(Event{Type: EventStageRetry, Phase: phaseOf(dim), Label: label, Attempt: attempt - 1, Detail: clip(rejection, 600)})
		}
		full := mission
		if rejection != "" {
			full += "\n\n# YOUR PREVIOUS ATTEMPT WAS REJECTED\n\n" + rejection + "\n\nFix it in `context/harness/` and reply again."
		}
		reply, harvested, cached, cost, err := m.call(ctx, RoleEditor, fmt.Sprintf("%s.a%d", label, attempt), full, evidence, true)
		stage.CostUSD += cost
		stage.Attempts++
		stage.Cached = stage.Cached && cached
		if err != nil {
			rejection = err.Error()
			break
		}
		if harvested == "" {
			rejection = "the harness directory did not come back from the session; edit the files in `context/harness/` in place"
			continue
		}
		var off []string
		proposed, err := ReadTree(harvested, &off)
		if err != nil {
			rejection = err.Error()
			continue
		}
		if len(proposed) == 0 {
			rejection = "`context/harness/` came back empty -- edit the files, do not delete them all"
			continue
		}
		if len(off) > 0 {
			if len(off) > 6 {
				off = off[:6]
			}
			rejection = "these files are off the editable surface and were not applied: " + strings.Join(off, ", ") +
				". Component names must be lowercase letters, digits, `_` or `-` (e.g. `skills/doc_review/SKILL.md`, " +
				"`scripts/check_totals.py`)."
			continue
		}
		kept, err := m.acceptTree(proposed, fallback)
		if err != nil {
			rejection = err.Error()
			// The next attempt starts from the rejected edits, so the editor
			// fixes them rather than starting over.
			if _, werr := WriteTree(proposed, filepath.Join(evidence, "harness"), true); werr != nil {
				rejection += "; " + werr.Error()
			}
			continue
		}
		stage.Tree = proposed
		stage.KeptBase = kept
		if match := notesRx.FindStringSubmatch(reply.Text); match != nil {
			stage.Notes = strings.TrimSpace(match[1])
		} else {
			stage.Notes = strings.TrimSpace(reply.Text)
		}
		return stage
	}
	stage.Tree = fallback.Clone()
	stage.FellBack = true
	stage.Error = clip(rejection, 400)
	return stage
}

// call runs one improver session in a fresh workspace holding the evidence
// as `context/`, caching the reply (and, when harvest is set, the edited
// `context/harness/`) under audit/ so a resumed run does not pay twice. It
// returns the reply, the harvested directory, whether it was cached, and
// the cost actually spent.
func (m *modeRun) call(ctx context.Context, role, label, mission, evidence string, harvest bool) (Reply, string, bool, float64, error) {
	key, err := fingerprint(mission, evidence)
	if err != nil {
		return Reply{}, "", false, 0, err
	}
	safe := regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(label, "_")
	audit := filepath.Join(m.dir, "audit")
	base := filepath.Join(audit, safe+"."+key)
	harvestDir := base + ".workspace"
	if data, err := os.ReadFile(base + ".response.md"); err == nil && strings.TrimSpace(string(data)) != "" {
		if !harvest {
			return Reply{Text: string(data)}, "", true, 0, nil
		}
		if info, err := os.Stat(harvestDir); err == nil && info.IsDir() {
			return Reply{Text: string(data)}, harvestDir, true, 0, nil
		}
	}

	var spent float64
	last := ""
	for attempt := 1; attempt <= transportAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return Reply{}, "", false, spent, err
		}
		workspace := filepath.Join(m.dir, "sessions", fmt.Sprintf("%s.t%d", safe, attempt))
		_ = os.RemoveAll(workspace)
		if err := CopyDir(evidence, filepath.Join(workspace, "context")); err != nil {
			return Reply{}, "", false, spent, err
		}
		if err := writeFileAll(filepath.Join(workspace, "MISSION.md"), mission); err != nil {
			return Reply{}, "", false, spent, err
		}
		stage := strings.SplitN(label, ".", 2)[0]
		reply, err := m.engine.session(ctx, SessionRequest{
			Role: role, Label: label, Workspace: workspace, Mission: mission,
			OnSession: func(id string) {
				m.emit(Event{Type: EventSession, Label: stage, SessionID: id, Role: role, Attempt: attempt})
			},
		})
		spent += reply.CostUSD
		if err != nil {
			last = err.Error()
			_ = os.RemoveAll(workspace)
			continue
		}
		if strings.TrimSpace(reply.Text) == "" {
			last = "empty reply"
			_ = os.RemoveAll(workspace)
			continue
		}
		reply.CostUSD = spent
		_ = writeFileAll(base+".mission.md", mission)
		_ = writeFileAll(base+".response.md", reply.Text)
		harvested := ""
		if harvest {
			src := filepath.Join(workspace, "context", "harness")
			if info, err := os.Stat(src); err == nil && info.IsDir() {
				_ = os.RemoveAll(harvestDir)
				if err := CopyDir(src, harvestDir); err == nil {
					harvested = harvestDir
				}
			}
		}
		_ = os.RemoveAll(workspace)
		return reply, harvested, false, spent, nil
	}
	return Reply{}, "", false, spent, fmt.Errorf("[%s] %s: %s", label, role, last)
}

// fingerprint digests a mission and an evidence directory, so a cached reply
// is reused only for the same question over the same evidence.
func fingerprint(mission, dir string) (string, error) {
	h := sha256.New()
	h.Write([]byte(mission))
	var paths []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	sort.Strings(paths)
	for _, path := range paths {
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// roll materialises a tree and executes it once, recording the evidence on
// disk. A dead rollout is recorded, never raised. A rollout already on disk
// is reused, which is what makes a rerun resume.
func (e *Engine) roll(ctx context.Context, task Task, label string, tree Tree, harnessDir, runDir string, emit func(Event)) RunRecord {
	record := RunRecord{Dir: runDir}
	if err := readJSON(filepath.Join(runDir, runJSON), &record.Rollout); err == nil && record.Label == label {
		return record
	}
	emit(Event{Type: EventRolloutStarted, Label: label})
	defer func() {
		emit(Event{Type: EventRolloutEnded, Label: label, OK: record.OK && record.Delivered,
			Detail: record.Error, CostUSD: record.CostUSD})
	}()

	fail := func(err error) RunRecord {
		record.Rollout = Rollout{Label: label, Error: err.Error()}
		_ = writeFileAll(filepath.Join(runDir, trajectoryFile), "(rollout failed: "+err.Error()+")")
		_ = writeJSON(filepath.Join(runDir, runJSON), record.Rollout)
		return record
	}
	if _, err := WriteTree(tree, harnessDir, false); err != nil {
		return fail(err)
	}
	workspace := filepath.Join(filepath.Dir(runDir), "..", "workspaces", label)
	workspace = filepath.Clean(workspace)
	_ = os.RemoveAll(workspace)
	if err := CopyDir(task.Workspace, workspace); err != nil {
		return fail(err)
	}
	if !e.KeepWorkspaces {
		defer os.RemoveAll(workspace)
	}
	result, err := e.Exec.Rollout(ctx, RolloutRequest{
		Task: task, Label: label, HarnessDir: harnessDir, Workspace: workspace,
		OnSession: func(id string) {
			emit(Event{Type: EventSession, Label: label, SessionID: id, Role: "rollout"})
		},
	})
	if err != nil {
		return fail(err)
	}
	result.Label = label
	change, changed, err := WorkspaceDiff(task.Workspace, workspace)
	if err != nil {
		change = "(could not diff the workspace: " + err.Error() + ")"
	}
	result.Delivered = result.OK && (len(changed) > 0 || strings.TrimSpace(result.Final) != "")
	if e.KeepWorkspaces {
		result.Workspace = workspace
	}
	var deliverable strings.Builder
	deliverable.WriteString("# Final message\n\n")
	if strings.TrimSpace(result.Final) != "" {
		deliverable.WriteString(strings.TrimSpace(result.Final))
	} else {
		deliverable.WriteString("(none)")
	}
	deliverable.WriteString("\n\n# Changes to the workspace\n\n")
	if len(changed) == 0 {
		deliverable.WriteString("(no files changed)\n")
	} else {
		fmt.Fprintf(&deliverable, "%d file(s) changed: %s\n\n```diff\n%s```\n", len(changed), strings.Join(changed, ", "), change)
	}
	_ = writeFileAll(filepath.Join(runDir, trajectoryFile), result.Trajectory)
	_ = writeFileAll(filepath.Join(runDir, finalFile), result.Final)
	_ = writeFileAll(filepath.Join(runDir, deliverableFile), deliverable.String())
	_ = writeJSON(filepath.Join(runDir, runJSON), result)
	// On disk now; RunRecord reads them back on demand. Holding every
	// rollout's trajectory (up to MaxTrace each) for the whole run is what
	// made a run's memory grow with its budget.
	result.Trajectory, result.Final = "", ""
	record.Rollout = result
	return record
}

// rollMany rolls independent harnesses concurrently, then records them in the
// caller's order, so a concurrent run's ledger reads like a serial one.
func (m *modeRun) rollMany(ctx context.Context, items []rollItem) {
	out := make([]RunRecord, len(items))
	parallel(len(items), m.engine.rolloutWidth(), func(i int) {
		it := items[i]
		out[i] = m.engine.roll(ctx, m.task, it.label, it.tree, m.harnessDir(it.label), filepath.Join(m.dir, "runs", it.label), m.log.emit)
	})
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, it := range items {
		m.rollouts[it.label] = out[i]
		m.ledger.rollout(it.label, out[i].CostUSD)
	}
}

type rollItem struct {
	label string
	tree  Tree
}

// parallel runs fn(0..n-1) with at most width at once.
func parallel(n, width int, fn func(int)) {
	if width < 1 {
		width = 1
	}
	sem := make(chan struct{}, width)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

func (m *modeRun) record(stages []*Stage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, stage := range stages {
		m.stages = append(m.stages, stage)
		m.trees[stage.Label] = stage.Tree
		m.ledger.session(stage.Label, stage.CostUSD)
	}
}

func (m *modeRun) decisions() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for _, stage := range m.stages {
		if stage.Notes != "" {
			out[stage.Label] = stage.Notes
		} else {
			out[stage.Label] = stage.Error
		}
	}
	return out
}

func (m *modeRun) poolOf(labels []string) []PoolEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	pool := make([]PoolEntry, 0, len(labels))
	for _, label := range labels {
		entry := PoolEntry{Label: label, HarnessDir: m.harnessDir(label)}
		if run, ok := m.rollouts[label]; ok {
			run := run
			entry.Run = &run
		}
		pool = append(pool, entry)
	}
	return pool
}

// runParallel is WIDTH: 2k iid candidates from shared evidence, then k
// independent compositions over the full pool (recursion.run_parallel).
func (m *modeRun) runParallel(ctx context.Context, base []RunRecord, baseTree Tree, baseDir, libraryDir string) {
	type spec struct {
		label, dim string
		index      int
	}
	var specs []spec
	for i := 1; i <= m.k; i++ {
		for _, dim := range []string{"INV", "DOM"} {
			specs = append(specs, spec{fmt.Sprintf("%s%d", strings.ToLower(dim), i), dim, i})
		}
	}
	m.emit(Event{Type: EventPhase, Phase: "probe"})
	stages := make([]*Stage, len(specs))
	parallel(len(specs), m.engine.proposeWidth(), func(i int) {
		s := specs[i]
		stages[i] = m.runStage(ctx, s.label, s.dim, ProposePrompt(m.task.Prompts, s.dim, m.k, s.index, 0), baseTree,
			EvidenceInput{Task: m.task, Rollouts: base, HarnessDir: baseDir, LibraryDir: libraryDir})
	})
	m.record(stages)
	items := make([]rollItem, len(stages))
	labels := make([]string, len(stages))
	for i, stage := range stages {
		items[i] = rollItem{stage.Label, stage.Tree}
		labels[i] = stage.Label
	}
	m.rollMany(ctx, items)

	m.emit(Event{Type: EventPhase, Phase: "compose"})
	pool := m.poolOf(labels)
	decisions := m.decisions()
	aggs := make([]*Stage, m.k)
	parallel(m.k, m.engine.proposeWidth(), func(i int) {
		label := fmt.Sprintf("agg%d", i+1)
		aggs[i] = m.runStage(ctx, label, "AGG", AggregatePrompt(m.task.Prompts, m.k, i+1), baseTree,
			EvidenceInput{Task: m.task, Rollouts: base, HarnessDir: baseDir, LibraryDir: libraryDir, Pool: pool, Decisions: decisions})
	})
	m.record(aggs)
	items = items[:0]
	for _, stage := range aggs {
		items = append(items, rollItem{stage.Label, stage.Tree})
		m.finals = append(m.finals, stage.Label)
	}
	m.rollMany(ctx, items)
}

// runSequential is DEPTH: k nested passes over the same shared base
// evidence, each carrying the current checkpoint (recursion.run_sequential).
func (m *modeRun) runSequential(ctx context.Context, base []RunRecord, baseTree Tree, libraryDir string) error {
	current := baseTree.Clone()
	var history []string
	for t := 1; t <= m.k; t++ {
		currentLabel := "base"
		if t > 1 {
			currentLabel = fmt.Sprintf("c%d", t-1)
		}
		currentDir := m.harnessDir(currentLabel)
		if _, err := WriteTree(current, currentDir, false); err != nil {
			return err
		}
		round := 0
		if m.k > 1 {
			round = t
		}
		m.emit(Event{Type: EventPhase, Phase: "probe", Detail: fmt.Sprintf("round %d", t)})
		specs := [][2]string{{fmt.Sprintf("inv%d", t), "INV"}, {fmt.Sprintf("dom%d", t), "DOM"}}
		pool := m.poolOf(history)
		decisions := m.decisions()
		stages := make([]*Stage, 2)
		parallel(2, m.engine.proposeWidth(), func(i int) {
			stages[i] = m.runStage(ctx, specs[i][0], specs[i][1], ProposePrompt(m.task.Prompts, specs[i][1], m.k, t, round), current,
				EvidenceInput{Task: m.task, Rollouts: base, HarnessDir: currentDir, LibraryDir: libraryDir, Pool: pool, Decisions: decisions})
		})
		m.record(stages)
		m.rollMany(ctx, []rollItem{{stages[0].Label, stages[0].Tree}, {stages[1].Label, stages[1].Tree}})

		roundPool := append(append([]string{}, history...), stages[0].Label, stages[1].Label)
		label := fmt.Sprintf("c%d", t)
		job := ComposeRoundPrompt(m.task.Prompts, t, m.k)
		if m.k == 1 {
			job = AggregatePrompt(m.task.Prompts, 1, 1)
		}
		m.emit(Event{Type: EventPhase, Phase: "compose", Detail: fmt.Sprintf("round %d", t)})
		comp := m.runStage(ctx, label, "COMPOSE", job, current, EvidenceInput{
			Task: m.task, Rollouts: base, HarnessDir: currentDir, LibraryDir: libraryDir,
			Pool: m.poolOf(roundPool), Decisions: m.decisions(),
		})
		m.record([]*Stage{comp})
		m.rollMany(ctx, []rollItem{{label, comp.Tree}})
		history = append(roundPool, label)
		current = comp.Tree
		m.finals = append(m.finals, label)
	}
	return nil
}
