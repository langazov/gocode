package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Engine runs Harness² for one task at a time, porting the per-task driver
// in harness2/core/schedule.py (run_task / _run_one_mode): k shared base
// executions, one recursion mode, blind selection over its finals, then —
// when an experience store is configured — reflection and a library entry.
type Engine struct {
	Exec     Executor
	Improver Improver
	// Store is the experience library; nil runs without one (no library
	// evidence, no reflection, nothing stored).
	Store ExperienceStore
	// Root holds the run directories (<Root>/<task>/k<k>/...).
	Root string
	// RolloutWidth and ProposeWidth bound concurrent rollouts and improver
	// sessions; zero means DefaultWidth.
	RolloutWidth int
	ProposeWidth int
	// Validate tunes tree validation.
	Validate ValidateOptions
	// KeepWorkspaces keeps every rollout's working copy (normally removed
	// once its change has been rendered).
	KeepWorkspaces bool
	// OnEvent receives progress; it may be called concurrently.
	OnEvent func(Event)
	// Environment, when set, snapshots what the rollouts can see beyond the
	// harness (the Library skills library-plugin advertises), at the start
	// and the end of a run, so drift between rollouts is recorded.
	Environment func(ctx context.Context) (map[string]string, error)
	// Extra, when set, supplies read-only evidence directories every editor
	// stage is shown (Library skills matching the task, related lessons).
	Extra func(ctx context.Context, task Task) ([]ExtraEvidence, error)

	eventMu sync.Mutex
}

// DefaultWidth bounds concurrency when no width is set.
const DefaultWidth = 2

func (e *Engine) rolloutWidth() int {
	if e.RolloutWidth > 0 {
		return e.RolloutWidth
	}
	return DefaultWidth
}

func (e *Engine) proposeWidth() int {
	if e.ProposeWidth > 0 {
		return e.ProposeWidth
	}
	return DefaultWidth
}

func (e *Engine) emit(ev Event) {
	if e.OnEvent == nil {
		return
	}
	e.eventMu.Lock()
	defer e.eventMu.Unlock()
	e.OnEvent(ev)
}

// LogFile is the per-run event log, beside summary.json: one JSON event per
// line, every event the run emitted, in order. It is how a run — from the
// CLI or a server — can be followed and audited at the lowest level after
// the fact.
const LogFile = "events.jsonl"

// runLog stamps and appends one run's events to its log, then forwards them
// to the engine's OnEvent.
type runLog struct {
	engine     *Engine
	task, mode string
	mu         sync.Mutex
	file       *os.File
}

func (e *Engine) openLog(path, task, mode string) *runLog {
	l := &runLog{engine: e, task: task, mode: mode}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		l.file = f
	}
	return l
}

func (l *runLog) emit(ev Event) {
	ev.Task, ev.Mode = l.task, l.mode
	if ev.Time == 0 {
		ev.Time = time.Now().UnixMilli()
	}
	if l.file != nil {
		if data, err := json.Marshal(ev); err == nil {
			l.mu.Lock()
			_, _ = l.file.Write(append(data, '\n'))
			l.mu.Unlock()
		}
	}
	l.engine.emit(ev)
}

func (l *runLog) close() {
	if l.file != nil {
		_ = l.file.Close()
	}
}

// ReadLog reads a run's event log; after skips that many events, so a
// follower can ask only for what is new.
func ReadLog(path string, after int) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Event
	n := 0
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		n++
		if n <= after {
			continue
		}
		var ev Event
		if json.Unmarshal(line, &ev) == nil {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (e *Engine) session(ctx context.Context, req SessionRequest) (Reply, error) {
	if e.Improver == nil {
		return Reply{}, errors.New("no improver configured")
	}
	return e.Improver.Session(ctx, req)
}

// RunOptions selects the recursion budget and mode.
type RunOptions struct {
	K    int
	Mode string
	// Base is the harness the task starts from; an empty tree is gocode as
	// it ships.
	Base Tree
}

// Summary is the durable record of one task run (summary.json).
type Summary struct {
	Task          string             `json:"task"`
	Domain        string             `json:"domain"`
	K             int                `json:"k"`
	Mode          string             `json:"mode"`
	PromptVersion string             `json:"promptVersion"`
	Backend       string             `json:"backend"`
	Status        string             `json:"status"` // ok, partial, base-failed, failed
	StartedAt     time.Time          `json:"startedAt"`
	FinishedAt    time.Time          `json:"finishedAt"`
	Base          []Rollout          `json:"base"`
	Stages        []*Stage           `json:"stages"`
	Rollouts      map[string]Rollout `json:"rollouts"`
	Finals        []string           `json:"finals"`
	Selection     *Selection         `json:"selection,omitempty"`
	Experience    *Experience        `json:"experience,omitempty"`
	Stored        bool               `json:"stored"`
	StoreError    string             `json:"storeError,omitempty"`
	Similarity    *Similarity        `json:"similarity,omitempty"`
	Ledger        *Ledger            `json:"ledger"`
	CostUSD       float64            `json:"costUSD"`
	Adopted       string             `json:"adopted,omitempty"` // directory of the adopted tree
	Diffs         map[string]Diff    `json:"diffs,omitempty"`
	// SkillsAtStart and SkillsAtEnd are the Environment snapshots;
	// SkillsDrifted reports that they differ, so this run's rollouts did not
	// all see the same skills.
	SkillsAtStart map[string]string `json:"skillsAtStart,omitempty"`
	SkillsAtEnd   map[string]string `json:"skillsAtEnd,omitempty"`
	SkillsDrifted bool              `json:"skillsDrifted,omitempty"`
	Errors        []string          `json:"errors,omitempty"`
}

// Dir returns a task's run directory for k.
func (e *Engine) Dir(taskID string, k int) string {
	return filepath.Join(e.Root, taskID, fmt.Sprintf("k%d", k))
}

// SummaryPath is where Run writes the summary for a task, k and mode.
func (e *Engine) SummaryPath(taskID string, k int, mode string) string {
	return filepath.Join(e.Dir(taskID, k), mode, "summary.json")
}

// LoadSummary reads a finished or partial run's summary.
func LoadSummary(path string) (*Summary, error) {
	var s Summary
	if err := readJSON(path, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Run adapts the harness for one task. Re-running the same task, k and mode
// resumes: rollouts and improver replies already on disk are reused, and a
// summary already marked ok is returned as is.
func (e *Engine) Run(ctx context.Context, task Task, opts RunOptions) (*Summary, error) {
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if opts.K < 1 {
		return nil, fmt.Errorf("k must be positive, got %d", opts.K)
	}
	mode, err := ParseMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	task.Prompts = task.Prompts.WithDefaults()
	k := opts.K
	path := e.SummaryPath(task.ID, k, mode)
	if prior, err := LoadSummary(path); err == nil && prior.Status == "ok" {
		return prior, nil
	}
	backend := "none"
	if e.Store != nil {
		backend = e.Store.Backend()
	}
	summary := &Summary{
		Task: task.ID, Domain: task.Domain, K: k, Mode: mode, PromptVersion: PromptVersion,
		Backend: backend, StartedAt: time.Now().UTC(), Rollouts: map[string]Rollout{},
	}
	dir := e.Dir(task.ID, k)
	if err := os.MkdirAll(filepath.Join(dir, mode), 0o755); err != nil {
		return nil, err
	}
	_ = writeJSON(filepath.Join(dir, "task.json"), task)

	baseTree := opts.Base.Clone()
	if baseTree == nil {
		baseTree = Tree{}
	}
	baseDir := filepath.Join(dir, "base", "harness")
	if _, err := WriteTree(baseTree, baseDir, true); err != nil {
		return nil, err
	}
	// The stubs are for editors to read; reload so the base tree compares
	// equal to what an editor gets back untouched.
	baseTree, _ = ReadTree(baseDir, nil)

	ledger := &Ledger{}
	summary.Ledger = ledger
	log := e.openLog(filepath.Join(dir, mode, LogFile), task.ID, mode)
	defer log.close()
	m := &modeRun{
		engine: e, task: task, k: k, mode: mode, dir: filepath.Join(dir, mode), ledger: ledger, log: log,
		trees: map[string]Tree{"base": baseTree}, rollouts: map[string]RunRecord{},
	}

	if e.Environment != nil {
		if snap, err := e.Environment(ctx); err == nil {
			summary.SkillsAtStart = snap
		} else {
			summary.Errors = append(summary.Errors, "environment snapshot: "+err.Error())
		}
	}
	if e.Extra != nil {
		extra, err := e.Extra(ctx, task)
		if err != nil {
			summary.Errors = append(summary.Errors, "extra evidence: "+err.Error())
		}
		m.extra = extra
	}

	log.emit(Event{Type: EventPhase, Phase: "base"})
	base := make([]RunRecord, k)
	parallel(k, e.rolloutWidth(), func(i int) {
		label := fmt.Sprintf("base%d", i+1)
		// Rollouts materialise their own copy, without the editor stubs: the
		// base evidence directory must stay byte-identical across a resume,
		// or every cached improver reply would miss.
		base[i] = e.roll(ctx, task, label, baseTree, filepath.Join(dir, "base", "harnesses", label), filepath.Join(dir, "base", "runs", label), log.emit)
	})
	anyOK := false
	for _, run := range base {
		ledger.rollout(run.Label, run.CostUSD)
		m.rollouts[run.Label] = run
		summary.Base = append(summary.Base, run.Rollout)
		anyOK = anyOK || run.OK
	}
	if err := ctx.Err(); err != nil {
		return e.finish(path, summary, m, "failed", err)
	}
	if !anyOK {
		summary.Errors = append(summary.Errors, "no base rollout produced anything")
		return e.finish(path, summary, m, "base-failed", nil)
	}

	libraryDir := ""
	if e.Store != nil {
		if libraryDir, err = e.Store.ContextDir(ctx, task.Domain); err != nil {
			summary.Errors = append(summary.Errors, "experience library unavailable: "+err.Error())
			libraryDir = ""
		}
	}

	if mode == ModeParallel {
		m.runParallel(ctx, base, baseTree, baseDir, libraryDir)
	} else if err := m.runSequential(ctx, base, baseTree, libraryDir); err != nil {
		return e.finish(path, summary, m, "failed", err)
	}
	if err := ctx.Err(); err != nil {
		return e.finish(path, summary, m, "failed", err)
	}
	if err := ledger.assertIdentity(k); err != nil {
		summary.Errors = append(summary.Errors, err.Error())
	}
	finals := map[string]Tree{}
	for _, label := range m.finals {
		finals[label] = m.trees[label]
	}
	sim := Diversity(finals)
	summary.Similarity = &sim

	log.emit(Event{Type: EventPhase, Phase: "select"})
	sel := m.selectFinal(ctx)
	summary.Selection = sel
	log.emit(Event{Type: EventSelected, Phase: "select", Label: sel.ChosenLabel, OK: sel.OK,
		Detail: firstNonEmpty(sel.Error, sel.Why)})
	if !sel.OK {
		summary.Errors = append(summary.Errors, "selection failed: "+sel.Error)
		return e.finish(path, summary, m, "partial", nil)
	}
	summary.Adopted = m.harnessDir(sel.ChosenLabel)

	if e.Store != nil {
		log.emit(Event{Type: EventPhase, Phase: "reflect"})
		exp := m.reflect(ctx, sel, baseDir)
		summary.Experience = exp
		if exp != nil {
			err := e.Store.Add(ctx, Entry{
				Domain: task.Domain, Task: task.ID, Label: sel.ChosenLabel, Why: sel.Why,
				Lesson: *exp, Tree: m.trees[sel.ChosenLabel],
			})
			summary.Stored = err == nil
			if err != nil {
				summary.StoreError = err.Error()
			}
		}
	}
	return e.finish(path, summary, m, "ok", nil)
}

func (e *Engine) finish(path string, summary *Summary, m *modeRun, status string, cause error) (*Summary, error) {
	if e.Environment != nil && summary.SkillsAtStart != nil {
		if snap, err := e.Environment(context.Background()); err == nil {
			summary.SkillsAtEnd = snap
			summary.SkillsDrifted = !sameMap(summary.SkillsAtStart, snap)
		}
	}
	summary.Status = status
	summary.FinishedAt = time.Now().UTC()
	summary.Stages = m.stages
	summary.Finals = m.finals
	summary.Diffs = map[string]Diff{}
	base := m.trees["base"]
	labels := make([]string, 0, len(m.rollouts))
	for label := range m.rollouts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		summary.Rollouts[label] = m.rollouts[label].Rollout
	}
	for label, tree := range m.trees {
		if label != "base" {
			summary.Diffs[label] = DiffTrees(base, tree)
		}
	}
	summary.CostUSD = m.ledger.CostUSD
	if cause != nil {
		summary.Errors = append(summary.Errors, cause.Error())
	}
	if err := writeJSON(path, summary); err != nil {
		return summary, err
	}
	if cause != nil {
		return summary, cause
	}
	return summary, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// AdoptedTree reads the adopted harness of a finished run.
func (s *Summary) AdoptedTree() (Tree, error) {
	if s.Adopted == "" {
		return nil, fmt.Errorf("run %s has no adopted harness (status %s)", s.Task, s.Status)
	}
	return ReadTree(s.Adopted, nil)
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
