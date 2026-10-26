package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/harness"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// Harness² sweeps: many task files run as one batch, domains in order so
// each task reads the lessons its predecessors stored (internal/harness
// sweep.go). The sweeps list and a sweep's task list are ordinary list
// dialogs; each running task is also a live run on the server, so selecting
// one opens the run's plan with all of its monitoring.

const (
	harnessSweepsTitle = "Harness sweeps"
	harnessSweepTitle  = "Harness sweep"
)

func (a *App) harnessSweepsOverlay() tea.Cmd {
	a.openHarnessSweeps()
	return a.loadHarnessSweepsCmd("")
}

func (a *App) loadHarnessSweepsCmd(open string) tea.Cmd {
	c := a.client
	return func() tea.Msg {
		sweeps, err := c.HarnessSweeps(a.ctx)
		return harnessSweepsMsg{sweeps: sweeps, err: err, open: open}
	}
}

// harnessSweepsMsg carries a refreshed sweep list; open names one to show.
type harnessSweepsMsg struct {
	sweeps []client.HarnessSweep
	err    error
	open   string
	status string
}

func (a *App) openHarnessSweeps() {
	items := make([]overlayItem, 0, len(a.harnessSweeps))
	for _, sweep := range a.harnessSweeps {
		sweep := sweep
		category := "Finished"
		if sweep.Status == "running" {
			category = "Running"
		}
		items = append(items, overlayItem{
			Label:    harnessSweepName(sweep),
			Hint:     harnessSweepHint(sweep),
			Value:    sweep.ID,
			Category: category,
			Gutter:   harnessStatusGlyph(sweep.Status),
			Footer:   formatMoney(sweep.CostUSD),
			Action: func() tea.Msg {
				a.openHarnessSweep(sweep.ID)
				return nil
			},
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Category == "Running" && items[j].Category != "Running" })
	a.openList(harnessSweepsTitle, items)
	o := a.overlay
	o.SetSize(dialogLarge)
	o.SetPlaceholder("Search sweeps...")
	o.SetActions([]dialogAction{
		{Title: "new", Keys: "ctrl+a", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.newHarnessSweep() }},
		{Title: "cancel", Keys: "ctrl+d", OnTrigger: func(item overlayItem) tea.Cmd { return a.cancelHarnessSweep(item.Value) }},
		{Title: "refresh", Keys: "ctrl+r", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.loadHarnessSweepsCmd("") }},
		{Title: "runs", Keys: "ctrl+b", Standalone: true, Right: true, OnTrigger: func(overlayItem) tea.Cmd { return a.harnessOverlay() }},
	})
	if len(items) == 0 {
		if a.harnessSweepsErr != "" {
			o.SetEmptyView("Could not load sweeps", a.harnessSweepsErr)
		} else {
			o.SetEmptyView("No sweeps yet",
				"A sweep refines the harness over a folder of task files, one domain's tasks in order. Press ctrl+a to start one.")
		}
	}
}

// harnessSweepName is what the task files were, else the sweep's id.
func harnessSweepName(sweep client.HarnessSweep) string {
	if sweep.Source != "" {
		return sweep.Source
	}
	return sweep.ID
}

func harnessSweepHint(sweep client.HarnessSweep) string {
	parts := []string{fmt.Sprintf("%s k=%d", sweep.Mode, sweep.K), sweep.Status,
		fmt.Sprintf("%d/%d tasks", sweep.Done+sweep.Failed, len(sweep.Tasks))}
	if sweep.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", sweep.Failed))
	}
	parts = append(parts, plural(len(sweep.DomainOrder), "domain"))
	if sweep.Status == "running" && !sweep.StartedAt.IsZero() {
		parts = append(parts, harnessElapsed(time.Since(sweep.StartedAt)))
	}
	return strings.Join(parts, " · ")
}

func (a *App) harnessSweepByID(id string) (client.HarnessSweep, bool) {
	for _, sweep := range a.harnessSweeps {
		if sweep.ID == id {
			return sweep, true
		}
	}
	return client.HarnessSweep{}, false
}

// openHarnessSweep lists a sweep's tasks by domain, in the order they run.
// A task that has started is a run: selecting it opens the run's plan.
func (a *App) openHarnessSweep(id string) {
	sweep, ok := a.harnessSweepByID(id)
	if !ok {
		return
	}
	a.harnessOpenSweep = id
	items := make([]overlayItem, 0, len(sweep.Tasks))
	for _, task := range sweep.Tasks {
		task := task
		hint := task.Status
		switch {
		case task.Adopted != "":
			hint += " · adopted " + task.Adopted
			if task.Lesson != "" {
				hint += " · " + task.Lesson
			}
		case task.Error != "":
			hint += " · " + harnessShortReason(task.Error)
		}
		footer := ""
		if task.CostUSD > 0 {
			footer = formatMoney(task.CostUSD)
		}
		item := overlayItem{
			Label: task.Task, Hint: hint, Value: task.RunID, Category: task.Domain,
			Gutter: harnessSweepTaskGlyph(task.Status), Footer: footer,
		}
		if task.Status != "pending" {
			item.Action = func() tea.Msg { return a.openSweepTaskRun(task.RunID) }
		}
		items = append(items, item)
	}
	a.openList(harnessSweepTitle+" · "+harnessSweepName(sweep), items)
	o := a.overlay
	o.SetSize(dialogLarge)
	placeholder := fmt.Sprintf("%s per task · %d rollouts, %d edits in all · %s", sweep.Budget, sweep.Rollouts, sweep.Edits, formatMoney(sweep.CostUSD))
	if sweep.Status == "running" && !sweep.StartedAt.IsZero() {
		placeholder += " · " + harnessElapsed(time.Since(sweep.StartedAt))
	}
	o.SetPlaceholder(placeholder)
	actions := []dialogAction{}
	if sweep.Status == "running" {
		actions = append(actions, dialogAction{Title: "cancel", Keys: "ctrl+d", Standalone: true,
			OnTrigger: func(overlayItem) tea.Cmd { return a.cancelHarnessSweep(id) }})
	}
	actions = append(actions, dialogAction{Title: "back", Keys: "ctrl+b", Standalone: true, Right: true,
		OnTrigger: func(overlayItem) tea.Cmd { a.harnessOpenSweep = ""; return a.harnessSweepsOverlay() }})
	o.SetActions(actions)
	if len(items) == 0 {
		o.SetEmptyView("No tasks", "This sweep has no tasks.")
	}
}

// harnessSweepTaskGlyph is a task's state in the gutter: pending, running,
// then the run's own status glyph.
func harnessSweepTaskGlyph(status string) string {
	switch status {
	case "pending":
		return "·"
	case "running":
		return "◐"
	}
	return harnessStatusGlyph(status)
}

// openSweepTaskRun opens a sweep task's run, reading the run list first
// when the task's run is not in it yet.
func (a *App) openSweepTaskRun(runID string) tea.Msg {
	if _, ok := a.harnessRunByID(runID); ok {
		a.openHarnessRun(runID)
		return nil
	}
	c := a.client
	return tea.Cmd(func() tea.Msg {
		runs, err := c.HarnessRuns(a.ctx)
		return harnessRunsMsg{runs: runs, err: err, open: runID}
	})
}

// mergeHarnessSweeps folds live sweep states into the list.
func (a *App) mergeHarnessSweeps(live map[string]client.HarnessSweep) {
	for id, sweep := range live {
		replaced := false
		for i := range a.harnessSweeps {
			if a.harnessSweeps[i].ID == id {
				a.harnessSweeps[i] = sweep
				replaced = true
			}
		}
		if !replaced {
			a.harnessSweeps = append([]client.HarnessSweep{sweep}, a.harnessSweeps...)
		}
	}
}

func (a *App) cancelHarnessSweep(id string) tea.Cmd {
	sweep, ok := a.harnessSweepByID(id)
	if !ok || sweep.Status != "running" {
		return staticMsg(statusMsg{text: "that sweep is not running"})
	}
	return func() tea.Msg {
		if err := a.client.CancelHarnessSweep(a.ctx, id); err != nil {
			return statusMsg{text: "cancel failed: " + err.Error()}
		}
		return statusMsg{text: "cancelling the sweep after the tasks in flight"}
	}
}

// newHarnessSweep asks for the task files, then the budget, then plans.
func (a *App) newHarnessSweep() tea.Cmd {
	a.openInput("Harness sweep task files", "tasks",
		"Task files, or folders of *.md task files, relative to the project; separate several with commas",
		func(value string) tea.Msg {
			var paths []string
			for _, path := range strings.Split(value, ",") {
				if path = strings.TrimSpace(path); path != "" {
					paths = append(paths, path)
				}
			}
			if len(paths) == 0 {
				return statusMsg{text: "no task files given"}
			}
			return harnessSweepBudgetMsg{paths: paths}
		})
	return nil
}

// harnessSweepBudgetMsg asks for the sweep's budget once its tasks are named.
type harnessSweepBudgetMsg struct{ paths []string }

// openHarnessSweepBudget picks the per-task budget and plans the sweep.
func (a *App) openHarnessSweepBudget(paths []string) {
	model := a.currentModelLabel()
	a.openBudgetPicker("Harness sweep budget", " per task", func(b harness.Budget) tea.Msg {
		start := client.HarnessSweepStart{Paths: paths, K: b.K, Mode: b.Mode, Model: model, DryRun: true}
		return tea.Cmd(func() tea.Msg {
			plan, err := a.client.StartHarnessSweep(a.ctx, start)
			if err != nil {
				return statusMsg{text: "cannot plan the sweep: " + err.Error()}
			}
			return harnessSweepPlanMsg{start: start, plan: *plan}
		})
	})
}

// harnessSweepPlanMsg carries a sweep's dry-run plan to confirm.
type harnessSweepPlanMsg struct {
	start client.HarnessSweepStart
	plan  client.HarnessSweep
}

// confirmHarnessSweep states the whole price before anything is spent: the
// task count by domain and order, the budget per task and the totals.
func (a *App) confirmHarnessSweep(msg harnessSweepPlanMsg) {
	plan := msg.plan
	byDomain := map[string][]string{}
	for _, task := range plan.Tasks {
		byDomain[task.Domain] = append(byDomain[task.Domain], task.Task)
	}
	var order []string
	for _, domain := range plan.DomainOrder {
		order = append(order, fmt.Sprintf("%s: %s", domain, strings.Join(byDomain[domain], " → ")))
	}
	message := fmt.Sprintf("%s in %s", plural(len(plan.Tasks), "task"), plural(len(plan.DomainOrder), "domain"))
	if msg.start.Model != "" {
		message += ", on " + msg.start.Model
	}
	message += ".\n\n" + strings.Join(order, "\n") +
		fmt.Sprintf("\n\n%s per task.\nIn all: %d rollouts, %d editor sessions.", plan.Budget, plan.Rollouts, plan.Edits) +
		"\n\nEach domain's tasks run in order, every one reading the lessons the ones before it stored. " +
		"Rollouts run on copies of the workspace; your files are not touched."
	start := msg.start
	start.DryRun = false
	a.openConfirm("Start harness sweep?", message, "Cancel", func() tea.Msg {
		sweep, err := a.client.StartHarnessSweep(a.ctx, start)
		if err != nil {
			return statusMsg{text: "could not start the sweep: " + err.Error()}
		}
		sweeps := []client.HarnessSweep{*sweep}
		for _, s := range a.harnessSweeps {
			if s.ID != sweep.ID {
				sweeps = append(sweeps, s)
			}
		}
		return harnessSweepsMsg{sweeps: sweeps, open: sweep.ID, status: "harness sweep started"}
	}, nil)
}
