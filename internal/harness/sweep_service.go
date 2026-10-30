package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Sweeps through the Service: the same Engine.Sweep `gocode harness sweep`
// runs, launched in the background for the HTTP API. Each task of a running
// sweep is also a live run in the runs list (s.active), with its rows,
// sessions and log, so every run view — watch, log, stall warnings — works on
// a sweep's tasks unchanged. A sweep is the unit of cancelling: stopping one
// of its tasks stops the sweep after the tasks in flight, the way the CLI's
// Ctrl-C does.

// SweepLaunch is one sweep request: tasks already loaded (LoadTasks).
type SweepLaunch struct {
	Tasks         []Task
	Source        string
	K             int
	Mode          string
	Workers       int
	MaxTasks      int
	Seed          *int64
	Model         string
	ImproverModel string
	Library       string
	Base          Tree
}

// SweepTask is one task of a sweep as clients see it.
type SweepTask struct {
	Task    string  `json:"task"`
	Domain  string  `json:"domain"`
	RunID   string  `json:"runID"`
	Status  string  `json:"status"` // pending, running, then the run's own status
	Adopted string  `json:"adopted,omitempty"`
	Lesson  string  `json:"lesson,omitempty"`
	Error   string  `json:"error,omitempty"`
	CostUSD float64 `json:"costUSD"`
}

// SweepState is a sweep, planned, live or finished.
type SweepState struct {
	ID          string      `json:"id"`
	Status      string      `json:"status"` // planned, running, ok, partial, cancelled, failed
	Source      string      `json:"source,omitempty"`
	K           int         `json:"k"`
	Mode        string      `json:"mode"`
	Workers     int         `json:"workers"`
	DomainOrder []string    `json:"domainOrder"`
	Tasks       []SweepTask `json:"tasks"`
	Done        int         `json:"done"`
	Failed      int         `json:"failed"`
	CostUSD     float64     `json:"costUSD"`
	// Budget is one task's cost line; Rollouts and Edits the sweep's totals.
	Budget     string    `json:"budget"`
	Rollouts   int       `json:"rollouts"`
	Edits      int       `json:"edits"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Error      string    `json:"error,omitempty"`

	cancel context.CancelFunc
}

func (s *SweepState) snapshot() SweepState {
	out := *s
	out.Tasks = append([]SweepTask(nil), s.Tasks...)
	out.DomainOrder = append([]string(nil), s.DomainOrder...)
	out.cancel = nil
	return out
}

func (s *SweepState) task(id string) *SweepTask {
	for i := range s.Tasks {
		if s.Tasks[i].Task == id {
			return &s.Tasks[i]
		}
	}
	return nil
}

// ErrSweepRunning refuses a second sweep while one runs: tasks within a
// domain read each other's lessons, and two sweeps interleaving them would
// make neither's order mean anything.
var ErrSweepRunning = errors.New("a sweep is already running")

// PlanSweep orders a sweep's tasks without running anything: the dry run a
// client confirms before spending.
func (s *Service) PlanSweep(launch SweepLaunch) (SweepState, error) {
	if err := ValidateTasks(launch.Tasks); err != nil {
		return SweepState{}, err
	}
	mode, err := ParseMode(launch.Mode)
	if err != nil {
		return SweepState{}, err
	}
	k := max(launch.K, 1)
	workers := max(launch.Workers, 1)
	domains, byDomain := Stream(launch.Tasks, launch.Seed, launch.MaxTasks)
	state := SweepState{
		Status: "planned", Source: launch.Source, K: k, Mode: mode, Workers: workers,
		DomainOrder: domains, Budget: CostLine(mode, k, true),
	}
	for _, domain := range domains {
		for _, task := range byDomain[domain] {
			state.Tasks = append(state.Tasks, SweepTask{
				Task: task.ID, Domain: domain, RunID: RunID(task.ID, k, mode), Status: "pending",
			})
		}
	}
	state.Rollouts = len(state.Tasks) * ExpectedRollouts(k)
	state.Edits = len(state.Tasks) * ExpectedSessions(k)
	return state, nil
}

// StartSweep plans the sweep and runs it in the background.
func (s *Service) StartSweep(ctx context.Context, launch SweepLaunch) (SweepState, error) {
	if s.NewEngine == nil {
		return SweepState{}, errors.New("harness runs are not available on this server")
	}
	plan, err := s.PlanSweep(launch)
	if err != nil {
		return SweepState{}, err
	}
	if launch.Base == nil && s.Base != nil {
		launch.Base = s.Base()
	}
	s.mu.Lock()
	for _, sweep := range s.sweeps {
		if sweep.Status == "running" {
			s.mu.Unlock()
			return SweepState{}, ErrSweepRunning
		}
	}
	for _, task := range plan.Tasks {
		if run, ok := s.active[task.RunID]; ok && run.Status == "running" {
			s.mu.Unlock()
			return SweepState{}, fmt.Errorf("%s: %w", task.RunID, ErrRunning)
		}
	}
	s.mu.Unlock()

	engine, detection, err := s.NewEngine(ctx, Launch{
		Task: launch.Tasks[0], K: plan.K, Mode: plan.Mode, Model: launch.Model,
		ImproverModel: launch.ImproverModel, Library: launch.Library, Base: launch.Base,
	})
	if err != nil {
		return SweepState{}, err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	sweep := &plan
	sweep.Status, sweep.StartedAt, sweep.cancel = "running", time.Now().UTC(), cancel
	sweep.ID = sweep.StartedAt.Format("20060102T150405Z")
	tasks := map[string]Task{}
	for _, task := range launch.Tasks {
		tasks[task.ID] = task
	}
	library := engine.Store != nil

	s.mu.Lock()
	if s.sweeps == nil {
		s.sweeps = map[string]*SweepState{}
	}
	if s.active == nil {
		s.active = map[string]*RunState{}
	}
	s.sweeps[sweep.ID] = sweep
	s.mu.Unlock()

	engine.OnEvent = func(ev Event) {
		s.mu.Lock()
		id := RunID(ev.Task, sweep.K, sweep.Mode)
		state, ok := s.active[id]
		if !ok {
			task := tasks[ev.Task]
			state = &RunState{
				ID: id, Task: task.ID, Domain: task.Domain, K: sweep.K, Mode: sweep.Mode, Status: "running",
				Backend: detection.Backend, Reason: detection.Reason, Rows: PlannedRows(sweep.Mode, sweep.K, library),
				StartedAt: time.Now().UTC(), Budget: CostLine(sweep.Mode, sweep.K, library),
				Workspace: task.Workspace, Prompt: clipPrompt(task.Prompt), cancel: cancel,
			}
			s.active[id] = state
		}
		state.apply(ev)
		snap := state.snapshot()
		changed := false
		if t := sweep.task(ev.Task); t != nil && t.Status == "pending" {
			t.Status, changed = "running", true
		}
		sweepSnap := sweep.snapshot()
		s.mu.Unlock()
		if s.Publish != nil {
			s.Publish(snap, ev)
		}
		if changed && s.PublishSweep != nil {
			s.PublishSweep(sweepSnap)
		}
	}

	go func() {
		result, err := engine.Sweep(runCtx, launch.Tasks, SweepOptions{
			K: sweep.K, Mode: sweep.Mode, Workers: sweep.Workers, MaxTasks: launch.MaxTasks, Seed: launch.Seed,
			Base: launch.Base, Dir: s.SweepsDir, Name: sweep.ID, Source: sweep.Source,
			OnTask: func(row SweepRow) { s.sweepTaskEnded(sweep, row) },
		})
		s.mu.Lock()
		sweep.FinishedAt = time.Now().UTC()
		switch {
		case errors.Is(err, context.Canceled):
			sweep.Status = "cancelled"
		case err != nil && result == nil:
			sweep.Status, sweep.Error = "failed", err.Error()
		case sweep.Failed > 0:
			sweep.Status = "partial"
		default:
			sweep.Status = "ok"
		}
		if err != nil && sweep.Error == "" && !errors.Is(err, context.Canceled) {
			sweep.Error = err.Error()
		}
		snap := sweep.snapshot()
		// Served from its record from here on, like a finished run.
		if _, statErr := os.Stat(filepath.Join(s.SweepsDir, sweep.ID+".json")); statErr == nil {
			delete(s.sweeps, sweep.ID)
		}
		s.mu.Unlock()
		if s.PublishSweep != nil {
			s.PublishSweep(snap)
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	return sweep.snapshot(), nil
}

// sweepTaskEnded settles one task: in the sweep, and as a run.
func (s *Service) sweepTaskEnded(sweep *SweepState, row SweepRow) {
	s.mu.Lock()
	if t := sweep.task(row.Task); t != nil {
		t.Status, t.Adopted, t.Lesson, t.Error, t.CostUSD = row.Status, row.Adopted, row.Lesson, row.Error, row.CostUSD
	}
	sweep.CostUSD += row.CostUSD
	if row.Status == "ok" {
		sweep.Done++
	} else {
		sweep.Failed++
	}
	id := RunID(row.Task, sweep.K, sweep.Mode)
	var runSnap *RunState
	if state, ok := s.active[id]; ok {
		state.Status, state.CostUSD, state.Adopted, state.Lesson = row.Status, row.CostUSD, row.Adopted, row.Lesson
		if row.Error != "" {
			state.Error = row.Error
		}
		snap := state.snapshot()
		runSnap = &snap
		if _, err := os.Stat(s.summaryPath(state.Task, state.K, state.Mode)); err == nil {
			delete(s.active, id)
		}
	}
	sweepSnap := sweep.snapshot()
	s.mu.Unlock()
	if runSnap != nil && s.Publish != nil {
		s.Publish(*runSnap, Event{Type: "run.ended", Task: runSnap.Task, Mode: runSnap.Mode, OK: runSnap.Status == "ok", Detail: runSnap.Error})
	}
	if s.PublishSweep != nil {
		s.PublishSweep(sweepSnap)
	}
}

// CancelSweep stops a running sweep after the tasks in flight; finished
// tasks stay finished and the stopped ones resume like any run.
func (s *Service) CancelSweep(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sweep, ok := s.sweeps[id]
	if !ok || sweep.Status != "running" {
		return false
	}
	sweep.cancel()
	return true
}

// Sweeps lists sweeps, running first, then newest first: live ones from
// memory, finished ones from their records.
func (s *Service) Sweeps() []SweepState {
	seen := map[string]bool{}
	var out []SweepState
	s.mu.Lock()
	for id, sweep := range s.sweeps {
		seen[id] = true
		out = append(out, sweep.snapshot())
	}
	s.mu.Unlock()
	if s.SweepsDir != "" {
		paths, _ := filepath.Glob(filepath.Join(s.SweepsDir, "*.json"))
		for _, path := range paths {
			id := strings.TrimSuffix(filepath.Base(path), ".json")
			if seen[id] {
				continue
			}
			if state, err := s.readSweep(id); err == nil {
				out = append(out, state)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Status == "running") != (out[j].Status == "running") {
			return out[i].Status == "running"
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

// Sweep returns one sweep, live or finished.
func (s *Service) Sweep(id string) (SweepState, error) {
	s.mu.Lock()
	if sweep, ok := s.sweeps[id]; ok {
		defer s.mu.Unlock()
		return sweep.snapshot(), nil
	}
	s.mu.Unlock()
	return s.readSweep(id)
}

// readSweep renders a sweep record (SweepResult) as a SweepState.
func (s *Service) readSweep(id string) (SweepState, error) {
	if s.SweepsDir == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return SweepState{}, fmt.Errorf("no sweep %s", id)
	}
	var result SweepResult
	if err := readJSON(filepath.Join(s.SweepsDir, id+".json"), &result); err != nil {
		return SweepState{}, fmt.Errorf("no sweep %s", id)
	}
	state := SweepState{
		ID: id, Source: result.Source, K: result.K, Mode: result.Mode, Workers: max(result.Workers, 1),
		DomainOrder: result.DomainOrder, Done: result.Done, Failed: result.Failed, CostUSD: result.CostUSD,
		Budget: CostLine(result.Mode, result.K, true), StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		Status: "ok",
	}
	if result.Failed > 0 {
		state.Status = "partial"
	}
	for _, row := range result.Rows {
		state.Tasks = append(state.Tasks, SweepTask{
			Task: row.Task, Domain: row.Domain, RunID: RunID(row.Task, result.K, result.Mode), Status: row.Status,
			Adopted: row.Adopted, Lesson: row.Lesson, Error: row.Error, CostUSD: row.CostUSD,
		})
	}
	state.Rollouts = len(state.Tasks) * ExpectedRollouts(result.K)
	state.Edits = len(state.Tasks) * ExpectedSessions(result.K)
	return state, nil
}
