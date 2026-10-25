package harness

import (
	"context"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"math/rand"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Blind selection over the k finals, then reflection on the whole round,
// porting harness2/core/selection.py.

// Selection records which final was delivered and why.
type Selection struct {
	ChosenID    string            `json:"chosenID"`
	ChosenLabel string            `json:"chosenLabel"`
	Why         string            `json:"why,omitempty"`
	IDMap       map[string]string `json:"idMap"`
	Dropped     []string          `json:"dropped,omitempty"`
	Candidates  int               `json:"candidates"`
	OK          bool              `json:"ok"`
	Fallback    bool              `json:"fallback"`
	Trivial     bool              `json:"trivial"`
	Error       string            `json:"error,omitempty"`
	CostUSD     float64           `json:"costUSD"`
}

// Experience is the reflector's output: the per-component edit rows and the
// one lesson that transfers.
type Experience struct {
	Cue        string `json:"cue"`
	Lesson     string `json:"lesson"`
	Evidence   string `json:"evidence"`
	Confidence string `json:"confidence,omitempty"`
	Edits      string `json:"edits,omitempty"`
}

// Complete reports whether the lesson carries every required field.
func (e *Experience) Complete() bool {
	return e != nil && strings.TrimSpace(e.Cue) != "" && strings.TrimSpace(e.Lesson) != "" &&
		strings.TrimSpace(e.Evidence) != ""
}

var (
	choiceRx = regexp.MustCompile("(?s)```choice\\s*\\n?\\s*([A-Za-z0-9_-]+)\\s*\\n?\\s*```")
	whyRx    = regexp.MustCompile(`(?s)<why>(.*?)</why>`)
	expRx    = regexp.MustCompile(`(?s)<experience>(.*?)</experience>`)
	editsRx  = regexp.MustCompile(`(?s)<edits>(.*?)</edits>`)
)

// AssignIDs maps anonymous ids to labels: canonical sort, then a shuffle
// seeded from the task, so ids are stable across reruns (assign_ids).
func AssignIDs(labels []string, taskID string, k int, mode string) map[string]string {
	ordered := append([]string(nil), labels...)
	sort.Strings(ordered)
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%s", taskID, k, mode)))
	seed := int64(binary.BigEndian.Uint64(sum[:8]) >> 1)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(ordered), func(i, j int) { ordered[i], ordered[j] = ordered[j], ordered[i] })
	out := make(map[string]string, len(ordered))
	for i, label := range ordered {
		out[anonID(i)] = label
	}
	return out
}

// anonID is A..Z, AA..AZ, BA...
func anonID(index int) string {
	out := ""
	for n := index + 1; n > 0; {
		n--
		out = string(rune('A'+n%26)) + out
		n /= 26
	}
	return out
}

// selectFinal picks the final to deliver. At k=1 the single final is
// delivered without a selector session (trivial_selection); otherwise a
// selector chooses blind, and a selector failure falls back to the first
// delivered final rather than failing the task.
func (m *modeRun) selectFinal(ctx context.Context) *Selection {
	sel := &Selection{IDMap: map[string]string{}}
	pool := map[string]RunRecord{}
	for _, label := range m.finals {
		run, ok := m.rollouts[label]
		if !ok || !run.OK || !run.Delivered {
			sel.Dropped = append(sel.Dropped, label)
			continue
		}
		pool[label] = run
	}
	sel.Candidates = len(pool)
	if len(pool) == 0 {
		sel.Error = "no final produced a deliverable"
		return sel
	}
	labels := make([]string, 0, len(pool))
	for label := range pool {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	if m.k == 1 || len(labels) == 1 {
		sel.OK, sel.Trivial = true, true
		sel.ChosenID, sel.ChosenLabel = "A", labels[0]
		sel.IDMap = map[string]string{"A": labels[0]}
		sel.Why = "trivial pool: a single delivered final; delivered without a selector session"
		return sel
	}

	sel.IDMap = AssignIDs(labels, m.task.ID, m.k, m.mode)
	root := filepath.Join(m.dir, "selection")
	ids := make([]string, 0, len(sel.IDMap))
	for id := range sel.IDMap {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if err := m.writeCandidates(root, sel, pool, ids); err != nil {
		sel.Error = err.Error()
		return sel
	}
	idList := strings.Join(ids, ", ")
	mission := SelectPrompt(m.task.Prompts, len(pool)) + "\n\n# YOUR EVIDENCE\n\nIt is on disk. Read it -- do not answer " +
		"from this message alone.\n\n" +
		"context/task.md                 the instructions\n" +
		"context/source/                 the workspace the task started from, when supplied\n" +
		"context/candidates/<id>.md      one file per candidate: " + idList + "\n\n" +
		"Open every candidate before choosing. Choose exactly one of: " + idList + "."
	m.emit(Event{Type: EventStageStarted, Phase: "select", Label: "select"})
	reply, _, _, cost, err := m.call(ctx, RoleSelector, "select", mission, root, false)
	sel.CostUSD += cost
	m.ledger.spend(cost)
	if err == nil {
		err = parseChoice(sel, reply.Text, idList)
	}
	if err != nil {
		sel.ChosenLabel = labels[0]
		for id, label := range sel.IDMap {
			if label == sel.ChosenLabel {
				sel.ChosenID = id
			}
		}
		sel.Fallback = true
		sel.Error = clip(err.Error(), 400)
	}
	sel.OK = true
	m.emit(Event{Type: EventStageEnded, Phase: "select", Label: "select", OK: !sel.Fallback, Detail: sel.Error, CostUSD: cost})
	return sel
}

func parseChoice(sel *Selection, text, idList string) error {
	match := choiceRx.FindStringSubmatch(text)
	if match == nil {
		return fmt.Errorf("no ```choice block in the reply")
	}
	id := strings.TrimSpace(match[1])
	label, ok := sel.IDMap[id]
	if !ok {
		return fmt.Errorf("chose %q, which is not one of %s", id, idList)
	}
	if why := whyRx.FindStringSubmatch(text); why != nil {
		sel.Why = clip(strings.TrimSpace(why[1]), 2000)
	}
	sel.ChosenID, sel.ChosenLabel = id, label
	return nil
}

func (m *modeRun) writeCandidates(root string, sel *Selection, pool map[string]RunRecord, ids []string) error {
	layout, err := BuildEvidence(root, EvidenceInput{Task: m.task})
	if err != nil {
		return err
	}
	_ = layout
	for _, id := range ids {
		text := pool[sel.IDMap[id]].Deliverable()
		header := fmt.Sprintf("<!-- candidate %s: %d chars, %d lines. COMPLETE -- nothing trimmed. Navigate with grep/offsets; "+
			"evaluating a candidate means opening the part that determines whether it works. -->\n", id, len(text), strings.Count(text, "\n")+1)
		if err := writeFileAll(filepath.Join(root, "candidates", id+".md"), header+text); err != nil {
			return err
		}
	}
	return AssertNoGraderVerdicts(root)
}

// reflect learns from every edit, decision, trajectory and deliverable after
// adoption (reflect_adoption + _distil). It returns nil when the record shows
// no transferable lesson.
func (m *modeRun) reflect(ctx context.Context, sel *Selection, baseDir string) *Experience {
	root := filepath.Join(m.dir, "reflection")
	var rollouts []RunRecord
	var poolLabels []string
	labels := make([]string, 0, len(m.rollouts))
	for label := range m.rollouts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		rollouts = append(rollouts, m.rollouts[label])
		if !strings.HasPrefix(label, "base") {
			poolLabels = append(poolLabels, label)
		}
	}
	m.emit(Event{Type: EventStageStarted, Phase: "reflect", Label: "experience"})
	layout, err := BuildEvidence(root, EvidenceInput{
		Task: m.task, Rollouts: rollouts, HarnessDir: m.harnessDir(sel.ChosenLabel),
		Pool: m.poolOf(poolLabels), Decisions: m.decisions(),
	})
	if err == nil {
		_, err = copyTreeFiles(baseDir, filepath.Join(root, "base_harness"))
	}
	if err == nil {
		err = writeJSON(filepath.Join(root, "adoption.json"), map[string]any{
			"chosen_label": sel.ChosenLabel, "candidate_ids": sel.IDMap, "reason": sel.Why,
		})
	}
	if err != nil {
		m.emit(Event{Type: EventStageEnded, Phase: "reflect", Label: "experience", Detail: err.Error()})
		return nil
	}
	mission := ExperiencePrompt(m.task.Prompts) + "\n\n# WHAT HAPPENED\n\n" +
		"Harness " + sel.ChosenLabel + " was adopted. The selection reasoning was:\n\n" + sel.Why + "\n\n" +
		"context/adoption.json maps any anonymous IDs in that reasoning to rollout labels. Compare context/harness/ " +
		"with context/base_harness/ and the candidates in context/pool/. Read the decisions and execution contrasts " +
		"before writing.\n\n" + layout.String()
	reply, _, _, cost, err := m.call(ctx, RoleReflector, "experience", mission, root, false)
	sel.CostUSD += cost
	m.ledger.spend(cost)
	if err != nil {
		m.emit(Event{Type: EventStageEnded, Phase: "reflect", Label: "experience", Detail: err.Error(), CostUSD: cost})
		return nil
	}
	exp := parseExperience(reply.Text)
	if edits := editsRx.FindStringSubmatch(reply.Text); edits != nil {
		_ = writeFileAll(filepath.Join(root, "edits.md"), strings.TrimSpace(edits[1])+"\n")
	}
	detail := "no transferable lesson"
	if exp.Complete() {
		detail = exp.Cue
	}
	m.emit(Event{Type: EventReflected, Phase: "reflect", Label: "experience", OK: exp.Complete(), Detail: detail, CostUSD: cost})
	if !exp.Complete() {
		return nil
	}
	return exp
}

func parseExperience(text string) *Experience {
	exp := &Experience{}
	if edits := editsRx.FindStringSubmatch(text); edits != nil {
		exp.Edits = strings.TrimSpace(edits[1])
	}
	match := expRx.FindStringSubmatch(text)
	if match == nil {
		return exp
	}
	for _, line := range strings.Split(strings.TrimSpace(match[1]), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "cue":
			exp.Cue = value
		case "lesson":
			exp.Lesson = value
		case "evidence":
			exp.Evidence = value
		case "confidence":
			exp.Confidence = value
		}
	}
	return exp
}
