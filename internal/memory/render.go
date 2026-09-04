package memory

import (
	"fmt"
	"strings"
)

// Rendering memories into a system prompt block.
//
// This block rides on every single request for the life of the project, so it
// is a permanent tax on both the context window and the provider's cache-write
// cost. That is why it is budgeted rather than simply concatenated: an
// unbounded block grows quietly until someone notices their token bill, and by
// then it is in every cached prefix.

// Budget bounds the rendered block.
type Budget struct {
	// MaxEntries caps how many memories are rendered.
	MaxEntries int
	// MaxChars caps the rendered block's length. Entries are dropped until it
	// fits, so a handful of very long memories cannot crowd out the rest.
	MaxChars int
}

// DefaultBudget is what the plugin uses unless the config overrides it. 100
// entries at a typical instruction length lands around 2k tokens, which is a
// real but defensible standing cost.
var DefaultBudget = Budget{MaxEntries: 100, MaxChars: 8000}

const (
	openTag  = "<memories>"
	closeTag = "</memories>"

	preamble = "Durable instructions saved by the user. Treat them as standing directions " +
		"that apply to this project regardless of what the current conversation is about. " +
		"When you act on one, or when the user tells you something that contradicts one, " +
		"cite its id."
)

// Render turns memories into the system prompt block, dropping entries from
// the tail until the budget is met.
//
// Input order is the precedence order the store already returns (pinned
// first, then most recently updated), so the entries dropped are the stalest
// unpinned ones. Returns "" for no memories — an empty block on every request
// would be pure overhead, and a model that sees `<memories></memories>` has
// been told something misleading rather than nothing.
func Render(memories []Memory, budget Budget) string {
	if budget.MaxEntries <= 0 {
		budget.MaxEntries = DefaultBudget.MaxEntries
	}
	if budget.MaxChars <= 0 {
		budget.MaxChars = DefaultBudget.MaxChars
	}

	usable := make([]Memory, 0, len(memories))
	for _, item := range memories {
		if item.Disabled || strings.TrimSpace(item.Content) == "" {
			continue
		}
		usable = append(usable, item)
	}
	if len(usable) == 0 {
		return ""
	}

	omitted := 0
	if len(usable) > budget.MaxEntries {
		omitted = len(usable) - budget.MaxEntries
		usable = usable[:budget.MaxEntries]
	}

	// Shrink until the whole block fits. Building the block each pass is
	// wasteful in principle and irrelevant in practice — the loop runs once
	// for any realistic memory set, and correctness here is worth more than
	// saving a few string builds on a path that runs once per turn.
	for {
		block := build(usable, omitted)
		if len(block) <= budget.MaxChars || len(usable) == 0 {
			if len(usable) == 0 {
				return ""
			}
			return block
		}
		usable = usable[:len(usable)-1]
		omitted++
	}
}

func build(memories []Memory, omitted int) string {
	var b strings.Builder
	b.WriteString(openTag)
	b.WriteByte('\n')
	b.WriteString(preamble)
	b.WriteByte('\n')
	for _, item := range memories {
		b.WriteString("- [")
		b.WriteString(item.ID)
		b.WriteString("] ")
		if item.Category != "" {
			b.WriteString("(")
			b.WriteString(flatten(sanitize(item.Category)))
			b.WriteString(") ")
		}
		// A multi-line memory keeps its lines, indented under the bullet so
		// the list stays unambiguous. Flattening them instead would silently
		// destroy the structure of anything written as steps or a checklist.
		for i, line := range strings.Split(sanitize(item.Content), "\n") {
			if i > 0 {
				b.WriteString("\n  ")
			}
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "(%d further %s omitted to stay within the memory budget; "+
			"the user can see all of them with /memory.)\n", omitted, plural(omitted, "memory", "memories"))
	}
	b.WriteString(closeTag)
	return b.String()
}

// sanitize defuses a block tag inside a memory's content and normalizes line
// endings. A memory is user- or model-authored text landing in a structured
// prompt block: one containing "</memories>" would otherwise end the block
// early and promote whatever followed it to a top-level instruction.
//
// The replacements are square-bracketed rather than escaped with a zero-width
// space, so the neutralization is visible in the rendered prompt and in this
// file.
//
// Newlines survive — build indents them under the bullet. What must not
// survive is a line that could pass for a new bullet, so a leading "- " on a
// continuation line is defanged.
func sanitize(value string) string {
	value = strings.ReplaceAll(value, closeTag, "[/memories]")
	value = strings.ReplaceAll(value, openTag, "[memories]")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")

	lines := strings.Split(value, "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if i > 0 {
			line = strings.TrimPrefix(line, "- ")
		}
		lines[i] = line
	}
	// Blank interior lines would render as a bare "  " row inside the list.
	kept := lines[:0]
	for i, line := range lines {
		if line == "" && i > 0 {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// flatten collapses a value onto one line, for the places that must stay
// single-line (the category label).
func flatten(value string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(value), " "))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
