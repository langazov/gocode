package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// This file ports packages/tui/src/component/dialog-skill.tsx. The dialog is
// deliberately minimal: a flat, single-category, fuzzy-searchable list of
// every discovered skill's name and description, with exactly one action —
// selecting a skill writes "/<name> " into the prompt, cursor at the end,
// the same convention as an ordinary custom slash command (every skill is
// already invocable that way regardless of its Slash flag; see
// internal/command's skill merge). There is no detail view, no
// enable/disable, no run-immediately action, matching the original.

// skillsOverlay opens the dialog from the cached skill list and refreshes it
// in the background, the same pattern modelsOverlay/agentsOverlay use so the
// dialog is never left staring at a stale "Loading" once the request lands.
func (a *App) skillsOverlay() tea.Cmd {
	a.openSkillDialog(a.skillList)
	return a.loadSkillListCmd()
}

// openSkillDialog renders the picker from an unfiltered skill list.
func (a *App) openSkillDialog(skills []client.Skill) {
	a.openList("Skills", a.skillItems(skills))
	o := a.overlay
	o.size = dialogLarge
	o.placeholder = "Search skills..."
	if len(skills) == 0 {
		switch {
		case a.skillListErr != "":
			o.emptyTitle = "Could not load skills"
			o.emptyBody = a.skillListErr
			o.locked = true
			o.hideFilter = true
		case !a.skillListLoaded:
			o.emptyTitle = "Loading skills"
			o.emptyBody = "Fetching the skill list..."
		default:
			o.emptyTitle = "No skills found"
			o.emptyBody = "No SKILL.md files were discovered for this project."
		}
	}
}

// skillItems ports DialogSkill's option mapping: name and description only,
// one flat "Skills" category, sorted by name for a stable, searchable list
// (the original relies on fuzzysort re-ordering as the user types, which
// this port's filter does too; the unfiltered order just needs to be
// deterministic).
func (a *App) skillItems(skills []client.Skill) []overlayItem {
	sorted := append([]client.Skill(nil), skills...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	items := make([]overlayItem, 0, len(sorted))
	for _, skill := range sorted {
		skill := skill
		items = append(items, overlayItem{
			label:    skill.Name,
			hint:     strings.Join(strings.Fields(skill.Description), " "),
			value:    skill.Name,
			category: "Skills",
			action: func() tea.Msg {
				a.input.SetValue("/" + skill.Name + " ")
				a.input.MoveToEnd()
				return nil
			},
		})
	}
	return items
}

// loadSkillListCmd refreshes the skill list.
func (a *App) loadSkillListCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		skills, err := c.Skills(a.ctx)
		if err != nil {
			return skillListMsg{err: err}
		}
		return skillListMsg{skills: skills}
	}
}

// skillListMsg carries a refreshed skill list, or the error that stopped one
// arriving — reported rather than dropped, matching DialogSkill's own error
// state (a locked, unfilterable panel naming the failure).
type skillListMsg struct {
	skills []client.Skill
	err    error
}
