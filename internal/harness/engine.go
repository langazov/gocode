package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/langazov/gocode-go/internal/markdown"
)

// The engine's seams. internal/harness never boots gocode itself: the CLI
// (cmd/gocode) implements Executor with a stack rooted in the rollout's
// workspace and Improver with a stack rooted in the evidence workspace, and
// the tests implement both with fakes. Ports the method/substrate split in
// harness2/core/spec.py.

// Modes are the two task-level recursion modes (recursion.run_mode).
const (
	ModeParallel   = "parallel"   // harness2's "agg": width
	ModeSequential = "sequential" // harness2's "seq": depth
)

// ParseMode accepts the mode names and harness2's on-disk tokens.
func ParseMode(name string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "parallel", "agg":
		return ModeParallel, nil
	case "sequential", "seq":
		return ModeSequential, nil
	}
	return "", fmt.Errorf("unknown mode %q; expected parallel or sequential", name)
}

// Improver roles, one built-in agent each (see cmd/gocode/builtin_agents.go).
const (
	RoleEditor    = "harness-editor"
	RoleSelector  = "harness-selector"
	RoleReflector = "harness-reflector"
)

// Task is one piece of work to adapt a harness for.
type Task struct {
	// ID names the task in run directories and the experience library.
	ID string `json:"id"`
	// Domain groups tasks whose harnesses inform each other: the experience
	// library is per domain, and a task reads only its own domain's entries.
	Domain string `json:"domain"`
	// Prompt is the request exactly as the agent receives it.
	Prompt string `json:"prompt"`
	// Workspace is the directory each rollout starts from. Every rollout
	// gets its own copy; the original is never touched. Empty means an
	// empty directory.
	Workspace string `json:"workspace,omitempty"`
	// Prompts is the work description the improver prompts interpolate.
	Prompts Prompts `json:"prompts"`
}

// Prompts is the work-shaped text the improver prompts interpolate,
// porting core/spec.BenchPrompts. Every field describes the WORK, never how
// it is measured.
type Prompts struct {
	Work            string `json:"work"`
	DomainWord      string `json:"domainWord"`
	DeliverableWord string `json:"deliverableWord"`
	InvLevers       string `json:"invLevers,omitempty"`
	DomLevers       string `json:"domLevers,omitempty"`
	Yardstick       string `json:"yardstick,omitempty"`
	DomainGuidance  string `json:"domainGuidance,omitempty"`
	SelectionChecks string `json:"selectionChecks,omitempty"`
}

// DefaultPrompts describes the work gocode is usually given: changing a code
// repository.
func DefaultPrompts() Prompts {
	return Prompts{
		Work: "Software engineering in a code repository. The agent receives a working copy of a " +
			"project and a request -- a fix, a feature, a refactor, an investigation or a document -- " +
			"and must leave the working copy in the requested state and say what it did. Correct, " +
			"minimal, verified changes matter; a description of a change is not a substitute for " +
			"making it.",
		DomainWord:      "area of work",
		DeliverableWord: "change",
		InvLevers: "- locate the code that governs the behaviour before editing: read its callers, its " +
			"tests and any document that specifies it\n" +
			"- run the project's own build and tests after changing code, and read a failure before " +
			"editing again\n" +
			"- keep the change scoped to the request: no unrelated renames, reformatting or rewrites\n" +
			"- before finishing, re-read every edited file and confirm each requested item is done",
		DomLevers: "- encode the project's conventions a change here must follow: layout, naming, error " +
			"handling, test patterns, build and lint commands\n" +
			"- name the invariants a change in this area must preserve and how to check each one\n" +
			"- point to the authoritative source in the repository (spec, design doc, cited upstream " +
			"file) instead of restating it",
	}
}

// WithDefaults fills every unset field from DefaultPrompts.
func (p Prompts) WithDefaults() Prompts {
	d := DefaultPrompts()
	if strings.TrimSpace(p.Work) == "" {
		p.Work = d.Work
		if p.InvLevers == "" {
			p.InvLevers = d.InvLevers
		}
		if p.DomLevers == "" {
			p.DomLevers = d.DomLevers
		}
	}
	if p.DomainWord == "" {
		p.DomainWord = d.DomainWord
	}
	if p.DeliverableWord == "" {
		p.DeliverableWord = d.DeliverableWord
	}
	return p
}

// measurementWords may not appear in Prompts: the prompts describe the work,
// and naming its measurement invites the improver to aim at the measure
// (BenchPrompts.__post_init__).
var measurementWords = []string{"benchmark", "evaluation", "grader", "rubric", "score"}

// Validate rejects a task that cannot be run, or whose prompts name how the
// work is measured.
func (t Task) Validate() error {
	if strings.TrimSpace(t.Prompt) == "" {
		return fmt.Errorf("task has no prompt")
	}
	if err := checkSlug("task id", t.ID); err != nil {
		return err
	}
	if err := checkSlug("domain", t.Domain); err != nil {
		return err
	}
	fields := map[string]string{
		"work": t.Prompts.Work, "invLevers": t.Prompts.InvLevers, "domLevers": t.Prompts.DomLevers,
		"yardstick": t.Prompts.Yardstick, "domainGuidance": t.Prompts.DomainGuidance,
		"selectionChecks": t.Prompts.SelectionChecks,
	}
	for name, value := range fields {
		low := strings.ToLower(value)
		for _, bad := range measurementWords {
			if strings.Contains(low, bad) {
				return fmt.Errorf("prompts.%s must describe the WORK, not its measurement (found %q)", name, bad)
			}
		}
	}
	if t.Workspace != "" {
		if info, err := os.Stat(t.Workspace); err != nil || !info.IsDir() {
			return fmt.Errorf("task workspace %s is not a directory", t.Workspace)
		}
	}
	return nil
}

func checkSlug(what, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", what)
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("%s %q may contain only letters, digits, '-', '_' and '.'", what, value)
		}
	}
	if strings.HasPrefix(value, ".") {
		return fmt.Errorf("%s %q may not start with '.'", what, value)
	}
	return nil
}

// Slug turns free text into an id usable as a task id or domain.
func Slug(text string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(text) {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if ok {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "task"
	}
	return out
}

// LoadTask reads a task file: markdown whose body is the prompt and whose
// optional frontmatter sets id, domain, workspace (relative to the file),
// work, deliverable and the other Prompts fields.
func LoadTask(path string) (Task, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Task{}, err
	}
	doc, err := markdown.Parse(string(raw))
	if err != nil {
		return Task{}, fmt.Errorf("task %s: %w", path, err)
	}
	base := filepath.Base(path)
	task := Task{
		ID:     doc.String("id"),
		Domain: doc.String("domain"),
		Prompt: strings.TrimSpace(doc.Content),
		Prompts: Prompts{
			Work:            doc.String("work"),
			DomainWord:      doc.String("domainWord"),
			DeliverableWord: doc.String("deliverable"),
			InvLevers:       doc.String("invLevers"),
			DomLevers:       doc.String("domLevers"),
			Yardstick:       doc.String("yardstick"),
			DomainGuidance:  doc.String("domainGuidance"),
			SelectionChecks: doc.String("selectionChecks"),
		},
	}
	if task.ID == "" {
		task.ID = Slug(strings.TrimSuffix(base, filepath.Ext(base)))
	}
	if ws := doc.String("workspace"); ws != "" {
		if !filepath.IsAbs(ws) {
			ws = filepath.Join(filepath.Dir(path), ws)
		}
		task.Workspace = filepath.Clean(ws)
	}
	return task, nil
}

// RolloutRequest asks for one execution of the task under one harness.
type RolloutRequest struct {
	Task  Task
	Label string
	// HarnessDir is the materialised harness tree to overlay.
	HarnessDir string
	// Workspace is a fresh copy of the task workspace, owned by this rollout.
	Workspace string
	// OnSession, when set, is called with the rollout's session id as soon
	// as the session exists, so a client can open it while it runs.
	OnSession func(sessionID string)
}

// Rollout is what one execution produced. The engine renders its evidence
// to disk straight away, so nothing downstream needs the executor again.
type Rollout struct {
	Label     string `json:"label"`
	SessionID string `json:"sessionID,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	OK        bool   `json:"ok"`
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
	// Stopped marks a rollout cut short from outside — a time or idle
	// limit, a cancel, an interrupt — rather than one that ran its course.
	// It says nothing about the candidate, so a resumed run rolls it again
	// instead of reusing it.
	Stopped bool `json:"stopped,omitempty"`
	// Limited marks a rollout a provider usage or rate limit cut short. It
	// is Stopped too, and it halts the run: everything after it would hit
	// the same limit.
	Limited bool    `json:"limited,omitempty"`
	CostUSD float64 `json:"costUSD"`
	// Trajectory is the full rendered session (RenderTrace).
	Trajectory string `json:"-"`
	// Final is the agent's last message.
	Final string `json:"-"`
}

// Executor runs one rollout. It must not return an error for a run that
// merely went badly: record that on the Rollout (OK false, Error set). An
// error is reserved for a failure to run at all.
type Executor interface {
	Rollout(ctx context.Context, req RolloutRequest) (Rollout, error)
}

// SessionRequest asks for one improver session.
type SessionRequest struct {
	Role string
	// Label names the stage (inv1, agg2, select, experience, ...).
	Label string
	// Workspace holds the `context/` evidence tree and MISSION.md; the
	// session runs rooted here and may edit `context/harness/`.
	Workspace string
	Mission   string
	// Resume, when set, is the session a limit or a stopped run cut short
	// in this same workspace: the improver continues it instead of starting
	// over, so a long session is not paid for again from the top.
	Resume string
	// OnSession, as on RolloutRequest.
	OnSession func(sessionID string)
}

// ErrLimited is wrapped by an improver error when a provider usage or rate
// limit stopped the session. The run halts on it, and the session is kept so
// a resume continues it.
var ErrLimited = errors.New("provider limit reached")

// Reply is what an improver session returned.
type Reply struct {
	Text    string  `json:"text"`
	CostUSD float64 `json:"costUSD"`
}

// Improver runs one improver session to completion.
type Improver interface {
	Session(ctx context.Context, req SessionRequest) (Reply, error)
}

// Event reports progress for the CLI, the HTTP API and the TUI.
type Event struct {
	Type    string  `json:"type"` // see the Event* constants
	Time    int64   `json:"time"` // unix milliseconds, set when emitted
	Task    string  `json:"task"`
	Mode    string  `json:"mode,omitempty"`
	Phase   string  `json:"phase,omitempty"` // base, probe, compose, select, reflect
	Label   string  `json:"label,omitempty"`
	OK      bool    `json:"ok"`
	Detail  string  `json:"detail,omitempty"`
	CostUSD float64 `json:"costUSD,omitempty"`
	// SessionID and Role are set on EventSession: which session a stage or
	// rollout runs in (Role is "rollout" or an improver role).
	SessionID string `json:"sessionID,omitempty"`
	Role      string `json:"role,omitempty"`
	// Attempt numbers EventStageRetry's rejected attempt.
	Attempt int `json:"attempt,omitempty"`
}

// Event types.
const (
	EventPhase          = "phase"
	EventRolloutStarted = "rollout.started"
	EventRolloutEnded   = "rollout.ended"
	EventStageStarted   = "stage.started"
	EventStageEnded     = "stage.ended"
	EventSelected       = "selected"
	EventReflected      = "reflected"
	// EventSession says a stage or rollout's session now exists.
	EventSession = "session"
	// EventStageRetry reports a rejected proposal and why; the next attempt
	// follows.
	EventStageRetry = "stage.retry"
)

// ExpectedRollouts is R = 4k: k base + 2k candidates + k finals (ledger.py).
func ExpectedRollouts(k int) int { return 4 * k }

// ExpectedSessions is P = 3k: k INV + k DOM + k compositions.
func ExpectedSessions(k int) int { return 3 * k }

// CostLine is the one-line budget a caller shows before spending money.
func CostLine(mode string, k int, library bool) string {
	extra := ""
	if k > 1 {
		extra = " + 1 selector"
	}
	if library {
		extra += " + 1 reflector"
	}
	return fmt.Sprintf("%s k=%d → %d rollouts, %d editor sessions%s", mode, k, ExpectedRollouts(k), ExpectedSessions(k), extra)
}

// Budget is one offered (mode, k) pair with its price, so a client lists
// the choice against what it costs. The TUI's picker and the desktop app's
// budget step both serve GET /api/harness/budgets from this.
type Budget struct {
	Mode        string `json:"mode"`
	K           int    `json:"k"`
	Rollouts    int    `json:"rollouts"`
	Edits       int    `json:"edits"`
	Description string `json:"description"`
}

// Budgets are the offered (mode, k) pairs, cheapest first. Each row names
// what it costs, so the choice is made against the price.
func Budgets() []Budget {
	return []Budget{
		{ModeParallel, 1, ExpectedRollouts(1), ExpectedSessions(1), "one Propose–Probe–Compose step"},
		{ModeParallel, 2, ExpectedRollouts(2), ExpectedSessions(2), "wider: 4 candidates, 2 compositions"},
		{ModeSequential, 2, ExpectedRollouts(2), ExpectedSessions(2), "deeper: 2 rounds, each building on the last"},
		{ModeParallel, 3, ExpectedRollouts(3), ExpectedSessions(3), "widest: 6 candidates, 3 compositions"},
		{ModeSequential, 3, ExpectedRollouts(3), ExpectedSessions(3), "deepest: 3 rounds"},
	}
}
