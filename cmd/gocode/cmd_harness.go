package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/harness"
	"github.com/langazov/gocode-go/internal/session"
)

// harnessCommand is the CLI for Harness² (internal/harness): adapt the
// harness for a task, browse the experience library, install what was
// adopted. See documentation/HARNESS2_PLAN.md.
func harnessCommand() *clix.Command {
	runFlags := []clix.Flag{
		{Name: "prompt", Aliases: []string{"p"}, Kind: clix.KindString, Describe: "the task, when no task file is given"},
		{Name: "dir", Kind: clix.KindString, Describe: "workspace each rollout starts from (default: the current directory; a task file's own workspace wins)"},
		{Name: "domain", Kind: clix.KindString, Describe: "area of work whose experience library this task reads and extends (default: the project directory name)"},
		{Name: "id", Kind: clix.KindString, Describe: "task id (default: from the file name or the prompt)"},
		{Name: "k", Kind: clix.KindNumber, Default: float64(1), Describe: "task-level recursion budget: 4k rollouts, 3k editor sessions"},
		{Name: "mode", Kind: clix.KindString, Default: harness.ModeParallel, Choices: []string{harness.ModeParallel, harness.ModeSequential}, Describe: "parallel explores wider, sequential refines deeper"},
		{Name: "model", Aliases: []string{"m"}, Kind: clix.KindString, Describe: "executor model, provider/model"},
		{Name: "improver-model", Kind: clix.KindString, Describe: "model for the editor, selector and reflector sessions (default: config harness.model, else the executor's)"},
		{Name: "variant", Kind: clix.KindString, Describe: "model variant (reasoning effort)"},
		{Name: "library", Kind: clix.KindString, Choices: []string{"auto", "local", "cloud"}, Describe: "experience-library backend (default: config harness.library, else auto)"},
		{Name: "cross-domain", Kind: clix.KindBool, Describe: "also show lessons from other domains (cloud backend only)"},
		{Name: "base", Kind: clix.KindString, Describe: "harness directory to start from (default: the project's installed .gocode/harness, else none)"},
		{Name: "width", Kind: clix.KindNumber, Default: float64(harness.DefaultWidth), Describe: "rollouts and editor sessions run at once"},
		{Name: "keep-workspaces", Kind: clix.KindBool, Describe: "keep every rollout's working copy"},
		{Name: "rollout-timeout", Kind: clix.KindString, Describe: "longest one rollout may run before it is stopped and recorded as failed, e.g. 45m (default: config harness.rolloutTimeout, else 30m)"},
		{Name: "session-timeout", Kind: clix.KindString, Describe: "the same for one editor, selector or reflector session (default 20m)"},
		{Name: "dry-run", Kind: clix.KindBool, Describe: "print the task, the backend and the budget, then exit without spending anything"},
		{Name: "format", Kind: clix.KindString, Default: "default", Choices: []string{"default", "json"}, Describe: "json prints progress events and the summary as JSON lines"},
	}
	selectRun := []clix.Flag{
		{Name: "k", Kind: clix.KindNumber, Default: float64(1), Describe: "the run's recursion budget"},
		{Name: "mode", Kind: clix.KindString, Default: harness.ModeParallel, Choices: []string{harness.ModeParallel, harness.ModeSequential}, Describe: "the run's mode"},
	}
	return &clix.Command{
		Name:     "harness",
		Describe: "adapt gocode's harness to a task (Harness²) and manage adopted harnesses",
		Demand:   true,
		Sub: []*clix.Command{
			{
				Name:        "run",
				Describe:    "refine the harness for one task: propose, probe, compose, select, reflect",
				Positionals: []clix.Positional{{Name: "task", Describe: "task file: markdown prompt with optional frontmatter (id, domain, workspace, work, deliverable)"}},
				Flags:       runFlags,
				Run:         runHarnessRun,
			},
			{
				Name: "sweep",
				Describe: "run many tasks: each domain's tasks in order, each reading the lessons earlier ones stored; " +
					"domains in parallel",
				Positionals: []clix.Positional{{Name: "tasks", Array: true, Required: true, Describe: "task files, or directories of *.md task files"}},
				Flags: append(sweepFlags(runFlags),
					clix.Flag{Name: "workers", Kind: clix.KindNumber, Default: float64(1), Describe: "domains run at once; tasks within a domain stay in order"},
					clix.Flag{Name: "max-tasks", Kind: clix.KindNumber, Describe: "cap per domain: its first N tasks in stream order"},
					clix.Flag{Name: "seed", Kind: clix.KindNumber, Describe: "shuffle the stream reproducibly (model sampling is not seeded)"},
				),
				Run: runHarnessSweep,
			},
			{
				Name:     "runs",
				Describe: "list harness runs and their outcome",
				Flags:    []clix.Flag{{Name: "format", Kind: clix.KindString, Default: "table", Choices: []string{"table", "json"}, Describe: "output format"}},
				Run:      runHarnessRuns,
			},
			{
				Name:        "show",
				Describe:    "show one run: stages, selection, lesson",
				Positionals: []clix.Positional{{Name: "task", Required: true, Describe: "task id"}},
				Flags:       selectRun,
				Run:         runHarnessShow,
			},
			{
				Name:        "install",
				Describe:    "install a run's adopted harness into the project (.gocode/harness) and/or publish its skills to the Library",
				Positionals: []clix.Positional{{Name: "task", Required: true, Describe: "task id"}},
				Flags: append(append([]clix.Flag{}, selectRun...),
					clix.Flag{Name: "to", Kind: clix.KindString, Default: "project", Choices: []string{"project", "library", "both"}, Describe: "where to install"},
					clix.Flag{Name: "force", Kind: clix.KindBool, Describe: "replace an existing installed harness or changed Library skills"},
					clix.Flag{Name: "dir", Kind: clix.KindString, Describe: "project directory (default: the current directory)"},
				),
				Run: runHarnessInstall,
			},
			{
				Name: "prune",
				Describe: "delete harness runs: their rollout and editor sessions and their run directories " +
					"(the experience library is kept)",
				Positionals: []clix.Positional{{Name: "task", Describe: "task id (with --k and --mode); omit with --all"}},
				Flags: append(append([]clix.Flag{}, selectRun...),
					clix.Flag{Name: "all", Kind: clix.KindBool, Describe: "every run"},
					clix.Flag{Name: "sessions-only", Kind: clix.KindBool, Describe: "with --all: drop the sessions but keep run directories, so runs can still be shown and installed"},
				),
				Run: runHarnessPrune,
			},
			{
				Name:        "validate",
				Describe:    "check a harness directory against the editable-surface rules",
				Positionals: []clix.Positional{{Name: "dir", Describe: "harness directory (default: .gocode/harness)"}},
				Run:         runHarnessValidate,
			},
			{
				Name:     "library",
				Describe: "browse and manage the experience library",
				Demand:   true,
				Sub: []*clix.Command{
					{
						Name:        "ls",
						Describe:    "list domains, or one domain's entries",
						Positionals: []clix.Positional{{Name: "domain", Describe: "domain to list"}},
						Flags:       []clix.Flag{{Name: "library", Kind: clix.KindString, Choices: []string{"auto", "local", "cloud"}, Describe: "backend"}},
						Run:         runHarnessLibraryList,
					},
					{
						Name:        "show",
						Describe:    "show one entry's lesson and harness",
						Positionals: []clix.Positional{{Name: "entry", Required: true, Describe: "domain/task"}},
						Run:         runHarnessLibraryShow,
					},
					{
						Name:        "forget",
						Describe:    "delete one entry",
						Positionals: []clix.Positional{{Name: "entry", Required: true, Describe: "domain/task"}},
						Flags:       []clix.Flag{{Name: "library", Kind: clix.KindString, Choices: []string{"auto", "local", "cloud"}, Describe: "backend"}},
						Run:         runHarnessLibraryForget,
					},
					{
						Name:     "sync",
						Describe: "upload pending entries to the gocoder.org Library and pull other machines' (cloud backend)",
						Run:      runHarnessLibrarySync,
					},
				},
			},
		},
	}
}

// harnessTaskFrom builds the task from a task file or --prompt.
func harnessTaskFrom(a *clix.Args, cwd string) (harness.Task, error) {
	var task harness.Task
	if file := a.PositionalOr("task", ""); file != "" {
		loaded, err := harness.LoadTask(file)
		if err != nil {
			return task, err
		}
		task = loaded
	} else {
		prompt := strings.TrimSpace(a.String("prompt"))
		if prompt == "" {
			prompt = readPipedStdin()
		}
		if prompt == "" {
			return task, &usageError{msg: "give a task file or --prompt"}
		}
		task = harness.Task{Prompt: prompt, ID: harness.Slug(prompt)}
	}
	if id := a.String("id"); id != "" {
		task.ID = id
	}
	if domain := a.String("domain"); domain != "" {
		task.Domain = domain
	}
	if task.Domain == "" {
		task.Domain = harness.Slug(filepath.Base(cwd))
	}
	if task.Workspace == "" {
		dir := a.String("dir")
		if dir == "" {
			dir = cwd
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return task, err
		}
		task.Workspace = abs
	}
	return task, nil
}

func runHarnessRun(a *clix.Args) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	task, err := harnessTaskFrom(a, cwd)
	if err != nil {
		return err
	}
	k := a.IntOr("k", 1)
	mode, err := harness.ParseMode(a.String("mode"))
	if err != nil {
		return err
	}
	base, baseDir, err := baseHarness(cwd, a.String("base"))
	if err != nil {
		return err
	}
	if _, err := harness.Validate(base, harness.ValidateOptions{}); err != nil && len(base) > 0 {
		return fmt.Errorf("base harness %s is invalid: %w", baseDir, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := bootStack(ctx, a.String("model"))
	if err != nil {
		return err
	}
	defer st.Close()
	opts, err := harnessRunOptions(a)
	if err != nil {
		return err
	}
	engine, detection, err := newHarnessEngine(ctx, st, opts)
	if err != nil {
		return err
	}
	task.Prompts = task.Prompts.WithDefaults()
	if err := task.Validate(); err != nil {
		return err
	}
	jsonOut := a.String("format") == "json"
	baseLabel := "none (gocode as shipped)"
	if baseDir != "" {
		baseLabel = baseDir
	}
	if !jsonOut || a.Bool("dry-run") {
		fmt.Fprintf(os.Stderr, "harness² · task %s · domain %s\n", task.ID, task.Domain)
		fmt.Fprintf(os.Stderr, "  workspace: %s\n  base:      %s\n", task.Workspace, baseLabel)
		fmt.Fprintf(os.Stderr, "  library:   %s (%s)\n", detection.Backend, detection.Reason)
		fmt.Fprintf(os.Stderr, "  budget:    %s\n", harness.CostLine(mode, k, engine.Store != nil))
		fmt.Fprintf(os.Stderr, "  runs in:   %s\n", engine.Dir(task.ID, k))
	}
	if a.Bool("dry-run") {
		return nil
	}

	engine.OnEvent = func(ev harness.Event) {
		if jsonOut {
			data, _ := json.Marshal(map[string]any{"type": "harness." + ev.Type, "event": ev})
			fmt.Println(string(data))
			return
		}
		if line := describeHarnessEvent(ev); line != "" {
			fmt.Fprintln(os.Stderr, line)
		}
	}
	summary, err := engine.Run(ctx, task, harness.RunOptions{K: k, Mode: mode, Base: base})
	if summary != nil {
		if jsonOut {
			data, _ := json.Marshal(map[string]any{"type": "harness.summary", "summary": summary})
			fmt.Println(string(data))
		} else {
			printHarnessSummary(os.Stdout, summary)
		}
	}
	return err
}

func describeHarnessEvent(ev harness.Event) string {
	mark := "✓"
	if !ev.OK {
		mark = "✗"
	}
	cost := ""
	if ev.CostUSD > 0 {
		cost = fmt.Sprintf(" · $%.2f", ev.CostUSD)
	}
	detail := ""
	if ev.Detail != "" {
		detail = " · " + clipLine(ev.Detail, 100)
	}
	switch ev.Type {
	case harness.EventPhase:
		return "▸ " + ev.Phase + map[bool]string{true: " · " + ev.Detail, false: ""}[ev.Detail != ""]
	case harness.EventRolloutStarted:
		return "  ◐ rollout " + ev.Label
	case harness.EventRolloutEnded:
		return fmt.Sprintf("  %s rollout %s%s%s", mark, ev.Label, cost, detail)
	case harness.EventStageStarted:
		return "  ◐ " + ev.Label
	case harness.EventStageEnded:
		return fmt.Sprintf("  %s %s%s%s", mark, ev.Label, cost, detail)
	case harness.EventSelected:
		return fmt.Sprintf("  %s selected %s%s", mark, ev.Label, detail)
	case harness.EventReflected:
		return fmt.Sprintf("  %s lesson%s", mark, detail)
	case harness.EventSession:
		return fmt.Sprintf("    %s %s session %s", ev.Label, ev.Role, ev.SessionID)
	case harness.EventStageRetry:
		return fmt.Sprintf("  ↻ %s attempt %d rejected · %s", ev.Label, ev.Attempt, clipLine(ev.Detail, 160))
	}
	return ""
}

func clipLine(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func printHarnessSummary(w *os.File, s *harness.Summary) {
	fmt.Fprintf(w, "\n%s · %s k=%d · %s · $%.2f\n", s.Task, s.Mode, s.K, s.Status, s.CostUSD)
	if s.Selection != nil && s.Selection.OK {
		fmt.Fprintf(w, "adopted: %s", s.Selection.ChosenLabel)
		if d, ok := s.Diffs[s.Selection.ChosenLabel]; ok {
			if d.Unchanged() {
				fmt.Fprint(w, " (same as base)")
			} else {
				fmt.Fprintf(w, " (+%d ~%d -%d files, %+d always-on chars)", len(d.Added), len(d.Modified), len(d.Removed), d.AlwaysOnCharsDelta)
			}
		}
		fmt.Fprintln(w)
		if s.Selection.Why != "" {
			fmt.Fprintf(w, "why: %s\n", clipLine(s.Selection.Why, 300))
		}
	}
	if s.Experience != nil {
		fmt.Fprintf(w, "lesson: %s → %s\n", s.Experience.Cue, s.Experience.Lesson)
		if !s.Stored && s.StoreError != "" {
			fmt.Fprintf(w, "lesson not stored: %s\n", s.StoreError)
		}
	}
	if s.SkillsDrifted {
		fmt.Fprintln(w, "warning: Library skills changed during this run; rollouts did not all see the same skills")
	}
	for _, e := range s.Errors {
		fmt.Fprintf(w, "error: %s\n", e)
	}
	if s.Adopted != "" {
		fmt.Fprintf(w, "install with: gocode harness install %s --k %d --mode %s\n", s.Task, s.K, s.Mode)
	}
}

// harnessSummaries reads every run summary under the runs root.
func harnessSummaries() ([]*harness.Summary, error) {
	paths, err := filepath.Glob(filepath.Join(harnessRoot(), "runs", "*", "k*", "*", "summary.json"))
	if err != nil {
		return nil, err
	}
	var out []*harness.Summary
	for _, path := range paths {
		if s, err := harness.LoadSummary(path); err == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

func runHarnessRuns(a *clix.Args) error {
	summaries, err := harnessSummaries()
	if err != nil {
		return err
	}
	if a.String("format") == "json" {
		return json.NewEncoder(os.Stdout).Encode(summaries)
	}
	if len(summaries) == 0 {
		fmt.Println("no harness runs yet — start one with `gocode harness run`")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TASK\tDOMAIN\tMODE\tK\tSTATUS\tADOPTED\tCOST\tSTARTED")
	for _, s := range summaries {
		adopted := "-"
		if s.Selection != nil && s.Selection.OK {
			adopted = s.Selection.ChosenLabel
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t$%.2f\t%s\n", s.Task, s.Domain, s.Mode, s.K, s.Status, adopted, s.CostUSD,
			s.StartedAt.Local().Format("2006-01-02 15:04"))
	}
	return tw.Flush()
}

func loadHarnessRun(a *clix.Args) (*harness.Summary, error) {
	engine := &harness.Engine{Root: filepath.Join(harnessRoot(), "runs")}
	mode, err := harness.ParseMode(a.String("mode"))
	if err != nil {
		return nil, err
	}
	task := a.PositionalOr("task", "")
	summary, err := harness.LoadSummary(engine.SummaryPath(task, a.IntOr("k", 1), mode))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no %s k=%d run for task %q (see `gocode harness runs`)", mode, a.IntOr("k", 1), task)
	}
	return summary, err
}

func runHarnessShow(a *clix.Args) error {
	summary, err := loadHarnessRun(a)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STAGE\tDIM\tOUTCOME\tATTEMPTS\tCOST\tNOTES")
	for _, stage := range summary.Stages {
		outcome := "edited"
		switch {
		case stage.FellBack:
			outcome = "fell back"
		case stage.KeptBase:
			outcome = "kept base"
		}
		notes := stage.Notes
		if stage.FellBack {
			notes = stage.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t$%.2f\t%s\n", stage.Label, stage.Dim, outcome, stage.Attempts, stage.CostUSD, clipLine(notes, 80))
	}
	_ = tw.Flush()
	printHarnessSummary(os.Stdout, summary)
	return nil
}

func runHarnessInstall(a *clix.Args) error {
	summary, err := loadHarnessRun(a)
	if err != nil {
		return err
	}
	dir := a.String("dir")
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	ctx := context.Background()
	to := a.String("to")
	var cloud func() (*harness.Cloud, error)
	if to == "library" || to == "both" {
		st, err := bootStack(ctx, "")
		if err != nil {
			return err
		}
		defer st.Close()
		cloud = func() (*harness.Cloud, error) { return harnessCloud(ctx, st) }
	}
	result, err := installHarness(ctx, dir, summary, harness.InstallRequest{To: to, Force: a.Bool("force")}, cloud)
	printInstallResult(os.Stdout, result)
	return err
}

// harnessCloud is the Library handle for publishing, or an error naming why
// the Library is unavailable.
func harnessCloud(ctx context.Context, st *stack) (*harness.Cloud, error) {
	_, cloud, detection, err := harnessBackend(ctx, st, "")
	if err != nil {
		return nil, err
	}
	if cloud == nil || detection.Backend == harness.BackendLocal {
		return nil, fmt.Errorf("cannot publish to the Library: %s", detection.Reason)
	}
	return cloud, nil
}

// installHarness puts a finished run's adopted harness into <dir>/.gocode/
// harness and/or publishes its skills to the Library. An existing, different
// installed harness is replaced only with Force, and so is a changed Library
// skill; DryRun reports all of it without writing. Shared by the CLI and the
// HTTP API.
func installHarness(ctx context.Context, dir string, summary *harness.Summary, req harness.InstallRequest, cloud func() (*harness.Cloud, error)) (harness.InstallResult, error) {
	var result harness.InstallResult
	tree, err := summary.AdoptedTree()
	if err != nil {
		return result, err
	}
	if req.To == "" {
		req.To = "project"
	}
	if req.To == "project" || req.To == "both" {
		target := filepath.Join(dir, filepath.FromSlash(harness.InstalledDir))
		current, _ := harness.ReadTree(target, nil)
		result.Target = target
		result.Files = harness.DiffTrees(current, tree)
		result.Conflict = !result.Files.Unchanged() && len(current) > 0 && !harnessEmpty(current)
		result.Collisions = skillCollisions(dir, tree)
		if over := installedAlwaysOn(dir, tree); over > harness.AlwaysOnMax {
			result.Warnings = append(result.Warnings, fmt.Sprintf(
				"always-on text (instructions + harness) is %d chars, over the %d cap the harness was validated against", over, harness.AlwaysOnMax))
		}
		if result.Conflict && !req.Force && !req.DryRun {
			return result, fmt.Errorf("%s already holds a different harness; replace it with force", target)
		}
		if !req.DryRun && !result.Files.Unchanged() {
			if _, err := harness.WriteTree(tree, target, false); err != nil {
				return result, err
			}
			result.Written = true
		}
	}
	if req.To == "library" || req.To == "both" {
		if cloud == nil {
			return result, errors.New("no Library connection")
		}
		c, err := cloud()
		if err != nil {
			return result, err
		}
		var report harness.PublishReport
		if req.DryRun {
			report, err = c.SkillStatus(ctx, tree)
		} else {
			report, err = c.PublishSkills(ctx, tree, req.Force)
		}
		result.Library = &report
		if err != nil {
			return result, err
		}
		if len(report.Blocked) > 0 {
			return result, fmt.Errorf("%d changed skill(s) not replaced in the Library; replace them with force", len(report.Blocked))
		}
	}
	return result, nil
}

func printInstallResult(w *os.File, r harness.InstallResult) {
	if r.Target != "" {
		for _, rel := range r.Files.Added {
			fmt.Fprintf(w, "  + %s\n", rel)
		}
		for _, rel := range r.Files.Modified {
			fmt.Fprintf(w, "  ~ %s\n", rel)
		}
		for _, rel := range r.Files.Removed {
			fmt.Fprintf(w, "  - %s\n", rel)
		}
		if len(r.Collisions) > 0 {
			fmt.Fprintf(w, "note: your own %s shadow the installed harness's copies (your files win)\n", strings.Join(r.Collisions, ", "))
		}
		switch {
		case r.Written:
			fmt.Fprintf(w, "installed into %s\n", r.Target)
		case r.Files.Unchanged():
			fmt.Fprintf(w, "%s already holds this harness\n", r.Target)
		}
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
	if r.Library != nil {
		printPublishReport(w, *r.Library)
	}
}

func harnessEmpty(tree harness.Tree) bool {
	for _, content := range tree {
		if strings.TrimSpace(content) != "" {
			return false
		}
	}
	return true
}

// skillCollisions names the tree's skills and agents the project already
// defines itself.
func skillCollisions(dir string, tree harness.Tree) []string {
	var out []string
	for _, name := range harness.SkillNames(tree) {
		for _, root := range []string{".gocode/skills", ".gocode/skill", ".agents/skills"} {
			if _, err := os.Stat(filepath.Join(dir, root, name, "SKILL.md")); err == nil {
				out = append(out, "skill "+name)
				break
			}
		}
	}
	for _, name := range harness.AgentNames(tree) {
		if _, err := os.Stat(filepath.Join(dir, ".gocode", "agent", name+".md")); err == nil {
			out = append(out, "agent "+name)
		}
	}
	return out
}

// installedAlwaysOn is the always-on text a session in dir will carry: its
// instruction files plus the harness.
func installedAlwaysOn(dir string, tree harness.Tree) int {
	total := len(harness.AlwaysOnText(tree))
	if source := instructionsFor(dir, nil); source != nil {
		for _, block := range source.System() {
			total += len(block)
		}
	}
	return total
}

func printPublishReport(w *os.File, r harness.PublishReport) {
	for _, line := range []struct {
		label string
		names []string
	}{{"published", r.New}, {"updated", r.Changed}, {"unchanged", r.Unchanged}, {"changed, not replaced (needs --force)", r.Blocked}} {
		if len(line.names) > 0 {
			fmt.Fprintf(w, "Library skills %s: %s\n", line.label, strings.Join(line.names, ", "))
		}
	}
}

func runHarnessValidate(a *clix.Args) error {
	dir := a.PositionalOr("dir", harness.InstalledDir)
	var off []string
	tree, err := harness.ReadTree(dir, &off)
	if err != nil {
		return err
	}
	if len(tree) == 0 {
		return fmt.Errorf("%s holds no harness", dir)
	}
	for _, rel := range off {
		fmt.Printf("ignored (off the editable surface): %s\n", rel)
	}
	census, err := harness.Validate(tree, harness.ValidateOptions{})
	var verr *harness.ValidationError
	if errors.As(err, &verr) {
		for _, problem := range verr.Problems {
			fmt.Printf("✗ %s\n", problem)
		}
		return fmt.Errorf("%d problem(s)", len(verr.Problems))
	}
	if err != nil {
		return err
	}
	fmt.Printf("✓ %s: %d files, %d skills, %d subagents, %d scripts, hooks %v, %d always-on chars\n",
		dir, census.Files, census.Skills, census.Agents, census.Scripts, census.Hooks > 0, census.AlwaysOnChars)
	return nil
}

// harnessStore opens the experience library for the library subcommands.
func harnessStore(setting string) (harness.ExperienceStore, func(), error) {
	ctx := context.Background()
	st, err := bootStack(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	store, _, _, err := harnessBackend(ctx, st, setting)
	if err != nil {
		st.Close()
		return nil, nil, err
	}
	return store, func() { st.Close() }, nil
}

func splitEntry(entry string) (string, string, error) {
	domain, task, ok := strings.Cut(entry, "/")
	if !ok || domain == "" || task == "" {
		return "", "", &usageError{msg: "expected domain/task"}
	}
	return domain, task, nil
}

func runHarnessLibraryList(a *clix.Args) error {
	store, done, err := harnessStore(a.String("library"))
	if err != nil {
		return err
	}
	defer done()
	ctx := context.Background()
	domain := a.PositionalOr("domain", "")
	if domain == "" {
		domains, err := store.Domains(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("experience library (%s)\n", store.Backend())
		if len(domains) == 0 {
			fmt.Println("  empty — entries appear after `gocode harness run` adopts a harness with a lesson")
		}
		for _, d := range domains {
			entries, _ := store.List(ctx, d)
			fmt.Printf("  %s (%d)\n", d, len(entries))
		}
		return nil
	}
	entries, err := store.List(ctx, domain)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ENTRY\tCOMPONENTS\tCUE")
	for _, e := range entries {
		name := e.Domain + "/" + e.Task
		if e.Pending {
			name += " (pending upload)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, e.Components, clipLine(e.Cue, 80))
	}
	return tw.Flush()
}

func runHarnessLibraryShow(a *clix.Args) error {
	domain, task, err := splitEntry(a.PositionalOr("entry", ""))
	if err != nil {
		return err
	}
	store, done, err := harnessStore("")
	if err != nil {
		return err
	}
	defer done()
	entry, err := store.Get(context.Background(), domain, task)
	if err != nil {
		return err
	}
	fmt.Printf("%s/%s · adopted %s\n\ncue:        %s\nlesson:     %s\nevidence:   %s\nconfidence: %s\n",
		domain, task, entry.Label, entry.Lesson.Cue, entry.Lesson.Lesson, entry.Lesson.Evidence, entry.Lesson.Confidence)
	if entry.Why != "" {
		fmt.Printf("\nwhy adopted:\n%s\n", entry.Why)
	}
	fmt.Println("\nharness:")
	for _, rel := range entry.Tree.Paths() {
		if strings.TrimSpace(entry.Tree[rel]) != "" {
			fmt.Printf("  %s (%d chars)\n", rel, len(entry.Tree[rel]))
		}
	}
	return nil
}

func runHarnessLibraryForget(a *clix.Args) error {
	domain, task, err := splitEntry(a.PositionalOr("entry", ""))
	if err != nil {
		return err
	}
	store, done, err := harnessStore(a.String("library"))
	if err != nil {
		return err
	}
	defer done()
	if err := store.Forget(context.Background(), domain, task); err != nil {
		return err
	}
	fmt.Printf("forgot %s/%s\n", domain, task)
	return nil
}

func runHarnessLibrarySync(*clix.Args) error {
	store, done, err := harnessStore("cloud")
	if err != nil {
		return err
	}
	defer done()
	cloud, ok := store.(*harness.CloudStore)
	if !ok {
		return fmt.Errorf("sync needs the cloud backend")
	}
	pushed, err := cloud.Sync(context.Background())
	fmt.Printf("uploaded %d pending entr%s\n", pushed, map[bool]string{true: "y", false: "ies"}[pushed == 1])
	return err
}

func runHarnessPrune(a *clix.Args) error {
	ctx := context.Background()
	task := a.PositionalOr("task", "")
	all := a.Bool("all")
	if (task == "") == !all {
		return &usageError{msg: "give a task id, or --all"}
	}
	if a.Bool("sessions-only") && !all {
		return &usageError{msg: "--sessions-only goes with --all"}
	}
	database, err := db.OpenDefault(ctx)
	if err != nil {
		return err
	}
	defer database.Close()
	runs := filepath.Join(harnessRoot(), "runs")
	if all {
		sessions, err := session.DeleteUnder(ctx, database, runs)
		if err != nil {
			return err
		}
		if !a.Bool("sessions-only") {
			if err := os.RemoveAll(runs); err != nil {
				return err
			}
			fmt.Printf("deleted every harness run (%d sessions); the experience library is kept\n", sessions)
			return nil
		}
		fmt.Printf("deleted %d harness sessions; run directories are kept\n", sessions)
		return nil
	}
	mode, err := harness.ParseMode(a.String("mode"))
	if err != nil {
		return err
	}
	service := &harness.Service{Root: runs, Prune: func(ctx context.Context, dir string) (int64, error) {
		return session.DeleteUnder(ctx, database, dir)
	}}
	deleted, err := service.Delete(ctx, harness.RunID(task, a.IntOr("k", 1), mode))
	if err != nil {
		return err
	}
	fmt.Printf("deleted %s (%d sessions)\n", strings.Join(deleted.Removed, ", "), deleted.Sessions)
	return nil
}

// sweepFlags is the run flags minus the ones that name a single task.
func sweepFlags(runFlags []clix.Flag) []clix.Flag {
	var out []clix.Flag
	for _, f := range runFlags {
		switch f.Name {
		case "prompt", "id":
			continue
		case "domain":
			f.Describe = "domain for task files that do not set one (default: the project directory name)"
		case "dir":
			f.Describe = "workspace for task files that do not set one (default: the current directory)"
		}
		out = append(out, f)
	}
	return out
}

// loadSweepTasks reads task files, expanding directories to their *.md
// files, and fills each task's domain and workspace defaults.
func loadSweepTasks(paths []string, defaultDomain, defaultWorkspace string) ([]harness.Task, error) {
	var files []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, path)
			continue
		}
		matches, err := filepath.Glob(filepath.Join(path, "*.md"))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		files = append(files, matches...)
	}
	tasks := make([]harness.Task, 0, len(files))
	for _, file := range files {
		task, err := harness.LoadTask(file)
		if err != nil {
			return nil, err
		}
		if task.Domain == "" {
			task.Domain = defaultDomain
		}
		if task.Workspace == "" {
			task.Workspace = defaultWorkspace
		}
		task.Prompts = task.Prompts.WithDefaults()
		tasks = append(tasks, task)
	}
	return tasks, harness.ValidateTasks(tasks)
}

func runHarnessSweep(a *clix.Args) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	domain := a.String("domain")
	if domain == "" {
		domain = harness.Slug(filepath.Base(cwd))
	}
	workspace := a.String("dir")
	if workspace == "" {
		workspace = cwd
	}
	if workspace, err = filepath.Abs(workspace); err != nil {
		return err
	}
	tasks, err := loadSweepTasks(a.Array("tasks"), domain, workspace)
	if err != nil {
		return err
	}
	k := a.IntOr("k", 1)
	mode, err := harness.ParseMode(a.String("mode"))
	if err != nil {
		return err
	}
	var seed *int64
	if a.Has("seed") {
		v := int64(a.Number("seed"))
		seed = &v
	}
	maxTasks := a.IntOr("max-tasks", 0)
	base, baseDir, err := baseHarness(cwd, a.String("base"))
	if err != nil {
		return err
	}
	if _, err := harness.Validate(base, harness.ValidateOptions{}); err != nil && len(base) > 0 {
		return fmt.Errorf("base harness %s is invalid: %w", baseDir, err)
	}

	domains, byDomain := harness.Stream(tasks, seed, maxTasks)
	total := 0
	for _, d := range domains {
		total += len(byDomain[d])
	}
	fmt.Fprintf(os.Stderr, "harness² sweep · %d tasks in %d domains · %s each\n", total, len(domains), harness.CostLine(mode, k, true))
	fmt.Fprintf(os.Stderr, "  in all: %d rollouts, %d editor sessions\n", total*harness.ExpectedRollouts(k), total*harness.ExpectedSessions(k))
	for _, d := range domains {
		ids := make([]string, 0, len(byDomain[d]))
		for _, task := range byDomain[d] {
			ids = append(ids, task.ID)
		}
		fmt.Fprintf(os.Stderr, "  %s: %s\n", d, strings.Join(ids, " → "))
	}
	if a.Bool("dry-run") {
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	st, err := bootStack(ctx, a.String("model"))
	if err != nil {
		return err
	}
	defer st.Close()
	opts, err := harnessRunOptions(a)
	if err != nil {
		return err
	}
	engine, detection, err := newHarnessEngine(ctx, st, opts)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "  library: %s (%s)\n", detection.Backend, detection.Reason)
	jsonOut := a.String("format") == "json"
	engine.OnEvent = func(ev harness.Event) {
		if jsonOut {
			data, _ := json.Marshal(map[string]any{"type": "harness." + ev.Type, "event": ev})
			fmt.Println(string(data))
		}
	}
	result, err := engine.Sweep(ctx, tasks, harness.SweepOptions{
		K: k, Mode: mode, Workers: a.IntOr("workers", 1), MaxTasks: maxTasks, Seed: seed, Base: base,
		Dir: filepath.Join(harnessRoot(), "sweeps"),
		OnTask: func(row harness.SweepRow) {
			if jsonOut {
				data, _ := json.Marshal(map[string]any{"type": "harness.sweep.task", "row": row})
				fmt.Println(string(data))
				return
			}
			line := fmt.Sprintf("%s %s/%s · %s · $%.2f", harnessStatusMark(row.Status), row.Domain, row.Task, row.Status, row.CostUSD)
			if row.Adopted != "" {
				line += " · adopted " + row.Adopted
			}
			if row.Error != "" {
				line += " · " + clipLine(row.Error, 100)
			}
			fmt.Fprintln(os.Stderr, line)
		},
	})
	if result != nil {
		if jsonOut {
			data, _ := json.Marshal(map[string]any{"type": "harness.sweep", "result": result})
			fmt.Println(string(data))
		} else {
			fmt.Printf("\nsweep: %d done, %d failed, $%.2f\n", result.Done, result.Failed, result.CostUSD)
		}
	}
	return err
}

func harnessStatusMark(status string) string {
	if status == "ok" {
		return "✓"
	}
	return "✗"
}

// harnessRunOptions reads the flags run and sweep share.
func harnessRunOptions(a *clix.Args) (harnessOptions, error) {
	opts := harnessOptions{
		Library: a.String("library"), Model: a.String("model"), ImproverModel: a.String("improver-model"),
		Variant: a.String("variant"), Width: a.IntOr("width", harness.DefaultWidth),
		CrossDomain: a.Bool("cross-domain"), KeepWorkspace: a.Bool("keep-workspaces"),
	}
	for _, f := range []struct {
		name string
		dst  *time.Duration
	}{{"rollout-timeout", &opts.RolloutTimeout}, {"session-timeout", &opts.SessionTimeout}} {
		if raw := a.String(f.name); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 {
				return opts, &usageError{msg: "--" + f.name + " must be a duration like 30m"}
			}
			*f.dst = d
		}
	}
	return opts, nil
}
