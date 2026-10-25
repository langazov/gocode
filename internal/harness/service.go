package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Service runs Harness² in the background for the HTTP API (and so the TUI):
// it launches runs, keeps each one's live state from the engine's events,
// and lists finished runs from their summaries on disk. Everything gocode-
// specific — booting stacks, the backend, installing — is injected by the
// boot wiring.
type Service struct {
	// Root is the runs root (Engine.Root), read for finished runs.
	Root string
	// NewEngine builds an engine for one launch.
	NewEngine func(ctx context.Context, launch Launch) (*Engine, Detection, error)
	// Publish fans an event out to connected clients; nil drops them.
	Publish func(RunState, Event)
	// Store opens the experience library for the library routes.
	Store func(ctx context.Context) (ExperienceStore, Detection, error)
	// Install puts a finished run's adopted harness where Launch.To says.
	Install func(ctx context.Context, summary *Summary, req InstallRequest) (InstallResult, error)
	// Base supplies the starting harness for a launch that names none.
	Base func() Tree
	// Prune deletes the sessions (and their projects) rooted under a
	// directory — session.DeleteUnder over the server's database.
	Prune func(ctx context.Context, dir string) (int64, error)

	mu     sync.Mutex
	active map[string]*RunState
}

// Launch is one run request.
type Launch struct {
	Task          Task   `json:"task"`
	K             int    `json:"k"`
	Mode          string `json:"mode"`
	Model         string `json:"model,omitempty"`
	ImproverModel string `json:"improverModel,omitempty"`
	Library       string `json:"library,omitempty"`
	CrossDomain   bool   `json:"crossDomain,omitempty"`
	Width         int    `json:"width,omitempty"`
	// Base is the starting harness; nil means the project's installed one.
	Base Tree `json:"-"`
}

// RunID names a run: task, budget and mode, the same triple that names its
// directory, so a finished run's id survives a restart.
func RunID(task string, k int, mode string) string {
	return fmt.Sprintf("%s--k%d-%s", task, k, mode)
}

var runIDRx = regexp.MustCompile(`^(.+)--k(\d+)-(parallel|sequential)$`)

// ParseRunID splits a RunID.
func ParseRunID(id string) (string, int, string, error) {
	m := runIDRx.FindStringSubmatch(id)
	if m == nil {
		return "", 0, "", fmt.Errorf("malformed run id %q", id)
	}
	k, _ := strconv.Atoi(m[2])
	return m[1], k, m[3], nil
}

// Row states for RunState.Rows.
const (
	RowPending = "pending"
	RowEditing = "editing"
	RowEdited  = "edited"
	RowRolling = "rolling"
	RowOK      = "ok"
	RowFailed  = "failed"
)

// Row is one label's progress: an improver stage and/or its rollout.
type Row struct {
	Label   string  `json:"label"`
	Phase   string  `json:"phase"` // base, probe, compose, select, reflect
	State   string  `json:"state"`
	CostUSD float64 `json:"costUSD"`
	Detail  string  `json:"detail,omitempty"`
	// SessionID is the label's rollout session; EditSessionID its latest
	// improver session (editor, selector or reflector). Either can be opened
	// while it runs to watch it live.
	SessionID     string `json:"sessionID,omitempty"`
	EditSessionID string `json:"editSessionID,omitempty"`
	// Retries counts rejected proposals for the label.
	Retries int `json:"retries,omitempty"`
}

// RunState is a run as clients see it, live or finished.
type RunState struct {
	ID        string    `json:"id"`
	Task      string    `json:"task"`
	Domain    string    `json:"domain"`
	K         int       `json:"k"`
	Mode      string    `json:"mode"`
	Status    string    `json:"status"` // running, ok, partial, base-failed, failed, cancelled
	Phase     string    `json:"phase,omitempty"`
	Backend   string    `json:"backend,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Rows      []Row     `json:"rows"`
	CostUSD   float64   `json:"costUSD"`
	StartedAt time.Time `json:"startedAt"`
	Adopted   string    `json:"adopted,omitempty"`
	Why       string    `json:"why,omitempty"`
	Lesson    string    `json:"lesson,omitempty"`
	Error     string    `json:"error,omitempty"`
	Budget    string    `json:"budget"`

	cancel context.CancelFunc
}

// PlannedRows lists every label a run will produce, in display order, so a
// client can show the whole plan before it happens.
func PlannedRows(mode string, k int, library bool) []Row {
	var rows []Row
	add := func(label, phase string) { rows = append(rows, Row{Label: label, Phase: phase, State: RowPending}) }
	for i := 1; i <= k; i++ {
		add(fmt.Sprintf("base%d", i), "base")
	}
	if mode == ModeParallel {
		for i := 1; i <= k; i++ {
			add(fmt.Sprintf("inv%d", i), "probe")
			add(fmt.Sprintf("dom%d", i), "probe")
		}
		for i := 1; i <= k; i++ {
			add(fmt.Sprintf("agg%d", i), "compose")
		}
	} else {
		for t := 1; t <= k; t++ {
			add(fmt.Sprintf("inv%d", t), "probe")
			add(fmt.Sprintf("dom%d", t), "probe")
			add(fmt.Sprintf("c%d", t), "compose")
		}
	}
	if k > 1 {
		add("select", "select")
	}
	if library {
		add("experience", "reflect")
	}
	return rows
}

func (r *RunState) row(label string) *Row {
	for i := range r.Rows {
		if r.Rows[i].Label == label {
			return &r.Rows[i]
		}
	}
	r.Rows = append(r.Rows, Row{Label: label, State: RowPending})
	return &r.Rows[len(r.Rows)-1]
}

// apply folds one engine event into the state.
func (r *RunState) apply(ev Event) {
	switch ev.Type {
	case EventPhase:
		r.Phase = ev.Phase
	case EventStageStarted:
		r.row(ev.Label).State = RowEditing
	case EventStageEnded:
		row := r.row(ev.Label)
		row.CostUSD += ev.CostUSD
		row.Detail = ev.Detail
		switch {
		case ev.Label == "select" || ev.Label == "experience":
			row.State = map[bool]string{true: RowOK, false: RowFailed}[ev.OK]
		default:
			row.State = RowEdited
		}
	case EventRolloutStarted:
		r.row(ev.Label).State = RowRolling
	case EventRolloutEnded:
		row := r.row(ev.Label)
		row.CostUSD += ev.CostUSD
		if ev.Detail != "" {
			row.Detail = ev.Detail
		}
		row.State = map[bool]string{true: RowOK, false: RowFailed}[ev.OK]
	case EventSelected:
		r.Adopted = ev.Label
		r.Why = ev.Detail
	case EventSession:
		row := r.row(ev.Label)
		if ev.Role == "rollout" {
			row.SessionID = ev.SessionID
		} else {
			row.EditSessionID = ev.SessionID
		}
	case EventStageRetry:
		row := r.row(ev.Label)
		row.Retries++
		row.Detail = fmt.Sprintf("retry %d: %s", ev.Attempt, ev.Detail)
	case EventReflected:
		row := r.row("experience")
		row.State = map[bool]string{true: RowOK, false: RowFailed}[ev.OK]
		row.Detail = ev.Detail
		if ev.OK {
			r.Lesson = ev.Detail
		}
	}
	r.CostUSD += ev.CostUSD
}

func (r *RunState) snapshot() RunState {
	out := *r
	out.Rows = append([]Row(nil), r.Rows...)
	out.cancel = nil
	return out
}

// ErrRunning is returned when a run with the same id is already going.
var ErrRunning = errors.New("this run is already in progress")

// Start launches a run in the background and returns its initial state.
func (s *Service) Start(ctx context.Context, launch Launch) (RunState, error) {
	if s.NewEngine == nil {
		return RunState{}, errors.New("harness runs are not available on this server")
	}
	mode, err := ParseMode(launch.Mode)
	if err != nil {
		return RunState{}, err
	}
	if launch.K < 1 {
		launch.K = 1
	}
	launch.Mode = mode
	if launch.Base == nil && s.Base != nil {
		launch.Base = s.Base()
	}
	launch.Task.Prompts = launch.Task.Prompts.WithDefaults()
	if err := launch.Task.Validate(); err != nil {
		return RunState{}, err
	}
	id := RunID(launch.Task.ID, launch.K, mode)

	s.mu.Lock()
	if s.active == nil {
		s.active = map[string]*RunState{}
	}
	if current, ok := s.active[id]; ok && current.Status == "running" {
		s.mu.Unlock()
		return RunState{}, ErrRunning
	}
	s.mu.Unlock()

	engine, detection, err := s.NewEngine(ctx, launch)
	if err != nil {
		return RunState{}, err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	state := &RunState{
		ID: id, Task: launch.Task.ID, Domain: launch.Task.Domain, K: launch.K, Mode: mode,
		Status: "running", Backend: detection.Backend, Reason: detection.Reason,
		Rows: PlannedRows(mode, launch.K, engine.Store != nil), StartedAt: time.Now().UTC(),
		Budget: CostLine(mode, launch.K, engine.Store != nil), cancel: cancel,
	}
	s.mu.Lock()
	s.active[id] = state
	s.mu.Unlock()

	engine.OnEvent = func(ev Event) {
		s.mu.Lock()
		state.apply(ev)
		snap := state.snapshot()
		s.mu.Unlock()
		if s.Publish != nil {
			s.Publish(snap, ev)
		}
	}
	go func() {
		summary, err := engine.Run(runCtx, launch.Task, RunOptions{K: launch.K, Mode: mode, Base: launch.Base})
		s.mu.Lock()
		switch {
		case errors.Is(err, context.Canceled):
			state.Status = "cancelled"
		case err != nil:
			state.Status, state.Error = "failed", err.Error()
		case summary != nil:
			state.Status = summary.Status
			if len(summary.Errors) > 0 {
				state.Error = summary.Errors[0]
			}
		}
		if summary != nil {
			state.CostUSD = summary.CostUSD
		}
		snap := state.snapshot()
		// A finished run is served from its summary on disk from here on;
		// keeping every run's live state for the life of the server would
		// grow without bound. One that died before writing a summary stays,
		// so its error can still be read.
		if _, err := os.Stat(s.summaryPath(snap.Task, snap.K, snap.Mode)); err == nil {
			delete(s.active, id)
		}
		s.mu.Unlock()
		if s.Publish != nil {
			s.Publish(snap, Event{Type: "run.ended", Task: snap.Task, Mode: snap.Mode, OK: snap.Status == "ok", Detail: snap.Error})
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	return state.snapshot(), nil
}

// Cancel stops a running run; completed work stays cached for a resume.
func (s *Service) Cancel(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.active[id]
	if !ok || state.Status != "running" {
		return false
	}
	state.cancel()
	return true
}

// Get returns a run, live or finished.
func (s *Service) Get(id string) (RunState, error) {
	s.mu.Lock()
	if state, ok := s.active[id]; ok {
		defer s.mu.Unlock()
		return state.snapshot(), nil
	}
	s.mu.Unlock()
	task, k, mode, err := ParseRunID(id)
	if err != nil {
		return RunState{}, err
	}
	summary, err := LoadSummary(s.summaryPath(task, k, mode))
	if err != nil {
		return RunState{}, fmt.Errorf("no run %s", id)
	}
	state := FromSummary(summary)
	// The summary keeps rollout sessions; the log also has the improver
	// sessions and retries, so a finished run opens to the same detail a
	// live one shows.
	if events, err := ReadLog(s.logPath(task, k, mode), 0); err == nil {
		for _, ev := range events {
			switch ev.Type {
			case EventSession:
				row := state.row(ev.Label)
				if ev.Role == "rollout" {
					row.SessionID = ev.SessionID
				} else {
					row.EditSessionID = ev.SessionID
				}
			case EventStageRetry:
				state.row(ev.Label).Retries++
			}
		}
	}
	return state, nil
}

func (s *Service) logPath(task string, k int, mode string) string {
	return filepath.Join((&Engine{Root: s.Root}).Dir(task, k), mode, LogFile)
}

// Log returns a run's event log after the first `after` events, so a
// follower polls for only what is new.
func (s *Service) Log(id string, after int) ([]Event, error) {
	task, k, mode, err := ParseRunID(id)
	if err != nil {
		return nil, err
	}
	events, err := ReadLog(s.logPath(task, k, mode), after)
	if os.IsNotExist(err) {
		return []Event{}, nil
	}
	return events, err
}

func (s *Service) summaryPath(task string, k int, mode string) string {
	return (&Engine{Root: s.Root}).SummaryPath(task, k, mode)
}

// Summary returns a finished run's summary.
func (s *Service) Summary(id string) (*Summary, error) {
	task, k, mode, err := ParseRunID(id)
	if err != nil {
		return nil, err
	}
	return LoadSummary(s.summaryPath(task, k, mode))
}

// List returns live runs and every finished one on disk, newest first.
func (s *Service) List() []RunState {
	seen := map[string]bool{}
	var out []RunState
	s.mu.Lock()
	for id, state := range s.active {
		seen[id] = true
		out = append(out, state.snapshot())
	}
	s.mu.Unlock()
	paths, _ := filepath.Glob(filepath.Join(s.Root, "*", "k*", "*", "summary.json"))
	for _, path := range paths {
		summary, err := LoadSummary(path)
		if err != nil {
			continue
		}
		id := RunID(summary.Task, summary.K, summary.Mode)
		if !seen[id] {
			seen[id] = true
			out = append(out, FromSummary(summary))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

// FromSummary renders a finished run as a RunState.
func FromSummary(s *Summary) RunState {
	state := RunState{
		ID: RunID(s.Task, s.K, s.Mode), Task: s.Task, Domain: s.Domain, K: s.K, Mode: s.Mode,
		Status: s.Status, Backend: s.Backend, CostUSD: s.CostUSD, StartedAt: s.StartedAt,
		Rows: PlannedRows(s.Mode, s.K, s.Backend != "none"), Budget: CostLine(s.Mode, s.K, s.Backend != "none"),
	}
	for label, run := range s.Rollouts {
		row := state.row(label)
		row.State = map[bool]string{true: RowOK, false: RowFailed}[run.OK && run.Delivered]
		row.CostUSD += run.CostUSD
		row.Detail = run.Error
		row.SessionID = run.SessionID
	}
	for _, stage := range s.Stages {
		row := state.row(stage.Label)
		row.CostUSD += stage.CostUSD
		if stage.FellBack {
			row.Detail = "fell back: " + stage.Error
		} else if stage.KeptBase {
			row.Detail = "kept base"
		}
	}
	if s.Selection != nil {
		state.Adopted, state.Why = s.Selection.ChosenLabel, s.Selection.Why
		if s.K > 1 {
			row := state.row("select")
			row.State = map[bool]string{true: RowOK, false: RowFailed}[s.Selection.OK && !s.Selection.Fallback]
			row.CostUSD = s.Selection.CostUSD
		}
	}
	if s.Experience != nil {
		state.Lesson = s.Experience.Cue
		state.row("experience").State = RowOK
	} else if s.Backend != "none" && s.Selection != nil && s.Selection.OK {
		state.row("experience").State = RowFailed
	}
	if len(s.Errors) > 0 {
		state.Error = s.Errors[0]
	}
	return state
}

// StageDetail is one label's harness and rollout, for the stage view.
type StageDetail struct {
	Label       string  `json:"label"`
	Diff        string  `json:"diff"`
	Files       Diff    `json:"files"`
	Notes       string  `json:"notes,omitempty"`
	Error       string  `json:"error,omitempty"`
	SessionID   string  `json:"sessionID,omitempty"`
	OK          bool    `json:"ok"`
	Delivered   bool    `json:"delivered"`
	Deliverable string  `json:"deliverable,omitempty"`
	CostUSD     float64 `json:"costUSD"`
}

// Stage reads one label's evidence from disk: its tree against the base, its
// notes and its rollout.
func (s *Service) Stage(id, label string) (StageDetail, error) {
	task, k, mode, err := ParseRunID(id)
	if err != nil {
		return StageDetail{}, err
	}
	if _, err := NormalizeRel(label); err != nil || label == "" {
		return StageDetail{}, fmt.Errorf("bad label %q", label)
	}
	dir := (&Engine{Root: s.Root}).Dir(task, k)
	detail := StageDetail{Label: label}
	base, _ := ReadTree(filepath.Join(dir, "base", "harness"), nil)
	harnessDir := filepath.Join(dir, mode, "harnesses", label)
	runDir := filepath.Join(dir, mode, "runs", label)
	if len(label) > 4 && label[:4] == "base" {
		harnessDir = filepath.Join(dir, "base", "harness")
		runDir = filepath.Join(dir, "base", "runs", label)
	}
	tree, _ := ReadTree(harnessDir, nil)
	detail.Files = DiffTrees(base, tree)
	detail.Diff = TreeDiff(base, tree, "base", label)
	if summary, err := LoadSummary(s.summaryPath(task, k, mode)); err == nil {
		for _, stage := range summary.Stages {
			if stage.Label == label {
				detail.Notes, detail.Error, detail.CostUSD = stage.Notes, stage.Error, stage.CostUSD
			}
		}
	}
	var run Rollout
	if err := readJSON(filepath.Join(runDir, runJSON), &run); err == nil {
		detail.SessionID, detail.OK, detail.Delivered = run.SessionID, run.OK, run.Delivered
		detail.CostUSD += run.CostUSD
		if detail.Error == "" {
			detail.Error = run.Error
		}
		detail.Deliverable = RunRecord{Rollout: run, Dir: runDir}.Deliverable()
	}
	return detail, nil
}

// InstallRequest says where a run's adopted harness goes.
type InstallRequest struct {
	To    string `json:"to"` // project, library, both
	Force bool   `json:"force"`
	// DryRun reports what would change without writing anything.
	DryRun bool `json:"dryRun"`
}

// InstallResult reports what an install did, or would do.
type InstallResult struct {
	Target     string         `json:"target,omitempty"`
	Files      Diff           `json:"files"`
	Conflict   bool           `json:"conflict"`
	Collisions []string       `json:"collisions,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
	Library    *PublishReport `json:"library,omitempty"`
	Written    bool           `json:"written"`
}

// Deleted reports what Delete removed.
type Deleted struct {
	Sessions int64    `json:"sessions"`
	Removed  []string `json:"removed"`
}

// ErrStillRunning is returned when deleting a run that has not stopped.
var ErrStillRunning = errors.New("the run is still going; cancel it first")

// Delete removes a run: its mode directory and every session under it. When
// no other mode of the same task and k remains, the shared base rollouts and
// their sessions go too. The experience library is not touched — a stored
// lesson outlives the run that taught it.
func (s *Service) Delete(ctx context.Context, id string) (Deleted, error) {
	task, k, mode, err := ParseRunID(id)
	if err != nil {
		return Deleted{}, err
	}
	s.mu.Lock()
	if state, ok := s.active[id]; ok && state.Status == "running" {
		s.mu.Unlock()
		return Deleted{}, ErrStillRunning
	}
	delete(s.active, id)
	s.mu.Unlock()
	engine := &Engine{Root: s.Root}
	kdir := engine.Dir(task, k)
	targets := []string{filepath.Join(kdir, mode)}
	other := ModeSequential
	if mode == ModeSequential {
		other = ModeParallel
	}
	if _, err := os.Stat(engine.SummaryPath(task, k, other)); err != nil {
		targets = []string{kdir}
	}
	var out Deleted
	for _, dir := range targets {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if s.Prune != nil {
			n, err := s.Prune(ctx, dir)
			out.Sessions += n
			if err != nil {
				return out, err
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			return out, err
		}
		out.Removed = append(out.Removed, dir)
	}
	if len(out.Removed) == 0 {
		return out, fmt.Errorf("no run %s", id)
	}
	// An empty task directory is noise in the listing.
	if entries, err := os.ReadDir(filepath.Join(s.Root, task)); err == nil && len(entries) == 0 {
		_ = os.Remove(filepath.Join(s.Root, task))
	}
	return out, nil
}
