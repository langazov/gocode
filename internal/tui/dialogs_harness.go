package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/dialog"
)

// The Harness² surfaces (documentation/HARNESS2_PLAN.md §6): /harness lists
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
		{Title: "cancel", Keys: "ctrl+d", OnTrigger: a.cancelHarnessAction},
		{Title: "library", Keys: "ctrl+l", Standalone: true, OnTrigger: func(overlayItem) tea.Cmd { return a.harnessLibraryOverlay() }},
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
	items := make([]overlayItem, 0, len(run.Rows)+2)
	for _, row := range run.Rows {
		row := row
		hint := row.State
		if row.Detail != "" {
			hint += " · " + strings.Join(strings.Fields(row.Detail), " ")
		}
		footer := ""
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
	title := harnessRunTitle + " · " + run.Task
	a.openList(title, items)
	o := a.overlay
	o.SetSize(dialogLarge)
	o.SetPlaceholder(fmt.Sprintf("%s · %s · %s", run.Budget, run.Backend, formatMoney(run.CostUSD)))
	if run.Adopted != "" {
		o.SetCurrent(run.Adopted)
	}
	actions := []dialogAction{
		{Title: "session", Keys: "ctrl+e", OnTrigger: func(item overlayItem) tea.Cmd { return a.openHarnessSession(id, item.Value) }},
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
	actions = append(actions, dialogAction{Title: "back", Keys: "ctrl+b", Standalone: true, Right: true,
		OnTrigger: func(overlayItem) tea.Cmd { a.harnessOpenRun = ""; return a.harnessOverlay() }})
	o.SetActions(actions)
	if len(items) == 0 {
		o.SetEmptyView("Nothing planned", "This run has no rows.")
	}
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
		return nil
	}
	a.harnessSeq = snapshot.HarnessSeq
	a.harnessLoaded = true
	ended := a.mergeHarnessRuns(snapshot.Harness)
	a.refreshHarnessDialogs()
	if len(ended) == 0 {
		return nil
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
	return a.showToastOptions(opts)
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
		return ""
	}
	done := 0
	for _, row := range run.Rows {
		if row.State == "ok" || row.State == "failed" {
			done++
		}
	}
	text := fmt.Sprintf("harness %s · %s %d/%d · %s", truncateRunes(run.Task, 24), run.Phase, done, len(run.Rows), formatMoney(run.CostUSD))
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
		start := client.HarnessStart{Prompt: value, K: 1, Mode: "parallel", DryRun: true}
		if a.active != nil {
			start.Workspace = a.active.Directory
		}
		plan, err := a.client.StartHarness(a.ctx, start)
		if err != nil {
			return statusMsg{text: "cannot plan the run: " + err.Error()}
		}
		return harnessPlanMsg{start: start, plan: *plan}
	})
	return nil
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

func (a *App) cancelHarnessAction(item overlayItem) tea.Cmd {
	run, ok := a.harnessRunByID(item.Value)
	if !ok || run.Status != "running" {
		return staticMsg(statusMsg{text: "that run is not running"})
	}
	o := a.overlay
	if o != nil && o.Armed() != item.Value {
		o.Arm(item.Value, "ctrl+d")
		return nil
	}
	if o != nil {
		o.Disarm()
	}
	return a.cancelHarnessRun(item.Value)
}

func (a *App) cancelHarnessRun(id string) tea.Cmd {
	return func() tea.Msg {
		if err := a.client.CancelHarness(a.ctx, id); err != nil {
			return statusMsg{text: "cancel failed: " + err.Error()}
		}
		return statusMsg{text: "cancelling harness run"}
	}
}

// openHarnessSession opens the rollout session behind a row.
func (a *App) openHarnessSession(id, label string) tea.Cmd {
	return func() tea.Msg {
		stage, err := a.client.HarnessStage(a.ctx, id, label)
		if err != nil {
			return statusMsg{text: "could not read " + label + ": " + err.Error()}
		}
		if stage.SessionID == "" {
			return statusMsg{text: label + " has no rollout session yet"}
		}
		session, err := a.client.Session(a.ctx, stage.SessionID)
		if err != nil {
			return statusMsg{text: "could not open the rollout session: " + err.Error()}
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
func harnessStageLines(stage client.HarnessStage) []string {
	var lines []string
	section := func(title string, body []string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, strings.ToUpper(title))
		lines = append(lines, body...)
	}
	files := stage.Files
	if len(files.Added)+len(files.Modified)+len(files.Removed) == 0 {
		section("Harness", []string{"same as base"})
	} else {
		var body []string
		for _, rel := range files.Added {
			body = append(body, "+ "+rel)
		}
		for _, rel := range files.Modified {
			body = append(body, "~ "+rel)
		}
		for _, rel := range files.Removed {
			body = append(body, "- "+rel)
		}
		body = append(body, fmt.Sprintf("%+d always-on chars", files.AlwaysOnCharsDelta))
		section("Harness", body)
	}
	if stage.Notes != "" {
		section("Editor notes", splitPanelLines(stage.Notes))
	}
	if stage.Error != "" {
		section("Error", splitPanelLines(stage.Error))
	}
	if stage.Diff != "" {
		section("Harness diff", splitPanelLines(stage.Diff))
	}
	if stage.Deliverable != "" {
		status := "delivered"
		if !stage.Delivered {
			status = "nothing delivered"
		}
		section("Rollout · "+status+" · "+formatMoney(stage.CostUSD), splitPanelLines(stage.Deliverable))
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

func harnessEntryLines(entry client.HarnessEntry) []string {
	e := entry.Entry
	lines := []string{
		"LESSON",
		"cue:        " + e.Lesson.Cue,
		"lesson:     " + e.Lesson.Lesson,
		"evidence:   " + e.Lesson.Evidence,
		"confidence: " + e.Lesson.Confidence,
	}
	if e.Why != "" {
		lines = append(lines, "", "WHY "+strings.ToUpper(e.Label)+" WAS ADOPTED")
		lines = append(lines, splitPanelLines(e.Why)...)
	}
	if e.Lesson.Edits != "" {
		lines = append(lines, "", "WHAT EACH EDIT DID")
		lines = append(lines, splitPanelLines(e.Lesson.Edits)...)
	}
	paths := make([]string, 0, len(entry.Tree))
	for rel, content := range entry.Tree {
		if strings.TrimSpace(content) != "" {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	lines = append(lines, "", "HARNESS")
	for _, rel := range paths {
		lines = append(lines, "", "── "+rel)
		lines = append(lines, splitPanelLines(entry.Tree[rel])...)
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
	case harnessPlanMsg:
		a.confirmHarnessPlan(msg)
		return nil, true
	case harnessInstallPlanMsg:
		return a.confirmHarnessInstall(msg), true
	case harnessStageMsg:
		a.openHelpDialog(msg.stage.Label, harnessStageLines(msg.stage))
		return nil, true
	case harnessEntryMsg:
		a.openHelpDialog(msg.entry.Entry.Domain+"/"+msg.entry.Entry.Task, harnessEntryLines(msg.entry))
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
	}
	return a.newHarnessRun(arguments)
}
