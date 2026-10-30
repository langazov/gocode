package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/harness"
	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/dialog"
)

// The Harness² surfaces (documentation/12-harness.md, "Running from the TUI"): /harness lists
// runs, a run opens as its plan of rows (base, probe, compose, select,
// reflect) that fill in live from the `harness.*` events, a row opens its
// harness diff and rollout, and /harness library browses the experience
// library. Every one is a KindList or a read-only panel (§9): there is no
// new dialog kind here.

const (
	harnessRunsTitle    = "Harness runs"
	harnessRunTitle     = "Harness run"
	harnessLibraryTitle = "Experience library"
)

// harnessOverlay opens the runs list from cache and refreshes behind it.
func (a *App) harnessOverlay() tea.Cmd {
	a.openHarnessRuns()
	return a.loadHarnessRunsCmd()
}

func (a *App) openHarnessRuns() {
	a.openList(harnessRunsTitle, a.harnessRunItems())
	o := a.overlay
	o.SetSize(dialogLarge)
	o.SetPlaceholder("Search runs...")
	o.SetActions([]dialogAction{
		// Standalone: starting a run must work from the empty list.
		{Title: "new", Keys: "ctrl+a", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.newHarnessRun("") }},
		// One key for both ends of a run's life: a running run is stopped, a
		// finished one deleted (its sessions and run directory). Both arm
		// first, like every destructive list action.
		{Title: "remove", Keys: "ctrl+d", OnTrigger: a.removeHarnessAction},
		{Title: "resume", Keys: "ctrl+y", OnTrigger: func(item overlayItem) tea.Cmd { return a.resumeHarnessRun(item.Value) }},
		{Title: "library", Keys: "ctrl+l", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.harnessLibraryOverlay() }},
		{Title: "installed", Keys: "ctrl+k", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.openInstalledHarness() }},
		{Title: "sweeps", Keys: "ctrl+t", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.harnessSweepsOverlay() }},
		{Title: "refresh", Keys: "ctrl+r", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.loadHarnessRunsCmd() }},
	})
	if len(a.harnessRuns) == 0 {
		switch {
		case a.harnessErr != "":
			o.SetEmptyView("Could not load harness runs", a.harnessErr)
			o.SetLocked(true)
			o.SetHideFilter(true)
		case !a.harnessLoaded:
			o.SetEmptyView("Loading harness runs", "Fetching runs...")
		default:
			o.SetEmptyView("No harness runs yet",
				"Press ctrl+a to refine the harness for this session's task, or type /harness refine.")
		}
	}
}

// harnessRunItems groups live runs above finished ones, newest first in
// each — the order the server returns, which is by start time.
func (a *App) harnessRunItems() []overlayItem {
	items := make([]overlayItem, 0, len(a.harnessRuns))
	for _, run := range a.harnessRuns {
		run := run
		category := "Finished"
		if run.Status == "running" {
			category = "Running"
		}
		items = append(items, overlayItem{
			Label:    run.Task,
			Hint:     harnessRunHint(run),
			Value:    run.ID,
			Category: category,
			Gutter:   harnessStatusGlyph(run.Status),
			Footer:   formatMoney(run.CostUSD),
			Action: func() tea.Msg {
				a.openHarnessRun(run.ID)
				return nil
			},
		})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Category == "Running" && items[j].Category != "Running" })
	return items
}

func harnessRunHint(run client.HarnessRun) string {
	parts := []string{fmt.Sprintf("%s k=%d", run.Mode, run.K), run.Status}
	if run.Status == "running" && run.Phase != "" {
		parts = append(parts, run.Phase)
	}
	if age := harnessRunAge(run); age != "" {
		parts = append(parts, age)
	}
	if run.Adopted != "" && run.Status != "running" {
		parts = append(parts, "adopted "+run.Adopted)
	}
	return strings.Join(parts, " · ")
}

// harnessStatusGlyph is the run's state in the gutter lane: the glyphs the
// timeline already uses for running, settled and failed work (§5).
func harnessStatusGlyph(status string) string {
	switch status {
	case "running":
		return "◐"
	case "ok":
		return "✓"
	case "partial", "cancelled":
		return "·"
	}
	return "✗"
}

func harnessRowGlyph(state string) string {
	switch state {
	case "editing", "rolling":
		return "◐"
	case "edited":
		return "~"
	case "ok":
		return "✓"
	case "failed":
		return "✗"
	}
	return "·"
}

func (a *App) harnessRunByID(id string) (client.HarnessRun, bool) {
	for _, run := range a.harnessRuns {
		if run.ID == id {
			return run, true
		}
	}
	return client.HarnessRun{}, false
}

// openHarnessRun shows one run as its plan. Rows are grouped by phase in
// the order the run moves through them, so the open phase is the one
// filling in.
func (a *App) openHarnessRun(id string) {
	run, ok := a.harnessRunByID(id)
	if !ok {
		return
	}
	a.harnessOpenRun = id
	items := a.harnessRowItems(run)
	title := harnessRunTitle + " · " + run.Task
	a.openList(title, items)
	o := a.overlay
	o.SetSize(dialogLarge)
	placeholder := fmt.Sprintf("%s · %s · %s", run.Budget, run.Backend, formatMoney(run.CostUSD))
	if age := harnessRunAge(run); age != "" {
		placeholder += " · " + age
	}
	o.SetPlaceholder(placeholder)
	if run.Adopted != "" {
		o.SetCurrent(run.Adopted)
	}
	actions := []dialogAction{
		// The session the row is running right now — its editor while it
		// edits, its rollout while it rolls — streamed live like a subagent.
		{Title: "watch", Keys: "ctrl+e", OnTrigger: func(item overlayItem) tea.Cmd { return a.watchHarnessRow(id, item.Value, false) }},
		{Title: "editor", Keys: "ctrl+o", OnTrigger: func(item overlayItem) tea.Cmd { return a.watchHarnessRow(id, item.Value, true) }},
		{Title: "log", Keys: "ctrl+g", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.openHarnessLog(id) }},
	}
	if run.Status == "ok" && run.Adopted != "" {
		actions = append(actions, dialogAction{Title: "compare", Keys: "ctrl+w", Standalone: true,
			OnTrigger: func(overlayItem) tea.Cmd { a.openHarnessCompare(id); return nil }})
	}
	if run.Status == "ok" {
		actions = append(actions, dialogAction{Title: "install", Keys: "ctrl+s", Standalone: true,
			OnTrigger: func(overlayItem) tea.Cmd { return a.planHarnessInstall(id, "project") }})
		if run.Backend == "split" || run.Backend == "cloud" {
			actions = append(actions, dialogAction{Title: "publish", Keys: "ctrl+u", Standalone: true,
				OnTrigger: func(overlayItem) tea.Cmd { return a.planHarnessInstall(id, "library") }})
		}
	}
	if run.Status == "running" {
		actions = append(actions, dialogAction{Title: "cancel", Keys: "ctrl+d", Standalone: true,
			OnTrigger: func(overlayItem) tea.Cmd { return a.cancelHarnessRun(id) }})
	}
	if harnessResumable(run) {
		actions = append(actions, dialogAction{Title: "resume", Keys: "ctrl+y", Standalone: true,
			OnTrigger: func(overlayItem) tea.Cmd { return a.resumeHarnessRun(id) }})
	}
	actions = append(actions, dialogAction{Title: "back", Keys: "ctrl+b", Standalone: true, Right: true,
		OnTrigger: func(overlayItem) tea.Cmd { a.harnessOpenRun = ""; return a.harnessOverlay() }})
	o.SetActions(actions)
	if len(items) == 0 {
		o.SetEmptyView("Nothing planned", "This run has no rows.")
	}
}

// harnessRowItems renders a run's plan. A row that is working shows what its
// session is doing this moment — the tool it is running and on what, from
// the live session events the server relays — so the plan reads as a
// monitor, not a checklist.
func (a *App) harnessRowItems(run client.HarnessRun) []overlayItem {
	id := run.ID
	items := make([]overlayItem, 0, len(run.Rows))
	for _, row := range run.Rows {
		row := row
		hint := row.State
		if activity := a.harnessRowActivity(row); activity != "" {
			hint += " · " + activity
		} else if row.State == "failed" && row.Detail != "" {
			// The reason first and short; the stage panel opens on the
			// whole error.
			hint += " · " + harnessShortReason(row.Detail)
		} else if row.Detail != "" {
			hint += " · " + strings.Join(strings.Fields(row.Detail), " ")
		}
		if row.Retries > 0 {
			hint += fmt.Sprintf(" · ↻%d", row.Retries)
		}
		footer := a.harnessRowCounts(row)
		if row.CostUSD > 0 {
			footer = formatMoney(row.CostUSD)
		}
		label := row.Label
		if row.Label == run.Adopted {
			label += " (adopted)"
		}
		items = append(items, overlayItem{
			Label:    label,
			Hint:     hint,
			Value:    row.Label,
			Category: harnessPhaseLabel(row.Phase),
			Gutter:   harnessRowGlyph(row.State),
			Footer:   footer,
			// A command, not its result: the fetch must not run on the update
			// goroutine.
			Action: func() tea.Msg { return a.loadHarnessStageCmd(id, row.Label) },
		})
	}
	return items
}

// harnessRowSession is the session a row is working in now: its editor's
// while it edits (or when editor is asked for), else its rollout's.
func harnessRowSession(row client.HarnessRow, editor bool) string {
	if editor || row.State == "editing" || row.SessionID == "" {
		if row.EditSessionID != "" {
			return row.EditSessionID
		}
	}
	return row.SessionID
}

// harnessRowActivity is what a working row's session is doing: the newest
// tool still running, with its telling argument, or "writing" while text
// streams. Empty for a row that is not working.
func (a *App) harnessRowActivity(row client.HarnessRow) string {
	if row.State != "editing" && row.State != "rolling" {
		return ""
	}
	node := a.agents.Sessions[harnessRowSession(row, false)]
	if node == nil {
		return ""
	}
	var running []ToolState
	for _, tool := range node.Tools {
		if tool.Status == "pending" {
			running = append(running, tool)
		}
	}
	doing := ""
	if len(running) > 0 {
		sort.Slice(running, func(i, j int) bool { return running[i].CallID > running[j].CallID })
		tool := running[0]
		doing = tool.Name
		if tool.Input != "" {
			doing += " " + truncateRunes(tool.Input, 60)
		}
	} else if node.Busy {
		doing = "thinking"
	}
	if quiet := harnessQuiet(node.LastEvent, time.Now()); quiet != "" {
		if doing == "" {
			return quiet
		}
		return doing + " · " + quiet
	}
	return doing
}

// harnessRowCounts is a working row's progress as its session's step and
// tool counts — "12 steps · 30 tools · 2 failed" — or empty. Many refused or
// failed calls with few steps is a rollout going in circles.
func (a *App) harnessRowCounts(row client.HarnessRow) string {
	if row.State != "editing" && row.State != "rolling" {
		return ""
	}
	node := a.agents.Sessions[harnessRowSession(row, false)]
	if node == nil || node.Steps == 0 {
		return ""
	}
	parts := []string{plural(node.Steps, "step"), plural(node.ToolCalls, "tool")}
	if node.ToolFailures > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", node.ToolFailures))
	}
	return strings.Join(parts, " · ")
}

// harnessShortReason names why a row failed in a few words, for the row's
// hint: the stop limits and an interrupt by name, anything else its first
// line clipped. The stage panel shows the whole error.
func harnessShortReason(detail string) string {
	switch {
	case strings.Contains(detail, "without activity"):
		return "stalled (no activity)"
	case strings.Contains(detail, "did not finish in time"):
		return "timed out"
	case strings.Contains(detail, "interrupted"), strings.Contains(detail, "context canceled"):
		return "interrupted"
	}
	first, _, _ := strings.Cut(strings.TrimSpace(detail), "\n")
	return truncateRunes(first, 60)
}

// harnessQuietAfter is how long a working row's session may go without an
// event before its row says so. Well short of the run's idle limit (15m by
// default), which stops the session: the reader sees a stall coming.
const harnessQuietAfter = 2 * time.Minute

// harnessQuiet is "quiet 3m" once a session has gone harnessQuietAfter
// without an event, else empty. Minutes only: the plan rebuilds when the
// text changes, and the reconciliation tick is what moves it on.
func harnessQuiet(last, now time.Time) string {
	if last.IsZero() || now.Sub(last) < harnessQuietAfter {
		return ""
	}
	return "quiet " + harnessElapsed(now.Sub(last))
}

// harnessElapsed is a run's age at the resolution the interface refreshes
// it: "<1m", "12m", "1h 05m".
func harnessElapsed(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// harnessRunAge is how long a running run has been going, or empty.
func harnessRunAge(run client.HarnessRun) string {
	if run.Status != "running" || run.StartedAt.IsZero() {
		return ""
	}
	return harnessElapsed(time.Since(run.StartedAt))
}

func harnessPhaseLabel(phase string) string {
	switch phase {
	case "base":
		return "Base rollouts"
	case "probe":
		return "Propose and probe"
	case "compose":
		return "Compose"
	case "select":
		return "Select"
	case "reflect":
		return "Reflect"
	}
	return "Other"
}

// refreshHarnessDialogs rebuilds whichever harness dialog is open, keeping
// the filter and selection, after the run cache moved.
func (a *App) refreshHarnessDialogs() {
	o := a.overlay
	if o == nil || o.Kind != dialog.KindList {
		return
	}
	filter, selected := o.Filter(), a.selectedOverlayValue()
	switch {
	case o.Title == harnessRunsTitle:
		a.openHarnessRuns()
	case strings.HasPrefix(o.Title, harnessRunTitle+" · ") && a.harnessOpenRun != "":
		a.openHarnessRun(a.harnessOpenRun)
	case o.Title == harnessSweepsTitle:
		a.openHarnessSweeps()
	case strings.HasPrefix(o.Title, harnessSweepTitle+" · ") && a.harnessOpenSweep != "":
		a.openHarnessSweep(a.harnessOpenSweep)
	default:
		return
	}
	a.restoreOverlaySelection(filter, selected)
}

// mergeHarnessRuns folds live run states into the cache, returning the runs
// that stopped running in this update so the caller can say so.
func (a *App) mergeHarnessRuns(live map[string]client.HarnessRun) []client.HarnessRun {
	var ended []client.HarnessRun
	for id, run := range live {
		replaced := false
		for i := range a.harnessRuns {
			if a.harnessRuns[i].ID != id {
				continue
			}
			if a.harnessRuns[i].Status == "running" && run.Status != "running" {
				ended = append(ended, run)
			}
			a.harnessRuns[i] = run
			replaced = true
		}
		if !replaced {
			a.harnessRuns = append([]client.HarnessRun{run}, a.harnessRuns...)
		}
	}
	return ended
}

// applyHarnessSnapshot takes the harness half of an aggregator snapshot.
func (a *App) applyHarnessSnapshot(snapshot Snapshot) tea.Cmd {
	if snapshot.HarnessSeq == a.harnessSeq {
		// Nothing moved in the runs themselves, but a working row's session
		// may have started a new tool: refresh the open plan when what it
		// shows would change.
		a.refreshHarnessActivity()
		return nil
	}
	a.harnessSeq = snapshot.HarnessSeq
	a.harnessLoaded = true
	ended := a.mergeHarnessRuns(snapshot.Harness)
	a.mergeHarnessSweeps(snapshot.HarnessSweeps)
	a.refreshHarnessDialogs()
	var follow tea.Cmd
	if a.harnessLogOpen() && a.harnessLogRun != "" {
		id, after := a.harnessLogRun, len(a.harnessLog)
		follow = func() tea.Msg {
			events, err := a.client.HarnessLog(a.ctx, id, after)
			if err != nil || len(events) == 0 {
				return nil
			}
			return harnessLogMsg{id: id, events: events}
		}
	}
	if len(ended) == 0 {
		return follow
	}
	run := ended[0]
	opts := toastOptions{title: "Harness² · " + run.Task, variant: toastSuccess}
	switch run.Status {
	case "ok":
		opts.message = "adopted " + run.Adopted + " · " + formatMoney(run.CostUSD) + " · /harness to review and install"
	case "cancelled":
		opts.message, opts.variant = "cancelled · finished work is kept for a resume", toastInfo
	default:
		opts.message, opts.variant = run.Status+": "+run.Error, toastError
	}
	return tea.Batch(follow, a.showToastOptions(opts))
}

// refreshHarnessActivity rebuilds the open run plan when a working row's
// live activity changed, and only then: snapshots arrive every frame.
func (a *App) refreshHarnessActivity() {
	o := a.overlay
	if o == nil || o.Kind != dialog.KindList || !strings.HasPrefix(o.Title, harnessRunTitle+" · ") || a.harnessOpenRun == "" {
		return
	}
	run, ok := a.harnessRunByID(a.harnessOpenRun)
	if !ok || run.Status != "running" {
		return
	}
	var b strings.Builder
	// The run's age is in the placeholder and moves by the minute.
	b.WriteString(harnessRunAge(run))
	b.WriteByte(0)
	for _, row := range run.Rows {
		b.WriteString(a.harnessRowActivity(row))
		b.WriteByte(0)
		b.WriteString(a.harnessRowCounts(row))
		b.WriteByte(0)
	}
	if b.String() == a.harnessActivitySig {
		return
	}
	a.harnessActivitySig = b.String()
	a.refreshHarnessDialogs()
}

// activeHarnessRun is the newest running run, for the hint row.
func (a *App) activeHarnessRun() (client.HarnessRun, bool) {
	for _, run := range a.harnessRuns {
		if run.Status == "running" {
			return run, true
		}
	}
	return client.HarnessRun{}, false
}

// harnessFooterSegment is the hint row's note of a run going on in the
// background: its task, phase, progress and spend.
func (a *App) harnessFooterSegment() string {
	run, ok := a.activeHarnessRun()
	if !ok {
		// Nothing running: say which harness the open session runs under,
		// by the fingerprint its timeline marker carries.
		if data, loaded := a.activeSessionHarness(); loaded && data.Fingerprint != "" {
			return a.styles().Muted.Render("harness " + data.Fingerprint)
		}
		return ""
	}
	done := 0
	for _, row := range run.Rows {
		if row.State == "ok" || row.State == "failed" {
			done++
		}
	}
	parts := []string{"harness " + truncateRunes(run.Task, 24), fmt.Sprintf("%s %d/%d", run.Phase, done, len(run.Rows))}
	if age := harnessRunAge(run); age != "" {
		parts = append(parts, age)
	}
	text := strings.Join(append(parts, formatMoney(run.CostUSD)), " · ")
	return a.styles().Muted.Render(text)
}

// harnessTaskPrompt is the task /harness refine adapts for: the open
// session's first request, as the user wrote it.
func (a *App) harnessTaskPrompt() string {
	for _, message := range a.timeline {
		if message.Type != "user" {
			continue
		}
		if user, err := client.DecodeUser(message.Data); err == nil && strings.TrimSpace(user.Text) != "" {
			return strings.TrimSpace(user.Text)
		}
	}
	return ""
}

// newHarnessRun asks for the task (prefilled from the open session), shows
// the plan and its cost, and starts the run only once that is confirmed —
// a run spends 4k rollouts and 3k editor sessions.
func (a *App) newHarnessRun(prompt string) tea.Cmd {
	if prompt == "" {
		prompt = a.harnessTaskPrompt()
	}
	a.openInput("Refine harness for task", prompt, "Describe the task the harness should get better at", func(value string) tea.Msg {
		value = strings.TrimSpace(value)
		if value == "" {
			return statusMsg{text: "no task given"}
		}
		return harnessBudgetMsg{prompt: value}
	})
	return nil
}

// harnessBudgetMsg asks for the run's mode and k once the task is known.
type harnessBudgetMsg struct{ prompt string }

// The offered (mode, k) pairs live in internal/harness (harness.Budgets),
// served as GET /api/harness/budgets, so the TUI and the desktop app offer
// the same choice.

// openHarnessBudget lists the budgets; picking one plans the run on the
// open session's model and opens the confirm.
func (a *App) openHarnessBudget(prompt string) {
	model := a.currentModelLabel()
	a.openBudgetPicker("Harness run budget", "", func(b harness.Budget) tea.Msg {
		start := client.HarnessStart{Prompt: prompt, K: b.K, Mode: b.Mode, Model: model, DryRun: true}
		if a.active != nil {
			start.Workspace = a.active.Directory
		}
		return a.planHarnessCmd(start)
	})
}

// openBudgetPicker lists the budgets cheapest first, each footed with its
// rollouts and edits (per task: per is what a sweep multiplies); picking one
// calls onPick, which plans before anything is spent.
func (a *App) openBudgetPicker(title, per string, onPick func(harness.Budget) tea.Msg) {
	model := a.currentModelLabel()
	budgets := harness.Budgets()
	items := make([]overlayItem, 0, len(budgets))
	for _, b := range budgets {
		b := b
		items = append(items, overlayItem{
			Label:  fmt.Sprintf("%s k=%d", b.Mode, b.K),
			Hint:   b.Description,
			Value:  fmt.Sprintf("%s-%d", b.Mode, b.K),
			Footer: fmt.Sprintf("%d rollouts · %d edits%s", b.Rollouts, b.Edits, per),
			Action: func() tea.Msg { return onPick(b) },
		})
	}
	a.openList(title, items)
	o := a.overlay
	o.SetSize(dialogMedium)
	o.SetHideFilter(true)
	o.SetCurrent("parallel-1")
	if model != "" {
		o.SetPlaceholder("rollouts and edits run on " + model)
	}
}

func (a *App) planHarnessCmd(start client.HarnessStart) tea.Cmd {
	return func() tea.Msg {
		plan, err := a.client.StartHarness(a.ctx, start)
		if err != nil {
			return statusMsg{text: "cannot plan the run: " + err.Error()}
		}
		return harnessPlanMsg{start: start, plan: *plan}
	}
}

// harnessPlanMsg carries a dry-run plan to confirm.
type harnessPlanMsg struct {
	start client.HarnessStart
	plan  client.HarnessRun
}

func (a *App) confirmHarnessPlan(msg harnessPlanMsg) {
	start := msg.start
	start.DryRun = false
	message := fmt.Sprintf("Task %s in %s.\n\n%s.\nExperience library: %s", msg.plan.Task, msg.plan.Domain, msg.plan.Budget, msg.plan.Backend)
	if msg.start.Model != "" {
		message = fmt.Sprintf("Task %s in %s, on %s.\n\n%s.\nExperience library: %s", msg.plan.Task, msg.plan.Domain, msg.start.Model, msg.plan.Budget, msg.plan.Backend)
	}
	if msg.plan.Reason != "" {
		message += " (" + msg.plan.Reason + ")"
	}
	message += ".\n\nRollouts run on copies of the workspace; your files are not touched."
	a.openConfirm("Start harness run?", message, "Cancel", func() tea.Msg {
		run, err := a.client.StartHarness(a.ctx, start)
		if err != nil {
			return statusMsg{text: "could not start the run: " + err.Error()}
		}
		return harnessRunsMsg{runs: append([]client.HarnessRun{*run}, a.harnessRuns...), open: run.ID, status: "harness run started"}
	}, nil)
}

func (a *App) removeHarnessAction(item overlayItem) tea.Cmd {
	run, ok := a.harnessRunByID(item.Value)
	if !ok {
		return nil
	}
	o := a.overlay
	if o != nil && o.Armed() != item.Value {
		o.Arm(item.Value, "ctrl+d")
		return nil
	}
	if o != nil {
		o.Disarm()
	}
	if run.Status == "running" {
		return a.cancelHarnessRun(item.Value)
	}
	id := item.Value
	return func() tea.Msg {
		if err := a.client.DeleteHarness(a.ctx, id); err != nil {
			return statusMsg{text: "delete failed: " + err.Error()}
		}
		runs, err := a.client.HarnessRuns(a.ctx)
		return harnessRunsMsg{runs: runs, err: err, status: "deleted " + run.Task + " (" + run.Mode + ")"}
	}
}

// harnessResumable is a run that stopped short of ok: partial, cancelled,
// failed, base-failed. Running again only redoes what was cut short.
func harnessResumable(run client.HarnessRun) bool {
	return run.Status != "running" && run.Status != "ok"
}

// resumeHarnessRun confirms, then starts a stopped run again. Spending money
// always shows its price first (§6.6): the confirm names the budget and that
// finished work is reused.
func (a *App) resumeHarnessRun(id string) tea.Cmd {
	run, ok := a.harnessRunByID(id)
	if !ok {
		return nil
	}
	if !harnessResumable(run) {
		return staticMsg(statusMsg{text: "nothing to resume: the run is " + run.Status})
	}
	message := fmt.Sprintf("Task %s, %s k=%d, ended %s.\n\n%s.\n\n", run.Task, run.Mode, run.K, run.Status, run.Budget) +
		"Finished stages and rollouts are reused; only what was cut short runs, and costs, again. " +
		"It runs on the server's current model."
	a.openConfirm("Resume harness run?", message, "Cancel", func() tea.Msg {
		resumed, err := a.client.ResumeHarness(a.ctx, id)
		if err != nil {
			return statusMsg{text: "could not resume the run: " + err.Error()}
		}
		runs := []client.HarnessRun{*resumed}
		for _, r := range a.harnessRuns {
			if r.ID != resumed.ID {
				runs = append(runs, r)
			}
		}
		return harnessRunsMsg{runs: runs, open: resumed.ID, status: "harness run resumed"}
	}, nil)
	return nil
}

func (a *App) cancelHarnessRun(id string) tea.Cmd {
	return func() tea.Msg {
		if err := a.client.CancelHarness(a.ctx, id); err != nil {
			return statusMsg{text: "cancel failed: " + err.Error()}
		}
		return statusMsg{text: "cancelling harness run"}
	}
}

// watchHarnessRow opens the session behind a row: the one it is working in
// now, or its editor's when editor is set. The server relays rollout and
// editor sessions' events, so the session view streams it live; /harness
// comes back to the run.
func (a *App) watchHarnessRow(id, label string, editor bool) tea.Cmd {
	run, ok := a.harnessRunByID(id)
	if !ok {
		return nil
	}
	var row client.HarnessRow
	for _, r := range run.Rows {
		if r.Label == label {
			row = r
		}
	}
	sessionID := harnessRowSession(row, editor)
	if editor {
		sessionID = row.EditSessionID
	}
	if sessionID == "" {
		what := "a session"
		if editor {
			what = "an editor session"
		}
		return staticMsg(statusMsg{text: label + " has no " + what + " yet"})
	}
	return func() tea.Msg {
		session, err := a.client.Session(a.ctx, sessionID)
		if err != nil {
			return statusMsg{text: "could not open " + label + "'s session: " + err.Error()}
		}
		// Through CloseThen, so the run dialog is gone before the session
		// view assumes no dialog is standing (§9.1).
		return dialog.CloseThenMsg{Then: func() tea.Cmd { return staticMsg(sessionOpenedMsg{session: session}) }}
	}
}

func (a *App) loadHarnessStageCmd(id, label string) tea.Cmd {
	return func() tea.Msg {
		stage, err := a.client.HarnessStage(a.ctx, id, label)
		if err != nil {
			return statusMsg{text: "could not read " + label + ": " + err.Error()}
		}
		return harnessStageMsg{stage: *stage}
	}
}

type harnessStageMsg struct{ stage client.HarnessStage }

// harnessStageLines renders a row's evidence for the read-only panel: what
// changed in its harness, the editor's notes, then its rollout's product.
func harnessStageLines(stage client.HarnessStage) []panelLine {
	var lines []panelLine
	section := func(title string, body []panelLine) {
		if len(lines) > 0 {
			lines = append(lines, panelLine{})
		}
		lines = append(lines, panelLine{Text: strings.ToUpper(title), Tone: toneHeading})
		lines = append(lines, body...)
	}
	// What went wrong first: it is why a failed row was opened.
	if stage.Error != "" {
		section("Error", proseLines(stage.Error, toneError))
	}
	files := stage.Files
	if len(files.Added)+len(files.Modified)+len(files.Removed) == 0 {
		section("Harness", []panelLine{{Text: "same as base", Tone: toneMuted}})
	} else {
		var body []panelLine
		for _, rel := range files.Added {
			body = append(body, panelLine{Text: "+ " + rel, Tone: toneAdd})
		}
		for _, rel := range files.Modified {
			body = append(body, panelLine{Text: "~ " + rel, Tone: toneHunk})
		}
		for _, rel := range files.Removed {
			body = append(body, panelLine{Text: "- " + rel, Tone: toneDel})
		}
		body = append(body, panelLine{Text: fmt.Sprintf("%+d always-on chars", files.AlwaysOnCharsDelta), Tone: toneMuted})
		section("Harness", body)
	}
	if stage.Notes != "" {
		section("Editor notes", proseLines(stage.Notes, toneBody))
	}
	if stage.Diff != "" {
		section("Harness diff", diffLines(stage.Diff))
	}
	if stage.Deliverable != "" {
		status := "delivered"
		if !stage.Delivered {
			status = "nothing delivered"
		}
		section("Rollout · "+status+" · "+formatMoney(stage.CostUSD), deliverableLines(stage.Deliverable))
	}
	return lines
}

// deliverableLines splits a rollout's deliverable (harness recursion.go:
// "# Final message", then "# Changes to the workspace" with a fenced diff)
// into the agent's words, wrapped, and its changes, colored as a diff.
func deliverableLines(deliverable string) []panelLine {
	final, changes, _ := strings.Cut(deliverable, "# Changes to the workspace")
	final = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(final), "# Final message"))
	lines := proseLines(final, toneBody)
	changes = strings.TrimSpace(changes)
	if changes == "" {
		return lines
	}
	lines = append(lines, panelLine{}, panelLine{Text: "CHANGES TO THE WORKSPACE", Tone: toneHeading})
	head, rest, fenced := strings.Cut(changes, "```diff")
	lines = append(lines, proseLines(strings.TrimSpace(head), toneMuted)...)
	if fenced {
		diff, _, _ := strings.Cut(rest, "```")
		lines = append(lines, diffLines(strings.TrimPrefix(diff, "\n"))...)
	}
	return lines
}

// splitPanelLines splits text into panel rows. Tabs become spaces: a raw tab
// spliced into a composited row tears it.
func splitPanelLines(text string) []string {
	text = strings.ReplaceAll(strings.TrimRight(text, "\n"), "\t", "    ")
	return strings.Split(text, "\n")
}

// planHarnessInstall dry-runs an install and confirms what it would write.
func (a *App) planHarnessInstall(id, to string) tea.Cmd {
	return func() tea.Msg {
		plan, err := a.client.InstallHarness(a.ctx, id, to, false, true)
		if err != nil {
			return statusMsg{text: "cannot plan the install: " + err.Error()}
		}
		return harnessInstallPlanMsg{id: id, to: to, plan: *plan}
	}
}

type harnessInstallPlanMsg struct {
	id, to string
	plan   client.HarnessInstall
}

func (a *App) confirmHarnessInstall(msg harnessInstallPlanMsg) tea.Cmd {
	plan := msg.plan
	var lines []string
	if msg.to == "project" || msg.to == "both" {
		files := plan.Files
		if len(files.Added)+len(files.Modified)+len(files.Removed) == 0 {
			return staticMsg(statusMsg{text: "the project already holds this harness"})
		}
		lines = append(lines, fmt.Sprintf("Writes %s: %d new, %d changed, %d removed.",
			plan.Target, len(files.Added), len(files.Modified), len(files.Removed)))
		if plan.Conflict {
			lines = append(lines, "Replaces the harness installed there now.")
		}
		if len(plan.Collisions) > 0 {
			lines = append(lines, "Your own "+strings.Join(plan.Collisions, ", ")+" keep precedence over the harness's copies.")
		}
	}
	if plan.Library != nil {
		p := plan.Library
		lines = append(lines, fmt.Sprintf("Library skills: %d new, %d changed, %d unchanged.", len(p.New), len(p.Blocked)+len(p.Changed), len(p.Unchanged)))
		if len(p.Blocked) > 0 {
			lines = append(lines, "Changed skills are replaced: "+strings.Join(p.Blocked, ", ")+".")
		}
	}
	lines = append(lines, plan.Warnings...)
	title := "Install harness?"
	if msg.to == "library" {
		title = "Publish skills to the Library?"
	}
	id, to := msg.id, msg.to
	a.openConfirm(title, strings.Join(lines, "\n"), "Cancel", func() tea.Msg {
		result, err := a.client.InstallHarness(a.ctx, id, to, true, false)
		if err != nil {
			return statusMsg{text: "install failed: " + err.Error()}
		}
		if result.Written {
			return statusMsg{text: "harness installed into " + result.Target + " — new sessions use it"}
		}
		return statusMsg{text: "skills published to the Library"}
	}, nil)
	return nil
}

// harnessLibraryOverlay opens the experience library from cache.
func (a *App) harnessLibraryOverlay() tea.Cmd {
	a.openHarnessLibrary()
	return a.loadHarnessLibraryCmd()
}

func (a *App) openHarnessLibrary() {
	var items []overlayItem
	backend := ""
	if lib := a.harnessLibrary; lib != nil {
		backend = lib.Backend
		for _, domain := range lib.Domains {
			for _, entry := range domain.Entries {
				entry := entry
				hint := entry.Cue
				if entry.Pending {
					hint = "pending upload · " + hint
				}
				items = append(items, overlayItem{
					Label:    entry.Task,
					Hint:     hint,
					Value:    entry.Domain + "/" + entry.Task,
					Category: domain.Domain,
					Footer:   entry.Components,
					Action:   func() tea.Msg { return a.loadHarnessEntryCmd(entry.Domain, entry.Task) },
				})
			}
		}
	}
	title := harnessLibraryTitle
	if backend != "" {
		title += " · " + backend
	}
	a.openList(title, items)
	o := a.overlay
	o.SetSize(dialogLarge)
	o.SetPlaceholder("Search lessons...")
	o.SetActions([]dialogAction{
		{Title: "forget", Keys: "ctrl+d", OnTrigger: a.forgetHarnessEntryAction},
		{Title: "runs", Keys: "ctrl+b", Standalone: true, Right: true, OnTrigger: func(overlayItem) tea.Cmd { return a.harnessOverlay() }},
	})
	if len(items) == 0 {
		switch {
		case a.harnessLibraryErr != "":
			o.SetEmptyView("Could not load the experience library", a.harnessLibraryErr)
			o.SetLocked(true)
			o.SetHideFilter(true)
		case a.harnessLibrary == nil:
			o.SetEmptyView("Loading the experience library", "Fetching entries...")
		default:
			o.SetEmptyView("No lessons yet", "An entry is stored when a harness run adopts a harness and its record supports a lesson.")
		}
	}
}

func (a *App) forgetHarnessEntryAction(item overlayItem) tea.Cmd {
	o := a.overlay
	if o == nil {
		return nil
	}
	if o.Armed() != item.Value {
		o.Arm(item.Value, "ctrl+d")
		return nil
	}
	o.Disarm()
	domain, task, _ := strings.Cut(item.Value, "/")
	return func() tea.Msg {
		if err := a.client.ForgetHarnessEntry(a.ctx, domain, task); err != nil {
			return statusMsg{text: "forget failed: " + err.Error()}
		}
		lib, err := a.client.HarnessLibrary(a.ctx)
		return harnessLibraryMsg{library: lib, err: err, status: "forgot " + item.Value}
	}
}

func (a *App) loadHarnessEntryCmd(domain, task string) tea.Cmd {
	return func() tea.Msg {
		entry, err := a.client.HarnessEntry(a.ctx, domain, task)
		if err != nil {
			return statusMsg{text: "could not read the entry: " + err.Error()}
		}
		return harnessEntryMsg{entry: *entry}
	}
}

type harnessEntryMsg struct{ entry client.HarnessEntry }

func harnessEntryLines(entry client.HarnessEntry) []panelLine {
	e := entry.Entry
	heading := func(text string) panelLine { return panelLine{Text: text, Tone: toneHeading} }
	lines := []panelLine{heading("LESSON")}
	for _, field := range []struct{ name, value string }{
		{"cue", e.Lesson.Cue}, {"lesson", e.Lesson.Lesson}, {"evidence", e.Lesson.Evidence}, {"confidence", e.Lesson.Confidence},
	} {
		lines = append(lines, panelLine{Text: fmt.Sprintf("%-11s %s", field.name+":", field.value)})
	}
	if e.Why != "" {
		lines = append(lines, panelLine{}, heading("WHY "+strings.ToUpper(e.Label)+" WAS ADOPTED"))
		lines = append(lines, proseLines(e.Why, toneBody)...)
	}
	if e.Lesson.Edits != "" {
		lines = append(lines, panelLine{}, heading("WHAT EACH EDIT DID"))
		lines = append(lines, proseLines(e.Lesson.Edits, toneBody)...)
	}
	paths := make([]string, 0, len(entry.Tree))
	for rel, content := range entry.Tree {
		if strings.TrimSpace(content) != "" {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	lines = append(lines, panelLine{}, heading("HARNESS"))
	for _, rel := range paths {
		lines = append(lines, panelLine{}, panelLine{Text: "── " + rel, Tone: toneHunk})
		lines = append(lines, proseLines(entry.Tree[rel], toneBody)...)
	}
	return lines
}

// harnessRunsMsg carries a refreshed run list; open names a run to show.
type harnessRunsMsg struct {
	runs   []client.HarnessRun
	err    error
	open   string
	status string
}

type harnessLibraryMsg struct {
	library *client.HarnessLibrary
	err     error
	status  string
}

func (a *App) loadHarnessRunsCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		runs, err := c.HarnessRuns(a.ctx)
		return harnessRunsMsg{runs: runs, err: err}
	}
}

func (a *App) loadHarnessLibraryCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		lib, err := c.HarnessLibrary(a.ctx)
		return harnessLibraryMsg{library: lib, err: err}
	}
}

// handleHarnessMsg handles the harness messages; ok reports whether msg was
// one of them.
func (a *App) handleHarnessMsg(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case harnessRunsMsg:
		if msg.err != nil {
			a.harnessErr = msg.err.Error()
		} else {
			a.harnessErr = ""
			a.harnessLoaded = true
			a.harnessRuns = msg.runs
		}
		if msg.open != "" {
			a.openHarnessRun(msg.open)
		} else {
			a.refreshHarnessDialogs()
		}
		if msg.status != "" {
			return staticMsg(statusMsg{text: msg.status}), true
		}
		return nil, true
	case harnessLibraryMsg:
		if msg.err != nil {
			a.harnessLibraryErr = msg.err.Error()
		} else {
			a.harnessLibraryErr = ""
			a.harnessLibrary = msg.library
		}
		if o := a.overlay; o != nil && o.Kind == dialog.KindList && strings.HasPrefix(o.Title, harnessLibraryTitle) {
			filter, selected := o.Filter(), a.selectedOverlayValue()
			a.openHarnessLibrary()
			a.restoreOverlaySelection(filter, selected)
		}
		if msg.status != "" {
			return staticMsg(statusMsg{text: msg.status}), true
		}
		return nil, true
	case harnessBudgetMsg:
		a.openHarnessBudget(msg.prompt)
		return nil, true
	case harnessPlanMsg:
		a.confirmHarnessPlan(msg)
		return nil, true
	case harnessInstallPlanMsg:
		return a.confirmHarnessInstall(msg), true
	case harnessLogMsg:
		if msg.reset || a.harnessLogOpen() {
			a.showHarnessLog(msg)
		}
		return nil, true
	case harnessStageMsg:
		a.openHarnessPanel(msg.stage.Label, harnessStageLines(msg.stage))
		return nil, true
	case harnessSweepsMsg:
		if msg.err != nil {
			a.harnessSweepsErr = msg.err.Error()
		} else {
			a.harnessSweepsErr = ""
			a.harnessSweeps = msg.sweeps
		}
		if msg.open != "" {
			a.openHarnessSweep(msg.open)
		} else {
			a.refreshHarnessDialogs()
		}
		if msg.status != "" {
			return staticMsg(statusMsg{text: msg.status}), true
		}
		return nil, true
	case harnessSweepBudgetMsg:
		a.openHarnessSweepBudget(msg.paths)
		return nil, true
	case harnessSweepPlanMsg:
		a.confirmHarnessSweep(msg)
		return nil, true
	case harnessCompareMsg:
		a.openHarnessPanel("Compare", harnessCompareLines(*msg.comparison))
		return nil, true
	case harnessInstalledMsg:
		a.openHarnessPanel("Installed harness", harnessInstalledLines(*msg.installed))
		return nil, true
	case harnessEntryMsg:
		a.openHarnessPanel(msg.entry.Entry.Domain+"/"+msg.entry.Entry.Task, harnessEntryLines(msg.entry))
		return nil, true
	}
	return nil, false
}

// harnessCommand handles "/harness <args>".
func (a *App) harnessCommand(arguments string) tea.Cmd {
	arguments = strings.TrimSpace(arguments)
	switch arguments {
	case "", "runs":
		return a.harnessOverlay()
	case "library":
		return a.harnessLibraryOverlay()
	case "refine":
		return a.newHarnessRun("")
	case "installed":
		return a.openInstalledHarness()
	}
	return a.newHarnessRun(arguments)
}

// openHarnessCompare asks what to compare a finished run's adopted harness
// with: the installed one — what installing would change — or another
// finished run's.
func (a *App) openHarnessCompare(id string) {
	run, ok := a.harnessRunByID(id)
	if !ok {
		return
	}
	compare := func(with string) func() tea.Msg {
		return func() tea.Msg { return a.loadHarnessCompare(id, with) }
	}
	items := []overlayItem{{Label: "Installed harness", Hint: "what installing " + run.Adopted + " would change",
		Value: "installed", Category: "Installed", Action: compare("installed")}}
	for _, other := range a.harnessRuns {
		if other.ID == id || other.Status != "ok" || other.Adopted == "" {
			continue
		}
		items = append(items, overlayItem{Label: other.Task, Hint: fmt.Sprintf("%s k=%d · adopted %s", other.Mode, other.K, other.Adopted),
			Value: other.ID, Category: "Other runs", Footer: formatMoney(other.CostUSD), Action: compare(other.ID)})
	}
	a.openList("Compare "+run.Task+" · "+run.Adopted+" with", items)
	a.overlay.SetSize(dialogLarge)
	a.overlay.SetActions([]dialogAction{{Title: "back", Keys: "ctrl+b", Standalone: true, Right: true,
		OnTrigger: func(overlayItem) tea.Cmd { a.openHarnessRun(id); return nil }}})
}

// loadHarnessCompare fetches a comparison off the update goroutine.
func (a *App) loadHarnessCompare(id, with string) tea.Cmd {
	return func() tea.Msg {
		comparison, err := a.client.CompareHarness(a.ctx, id, with)
		if err != nil {
			return statusMsg{text: "compare failed: " + err.Error()}
		}
		return harnessCompareMsg{comparison: comparison}
	}
}

type harnessCompareMsg struct{ comparison *client.HarnessComparison }

func harnessCompareLines(c client.HarnessComparison) []panelLine {
	lines := []panelLine{
		{Text: "from " + c.From, Tone: toneNote},
		{Text: "to   " + c.To, Tone: toneNote},
		{},
	}
	files := c.Files
	if len(files.Added)+len(files.Modified)+len(files.Removed) == 0 {
		return append(lines, panelLine{Text: "The two harnesses are the same.", Tone: toneBody})
	}
	lines = append(lines, panelLine{Text: "FILES", Tone: toneHeading})
	for _, rel := range files.Added {
		lines = append(lines, panelLine{Text: "+ " + rel, Tone: toneAdd})
	}
	for _, rel := range files.Modified {
		lines = append(lines, panelLine{Text: "~ " + rel, Tone: toneHunk})
	}
	for _, rel := range files.Removed {
		lines = append(lines, panelLine{Text: "- " + rel, Tone: toneDel})
	}
	lines = append(lines, panelLine{Text: fmt.Sprintf("%+d always-on chars", files.AlwaysOnCharsDelta), Tone: toneMuted})
	if c.Diff != "" {
		lines = append(lines, panelLine{}, panelLine{Text: "DIFF", Tone: toneHeading})
		lines = append(lines, diffLines(c.Diff)...)
	}
	return lines
}

// openInstalledHarness shows the project's installed harness: where it is,
// what it brings, its fingerprint (the one the chat's harness marker
// carries) and every file.
func (a *App) openInstalledHarness() tea.Cmd {
	return func() tea.Msg {
		installed, err := a.client.InstalledHarness(a.ctx)
		if err != nil {
			return statusMsg{text: "could not read the installed harness: " + err.Error()}
		}
		return harnessInstalledMsg{installed: installed}
	}
}

type harnessInstalledMsg struct{ installed *client.HarnessInstalled }

func harnessInstalledLines(h client.HarnessInstalled) []panelLine {
	if !h.Installed {
		return []panelLine{
			{Text: "No harness is installed in this project.", Tone: toneBody},
			{},
			{Text: "A finished run's install (ctrl+s in its plan) puts one at " + harnessPlace(h.Dir) + "; new sessions here are then overlaid with it.", Tone: toneMuted},
		}
	}
	facts := harnessFacts(client.HarnessData{Loaded: true, Dir: h.Dir, Fingerprint: h.Fingerprint, AlwaysOn: h.AlwaysOn,
		Skills: h.Skills, Agents: h.Agents, Scripts: h.Scripts, Hooks: h.Hooks})
	lines := []panelLine{{Text: strings.Join(facts, " · "), Tone: toneNote}}
	for _, list := range []struct {
		title string
		names []string
	}{{"Skills", h.Skills}, {"Agents", h.Agents}} {
		if len(list.names) > 0 {
			lines = append(lines, panelLine{Text: list.title + ": " + strings.Join(list.names, ", "), Tone: toneBody})
		}
	}
	paths := make([]string, 0, len(h.Tree))
	for rel, content := range h.Tree {
		if strings.TrimSpace(content) != "" {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	for _, rel := range paths {
		lines = append(lines, panelLine{}, panelLine{Text: "── " + rel, Tone: toneHunk})
		lines = append(lines, proseLines(h.Tree[rel], toneBody)...)
	}
	return lines
}

// activeSessionHarness is the harness the open session runs under now — its
// newest harness marker, when that says loaded — for the hint row.
func (a *App) activeSessionHarness() (client.HarnessData, bool) {
	for i := len(a.timeline) - 1; i >= 0; i-- {
		if a.timeline[i].Type != "harness" {
			continue
		}
		data, err := client.DecodeHarness(a.timeline[i].Data)
		return data, err == nil && data.Loaded
	}
	return client.HarnessData{}, false
}

const harnessLogTitle = "Harness log · "

// openHarnessLog opens a run's event log: every phase, rollout, editor
// session, retry with its validation error, selection and lesson, in order,
// time-stamped. It follows the run while it goes (applyHarnessSnapshot).
func (a *App) openHarnessLog(id string) tea.Cmd {
	return func() tea.Msg {
		events, err := a.client.HarnessLog(a.ctx, id, 0)
		if err != nil {
			return statusMsg{text: "could not read the log: " + err.Error()}
		}
		return harnessLogMsg{id: id, events: events, reset: true}
	}
}

type harnessLogMsg struct {
	id     string
	events []client.HarnessLogEvent
	reset  bool
}

func (a *App) harnessLogOpen() bool {
	o := a.overlay
	return o != nil && o.Kind == dialog.KindHelp && strings.HasPrefix(o.Title, harnessLogTitle)
}

// showHarnessLog (re)opens the log panel. While the reader is at the bottom
// it follows new lines; once they scroll up it holds their place.
func (a *App) showHarnessLog(msg harnessLogMsg) {
	pos, follow := 0, true
	if a.harnessLogOpen() && !msg.reset {
		pos = a.overlay.ScrollPos()
		follow = pos >= a.harnessLogPos
	}
	if msg.reset {
		a.harnessLog, a.harnessLogRun, a.harnessLogPos = nil, msg.id, 0
	}
	a.harnessLog = append(a.harnessLog, msg.events...)
	task := msg.id
	if run, ok := a.harnessRunByID(msg.id); ok {
		task = run.Task
	}
	a.openHelpDialog(harnessLogTitle+task, harnessLogLines(a.harnessLog))
	if follow {
		a.overlay.ScrollEnd()
	} else {
		a.overlay.ScrollDown(pos)
	}
	a.harnessLogPos = pos
}

// harnessLogLines renders one line per event.
func harnessLogLines(events []client.HarnessLogEvent) []string {
	if len(events) == 0 {
		return []string{"no events yet"}
	}
	lines := make([]string, 0, len(events))
	for _, ev := range events {
		stamp := time.UnixMilli(ev.Time).Format("15:04:05")
		mark := "·"
		switch {
		case strings.HasSuffix(ev.Type, ".started") || ev.Type == "session":
			mark = "◐"
		case ev.Type == "stage.retry":
			mark = "↻"
		case strings.HasSuffix(ev.Type, ".ended") || ev.Type == "selected" || ev.Type == "reflected":
			mark = map[bool]string{true: "✓", false: "✗"}[ev.OK]
		case ev.Type == "phase":
			mark = "▸"
		}
		line := stamp + " " + mark + " "
		switch ev.Type {
		case "phase":
			line += "phase " + ev.Phase
		case "session":
			line += ev.Label + " " + ev.Role + " session " + ev.SessionID
		case "stage.retry":
			line += fmt.Sprintf("%s attempt %d rejected", ev.Label, ev.Attempt)
		default:
			line += ev.Type + " " + ev.Label
		}
		if ev.CostUSD > 0 {
			line += " · " + formatMoney(ev.CostUSD)
		}
		if ev.Detail != "" && ev.Type != "phase" {
			line += " · " + strings.Join(strings.Fields(ev.Detail), " ")
		} else if ev.Detail != "" {
			line += " · " + ev.Detail
		}
		lines = append(lines, line)
	}
	return lines
}
