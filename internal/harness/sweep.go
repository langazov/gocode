package harness

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Sweeps run many tasks, porting run_sweep / run_domain / stream_order in
// harness2/core/schedule.py: tasks are grouped by domain, each domain's
// tasks run one after another so every task reads the experience library
// its predecessors wrote, and different domains run concurrently.
//
// One departure: harness2 trims each domain's library to the entries that
// precede the current task in the stream (Library.retain), because its
// libraries are per-run. gocode's experience library is the user's own and
// outlives a sweep, so nothing is forgotten — a task also reads entries
// earlier sweeps left.

// SweepOptions configure a sweep.
type SweepOptions struct {
	K    int
	Mode string
	// Workers bounds how many domains run at once; tasks within a domain
	// always run in order. Zero means one.
	Workers int
	// MaxTasks caps tasks per domain (the first N in stream order).
	MaxTasks int
	// Seed shuffles the stream reproducibly; nil keeps the reference order.
	Seed *int64
	Base Tree
	// OnTask is called as each task finishes.
	OnTask func(SweepRow)
	// Dir, when set, receives the sweep record as <started>.json, or as
	// <Name>.json when Name is set (the id a server gave the sweep).
	Dir  string
	Name string
	// Source says what the tasks were loaded from, for the record.
	Source string
}

// SweepRow is one task's outcome.
type SweepRow struct {
	Task          string  `json:"task"`
	Domain        string  `json:"domain"`
	Status        string  `json:"status"`
	Adopted       string  `json:"adopted,omitempty"`
	Lesson        string  `json:"lesson,omitempty"`
	CostUSD       float64 `json:"costUSD"`
	Error         string  `json:"error,omitempty"`
	LibraryBefore int     `json:"libraryBefore"`
}

// SweepResult is the durable record of a sweep.
type SweepResult struct {
	K           int        `json:"k"`
	Mode        string     `json:"mode"`
	Workers     int        `json:"workers,omitempty"`
	Source      string     `json:"source,omitempty"`
	Seed        *int64     `json:"seed,omitempty"`
	DomainOrder []string   `json:"domainOrder"`
	StartedAt   time.Time  `json:"startedAt"`
	FinishedAt  time.Time  `json:"finishedAt"`
	Rows        []SweepRow `json:"rows"`
	Done        int        `json:"done"`
	Failed      int        `json:"failed"`
	CostUSD     float64    `json:"costUSD"`
}

// Stream groups tasks by domain and orders them (stream_order): without a
// seed, domains by descending task count then name and tasks by id; with
// one, each shuffle starts from that sorted order and is seeded by (seed,
// domain), so the result depends only on the seed and the task set.
func Stream(tasks []Task, seed *int64, maxTasks int) ([]string, map[string][]Task) {
	byDomain := map[string][]Task{}
	for _, task := range tasks {
		byDomain[task.Domain] = append(byDomain[task.Domain], task)
	}
	domains := make([]string, 0, len(byDomain))
	for domain, list := range byDomain {
		sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
		domains = append(domains, domain)
	}
	sort.Slice(domains, func(i, j int) bool {
		a, b := len(byDomain[domains[i]]), len(byDomain[domains[j]])
		if a != b {
			return a > b
		}
		return domains[i] < domains[j]
	})
	if seed != nil {
		sorted := append([]string(nil), domains...)
		sort.Strings(sorted)
		domains = sorted
		seeded(*seed, "__domains__").Shuffle(len(domains), func(i, j int) { domains[i], domains[j] = domains[j], domains[i] })
		for _, domain := range domains {
			list := byDomain[domain]
			seeded(*seed, domain).Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
		}
	}
	if maxTasks > 0 {
		for domain, list := range byDomain {
			if len(list) > maxTasks {
				byDomain[domain] = list[:maxTasks]
			}
		}
	}
	return domains, byDomain
}

func seeded(seed int64, key string) *rand.Rand {
	h := fnv.New64a()
	fmt.Fprintf(h, "%d:%s", seed, key)
	return rand.New(rand.NewSource(int64(h.Sum64() >> 1)))
}

// LoadTasks reads task files: each path is a task file, or a directory
// whose *.md files are tasks (sorted). A task without a domain or workspace
// takes the defaults. The set is validated (ValidateTasks).
func LoadTasks(paths []string, defaultDomain, defaultWorkspace string) ([]Task, error) {
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
	tasks := make([]Task, 0, len(files))
	for _, file := range files {
		task, err := LoadTask(file)
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
	return tasks, ValidateTasks(tasks)
}

// ValidateTasks checks every task and that ids are unique: a task's id names
// its run directory, so two tasks with one id would share one.
func ValidateTasks(tasks []Task) error {
	seen := map[string]bool{}
	for _, task := range tasks {
		if err := task.Validate(); err != nil {
			return fmt.Errorf("task %s: %w", task.ID, err)
		}
		if seen[task.ID] {
			return fmt.Errorf("two tasks have the id %q; give one an id in its frontmatter", task.ID)
		}
		seen[task.ID] = true
	}
	if len(tasks) == 0 {
		return fmt.Errorf("no tasks to run")
	}
	return nil
}

// Sweep runs every task. A failed task is recorded and the domain carries
// on; cancelling ctx stops the sweep after the tasks in flight.
func (e *Engine) Sweep(ctx context.Context, tasks []Task, opts SweepOptions) (*SweepResult, error) {
	for i := range tasks {
		tasks[i].Prompts = tasks[i].Prompts.WithDefaults()
	}
	if err := ValidateTasks(tasks); err != nil {
		return nil, err
	}
	mode, err := ParseMode(opts.Mode)
	if err != nil {
		return nil, err
	}
	if opts.K < 1 {
		opts.K = 1
	}
	domains, byDomain := Stream(tasks, opts.Seed, opts.MaxTasks)
	result := &SweepResult{K: opts.K, Mode: mode, Workers: opts.Workers, Source: opts.Source, Seed: opts.Seed,
		DomainOrder: domains, StartedAt: time.Now().UTC()}
	rows := map[string][]SweepRow{}
	var mu sync.Mutex
	workers := opts.Workers
	if workers < 1 {
		workers = 1
	}
	parallel(len(domains), workers, func(i int) {
		domain := domains[i]
		for _, task := range byDomain[domain] {
			if ctx.Err() != nil {
				return
			}
			row := SweepRow{Task: task.ID, Domain: domain}
			if e.Store != nil {
				if entries, err := e.Store.List(ctx, domain); err == nil {
					row.LibraryBefore = len(entries)
				}
			}
			summary, err := e.Run(ctx, task, RunOptions{K: opts.K, Mode: mode, Base: opts.Base})
			switch {
			case err != nil && summary == nil:
				row.Status, row.Error = "failed", err.Error()
			case summary != nil:
				row.Status, row.CostUSD = summary.Status, summary.CostUSD
				if err != nil {
					row.Error = err.Error()
				} else if len(summary.Errors) > 0 {
					row.Error = summary.Errors[0]
				}
				if summary.Selection != nil && summary.Selection.OK {
					row.Adopted = summary.Selection.ChosenLabel
				}
				if summary.Experience != nil {
					row.Lesson = summary.Experience.Cue
				}
			}
			mu.Lock()
			rows[domain] = append(rows[domain], row)
			mu.Unlock()
			if opts.OnTask != nil {
				opts.OnTask(row)
			}
		}
	})
	for _, domain := range domains {
		for _, row := range rows[domain] {
			result.Rows = append(result.Rows, row)
			result.CostUSD += row.CostUSD
			if row.Status == "ok" {
				result.Done++
			} else {
				result.Failed++
			}
		}
	}
	result.FinishedAt = time.Now().UTC()
	if opts.Dir != "" {
		name := opts.Name
		if name == "" {
			name = result.StartedAt.Format("20060102T150405Z")
		}
		if err := writeJSON(filepath.Join(opts.Dir, name+".json"), result); err != nil {
			return result, err
		}
	}
	return result, ctx.Err()
}
