package harness

import (
	"embed"
	"fmt"
	"regexp"
	"strings"
)

// The improver prompts, porting the "adapt" pack of harness2/core/prompts.py
// (VERSION "harness2-1"). Paragraphs are embedded verbatim from prompts/,
// except surface.md, which describes gocode's editable surface instead of
// opencode's (agent/<name>.md for sub_agents/, hooks.json for plugin/*.ts,
// and no Jinja-brace rule: gocode renders no template).

//go:embed prompts/*.md
var promptFS embed.FS

// PromptVersion names the prompt pack, recorded in every summary so results
// from different packs are never compared silently.
const PromptVersion = "harness2-1+gocode"

func fragment(name string) string {
	data, err := promptFS.ReadFile("prompts/" + name + ".md")
	if err != nil {
		panic(fmt.Sprintf("harness: missing prompt fragment %s: %v", name, err))
	}
	return strings.TrimSpace(string(data))
}

func fill(text string, vars map[string]string) string {
	for key, value := range vars {
		text = strings.ReplaceAll(text, "{{"+key+"}}", value)
	}
	return text
}

var blankRuns = regexp.MustCompile(`\n{3,}`)

// tidy collapses the blank runs an empty optional slot leaves.
func tidy(parts ...string) string {
	return strings.TrimSpace(blankRuns.ReplaceAllString(strings.Join(parts, "\n\n"), "\n\n")) + "\n"
}

func work(p Prompts) string { return "# THE WORK\n\n" + strings.TrimSpace(p.Work) }

func lens(p Prompts) string {
	if body := strings.TrimSpace(p.DomainGuidance); body != "" {
		return "# DOMAIN AND TASK LENS\n\n" + body
	}
	return ""
}

func yardstick(p Prompts) string {
	if body := strings.TrimSpace(p.Yardstick); body != "" {
		return "# WHAT GOOD PROCESS LOOKS LIKE HERE\n\n" + body
	}
	return ""
}

// ProposePrompt is the job for one INV or DOM candidate. round > 0 means the
// sequential mode's round t (prompts.propose with round_t).
func ProposePrompt(p Prompts, dim string, k, index, round int) string {
	p = p.WithDefaults()
	var block string
	switch dim {
	case "INV":
		block = fill(fragment("inv"), map[string]string{"deliverable": p.DeliverableWord, "levers": strings.TrimSpace(p.InvLevers)})
	case "DOM":
		block = fill(fragment("dom"), map[string]string{
			"deliverable": p.DeliverableWord, "domain": p.DomainWord, "levers": strings.TrimSpace(p.DomLevers),
		})
	default:
		panic("harness: unknown proposal dimension " + dim)
	}
	where := ""
	if k > 1 {
		where = fmt.Sprintf("You are candidate %d of %d on this dimension, drawn independently from the same evidence.", index, k)
	}
	if round > 0 {
		where += fmt.Sprintf("\n\nThis is round %d. You are editing the checkpoint the previous round produced, not the "+
			"original harness, and the evidence includes every earlier round. In this stage `KEEP_BASE` means leave "+
			"that CURRENT checkpoint unchanged; it never resets to the original harness.", round)
	}
	how := []string{
		"# HOW TO DECIDE WHAT TO CHANGE\n",
		"Every edit must trace to a moment in a trajectory. For each one ask: which run did what, and would this " +
			"text have changed it? If you cannot name the moment, the edit is speculation and costs context for " +
			"nothing. Prefer, in order:",
		"  1. a DIVERGENCE -- the runs disagreed, so the agent can already do it and just needs telling. This is " +
			"the cheapest win.",
		"  2. a repeated concrete failure -- the same error or omission in every run.",
		"  3. a risk visible in a trajectory but not yet realised.",
		"Do not fix what the runs already got right, and do not restate what the harness already says clearly; " +
			"duplicated instruction dilutes the rest.",
	}
	if strings.TrimSpace(where) != "" {
		how = append(how, "", strings.TrimSpace(where))
	}
	how = append(how, "", "Do not hedge toward an average or try to cover every base -- commit to the single most "+
		"defensible version of your dimension. A later stage compares and merges.")
	return tidy(
		"You are adapting an autonomous agent's harness.",
		work(p),
		lens(p),
		block,
		generalize(p),
		strings.Join(how, "\n"),
		yardstick(p),
		fragment("answer_free"),
		fragment("evidence_boundary"),
		fragment("surface"),
	)
}

func generalize(p Prompts) string {
	return fill(fragment("generalize"), map[string]string{
		"domain_word": p.DomainWord, "DOMAIN_WORD_UPPER": strings.ToUpper(p.DomainWord),
	})
}

// AggregatePrompt is agg-i: compose ONE final from all base evidence and all
// 2k candidates (prompts.aggregate).
func AggregatePrompt(p Prompts, k, index int) string {
	p = p.WithDefaults()
	n := 2 * k
	plural, sAre := "", " is"
	if k > 1 {
		plural, sAre = "s", "s are"
	}
	parallel := ""
	if k > 1 {
		parallel = fmt.Sprintf("You are one of %d aggregators composing a final from this same evidence in parallel, "+
			"and you will not see the others. Do not aim for the consensus answer -- commit to the harness you can defend.", k)
	}
	example := k - 1
	if example < 2 {
		example = 2
	}
	return tidy(
		"You are composing THE final harness for one piece of work. What you emit is what gets ADOPTED: it runs on "+
			"this task, and if selected it enters this area's library where later tasks read it. Every rule that bound "+
			"the candidate proposers binds you -- you are the last gate, and merging an edit launders it.",
		work(p),
		lens(p),
		fmt.Sprintf("# WHAT YOU HAVE\n\n%d rollout%s under the unmodified harness, and %d candidate harnesses -- %d on the "+
			"INV dimension (how the agent works) and %d on the DOM dimension (what a practitioner knows). Each candidate "+
			"was rolled out ONCE and you have that rollout.\n\nThe candidates were sampled separately but share the same "+
			"task, evidence and likely model biases. Agreement is support, not independent proof; contradiction is a "+
			"reason to inspect the trajectories, not to vote.", k, plural, n, k, k),
		fill(fragment("aggregate_weigh"), map[string]string{"k": fmt.Sprint(k), "s_are": sAre}),
		fill(fragment("aggregate_job"), map[string]string{"keep_base": fragment("keep_base")}),
		parallel,
		yardstick(p),
		fragment("answer_free"),
		fragment("evidence_boundary"),
		fragment("surface"),
		fmt.Sprintf("# NOTES FORMAT\n\nIn your single `<notes>` block, one line per decision, in this exact shape:\n\n"+
			"    kept|revised|dropped -> <which edit> -> <the trajectory moment or base-set pattern that decided it, "+
			"with counts where sets are involved, e.g. 'failed in %d/%d base rollouts'>\n\n"+
			"A decision without evidence attached is a decision the next reader cannot audit.", example, k),
	)
}

// ComposeRoundPrompt is seq-t: compose checkpoint C_t from this round's
// candidates and every earlier round (prompts.compose_round).
func ComposeRoundPrompt(p Prompts, t, k int) string {
	p = p.WithDefaults()
	first := ""
	if t == 1 {
		first = "\n\nThis is the FIRST round, so the only evidence is the base rollouts and this round's two candidates."
	}
	job := fmt.Sprintf("Compose checkpoint %d, which the next round will build on.\n", t)
	if t >= k {
		job = "Compose THE FINAL harness, from the WHOLE history rather than just this round.\n"
	}
	return tidy(
		fmt.Sprintf("You are composing checkpoint %d of %d in a sequential adaptation loop.", t, k),
		work(p),
		lens(p),
		"# WHAT YOU HAVE\n\nThe rollouts under the unmodified harness (the shared evidence, which does not change "+
			"between rounds), this round's INV and DOM candidates with their rollouts, and every earlier round's "+
			"candidates, checkpoints and trajectories. The previous checkpoint's own rollout is this round's on-policy "+
			"evidence: it shows what the harness you are editing actually does."+first,
		"# YOUR JOB\n\n"+job+fill(fragment("compose_job"), map[string]string{"keep_base": fragment("keep_base")}),
		fragment("answer_free"),
		fragment("evidence_boundary"),
		fragment("surface"),
	)
}

// SelectPrompt is blind selection over n anonymised finals (prompts.select,
// pool_kind "finals", the only pool this release builds).
func SelectPrompt(p Prompts, n int) string {
	p = p.WithDefaults()
	checks := ""
	if body := strings.TrimSpace(p.SelectionChecks); body != "" {
		checks = "# WHAT CORRECTNESS MEANS FOR THIS WORK\n\n" + body
	}
	return tidy(
		fmt.Sprintf("You are choosing which of %d candidate answers should be delivered to the person who asked for this work.", n),
		work(p),
		lens(p),
		fragment("pool_finals_select"),
		"# WHAT YOU ARE LOOKING AT\n\n"+
			"    context/task.md              the request, verbatim\n"+
			"    context/source/             the material the work was done from\n"+
			"    context/candidates/<id>.md  what that run PRODUCED -- the "+p.DeliverableWord+", rendered\n\n"+
			"You are choosing between ARTIFACTS. You are not told how any of them was produced and you cannot see the "+
			"runs that made them, so judge each one on what it is, against the request and the source -- never on how "+
			"impressive or thorough it sounds.",
		fill(fragment("select_decide"), map[string]string{"deliverable": p.DeliverableWord}),
		checks,
		fragment("answer_free_select"),
		fragment("evidence_boundary_select"),
		"# OUTPUT FORMAT\n\nFirst the reasoning, then the choice as the very last thing:\n\n"+
			"<why>\nAt most 6 lines. Name the criterion that decided it, and the closest runner-up with the specific "+
			"thing that cost it the pick. If you rejected a candidate on CONFORMANCE, say which file, field or "+
			"section.\n</why>\n\n```choice\n<the id of the single candidate to submit>\n```",
	)
}

// ExperiencePrompt asks for the edit rows and the one transferable lesson
// (prompts.experience).
func ExperiencePrompt(p Prompts) string {
	p = p.WithDefaults()
	return tidy(
		"You are reading the complete record of one improvement round: a base harness, the edits proposed against "+
			"it, the executions those edited harnesses produced, and which output was finally submitted. Write two "+
			"things for a library that future work in this area reads before it makes its own edits.",
		work(p),
		lens(p),
		fragment("experience"),
		fragment("answer_free"),
		fragment("evidence_boundary"),
		fragment("experience_format"),
	)
}
