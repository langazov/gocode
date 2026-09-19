package tui

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/langazov/gocode-go/internal/tui/dialog"
)

// The read-only panels (help, status, stats) mount as note dialogs: a body
// callback renders their lines through the dialog package's palette, and —
// for the stats panel — a scroll budget with the balanced-margin arithmetic
// from TUI_RECOMENDATIONS §9.7.

// statsChrome is every panel row that is not body: overlayPanel's
// PaddingTop, the header, the rule under it and the gap under that, the
// rule above the footer, the hint row, and the trailing blank. What the
// scroll window hides is reported by the footer, not by a row of its own,
// so the budget the panel is handed is the budget its body fills.
const statsChrome = 7

// openHelpDialog opens the help panel, optionally with caller-supplied rows
// (the diff viewer's shortcut sheet) in place of the default paragraph.
//
// It takes the same scroll budget the stats panel does: a shortcut sheet is
// as capable of outgrowing the terminal as a usage report, and a panel that
// outgrows it runs off the bottom of the screen instead of windowing.
func (a *App) openHelpDialog(title string, helpLines []string) {
	a.mount(dialog.NewNote(dialog.KindHelp, nil, a.statsBudget, nil).WithHelpLines(title, helpLines))
}

// openStatusDialog opens the MCP/formatter/plugin status panel.
func (a *App) openStatusDialog() {
	a.mount(dialog.NewNote(dialog.KindStatus, a.statusBody, a.statsBudget, nil))
}

// statusBody is the MCP / formatter / plugin panel.
//
// It is built from the same row controls the stats panel uses — a bold
// section heading, a blank under it, entries and muted notes in one column —
// because they are the same kind of surface and the reader should not have
// to learn two layouts. It used to mix its own idioms instead, heading one
// section "No MCP Servers", another "No Formatters" and a third "0 Plugins":
// three phrasings for one state, none of them a heading.
func (a *App) statusBody(p dialog.Palette, w int) []string {
	inner := max(8, w-2*statsPad)
	entry := func(dot color.Color, name, detail string) string {
		marker := lipgloss.NewStyle().Foreground(dot).Background(p.BackgroundPanel).Render("•")
		label := truncateRunes(name, max(1, inner-statsIndent))
		row := strings.Repeat(" ", statsPad) + marker + " " +
			lipgloss.NewStyle().Foreground(p.Text).Background(p.BackgroundPanel).Bold(true).Render(label)
		if detail != "" {
			room := inner - statsIndent - lipgloss.Width(label) - 1
			if room > 0 {
				row += " " + lipgloss.NewStyle().Foreground(p.TextMuted).
					Background(p.BackgroundPanel).Render(truncateRunes(detail, room))
			}
		}
		return row
	}

	// A section with entries gets the blank row under its heading that
	// §9.7 asks for; an empty one answers on the next line instead, so
	// three empty sections do not cost eleven rows of a panel that has to
	// fit on screen.
	var lines []string
	section := func(title, empty string, rows []string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, a.statsHeading(p, inner, title))
		if len(rows) == 0 {
			lines = append(lines, a.statsDetail(p, inner, empty))
			return
		}
		lines = append(lines, "")
		lines = append(lines, rows...)
	}

	servers := make([]string, 0, len(a.mcpServers))
	for _, server := range a.mcpServers {
		servers = append(servers, entry(mcpDotColor(a.theme, server.Status), server.Name, mcpStatusLabel(server)))
	}
	section("MCP Servers", "None connected.", servers)

	section("Formatters", "None configured.", nil)

	plugins := make([]string, 0, len(a.plugins))
	for _, plugin := range a.plugins {
		plugins = append(plugins, entry(pluginDotColor(a.theme, plugin.State), plugin.ID,
			plugin.Source+" · "+plugin.State))
	}
	section("Plugins", "None loaded.", plugins)
	return lines
}

// openStatsOverlay opens the /stats panel and kicks off the fetch of per-
// session stats. The panel renders immediately with whatever data is already
// loaded (the active session's stats, at minimum) and refreshes when
// allStatsMsg arrives — the "open synchronously from cache, refresh in the
// background" rule every other dialog follows.
func (a *App) openStatsOverlay() tea.Msg {
	a.mount(dialog.NewNote(dialog.KindStats, a.statsBody, a.statsBudget, nil))
	return a.loadAllStats()
}

// statsBudget is every read-only panel's row arithmetic: the balanced margin
// (height - 2*height/4) is the preference, the hard fit (height - height/4)
// the bound, and the margin gives way first on a short terminal.
func (a *App) statsBudget(height int) int {
	panelTop := height / 4
	balanced := height - 2*panelTop - statsChrome
	fits := height - panelTop - statsChrome
	return max(1, min(balanced, fits))
}

// statsRow renders one aligned key/value row: the label muted in a fixed
// column, then the value in Text — the number is the content, the label is
// the annotation, the same hierarchy the sidebar's sections use.
func (a *App) statsRow(p dialog.Palette, inner int, label, value string) string {
	key := truncateRunes(label, statsLabelWidth)
	key += strings.Repeat(" ", statsLabelWidth-lipgloss.Width(key))
	value = truncateRunes(value, max(1, inner-statsLabelWidth-1))
	return strings.Repeat(" ", statsPad) +
		lipgloss.NewStyle().Foreground(p.TextMuted).Background(p.BackgroundPanel).Render(key) + " " +
		lipgloss.NewStyle().Foreground(p.Text).Background(p.BackgroundPanel).Render(value)
}

// statsHeading renders a section title, matching the sidebar's bold-Text
// section headers.
func (a *App) statsHeading(p dialog.Palette, inner int, title string) string {
	return strings.Repeat(" ", statsPad) +
		lipgloss.NewStyle().Foreground(p.Text).Background(p.BackgroundPanel).Bold(true).
			Render(truncateRunes(title, inner))
}

// statsEntry renders the title line of a per-model / per-session entry.
func (a *App) statsEntry(p dialog.Palette, inner int, title string) string {
	return strings.Repeat(" ", statsPad) +
		lipgloss.NewStyle().Foreground(p.Text).Background(p.BackgroundPanel).Bold(true).
			Render(truncateRunes(title, inner))
}

// statsDetail renders an entry's indented muted detail line.
func (a *App) statsDetail(p dialog.Palette, inner int, text string) string {
	return strings.Repeat(" ", statsPad+statsIndent) +
		lipgloss.NewStyle().Foreground(p.TextMuted).Background(p.BackgroundPanel).
			Render(truncateRunes(text, max(1, inner-statsIndent)))
}

// statsNote renders an advisory line — a partial load, an unavailable
// session — in the same column as the section's rows.
func (a *App) statsNote(p dialog.Palette, inner int, text string) string {
	return strings.Repeat(" ", statsPad) +
		lipgloss.NewStyle().Foreground(p.TextMuted).Background(p.BackgroundPanel).
			Render(truncateRunes(text, inner))
}

// statsEmptyView is the panel's empty state, distinguishing the three reasons
// there is nothing to report. Only the third is a failure, and only it wears
// the Error color — the list dialog's emptyView draws the same line. Telling
// a user that having started no sessions yet is an error is telling them
// something untrue about their own machine.
func (a *App) statsEmptyView(p dialog.Palette, inner int, failed bool, title, body string) []string {
	titleFg := p.Text
	if failed {
		titleFg = p.Error
	}
	lines := []string{strings.Repeat(" ", statsPad) +
		lipgloss.NewStyle().Foreground(titleFg).Background(p.BackgroundPanel).Bold(true).
			Render(truncateRunes(title, inner)), ""}
	for _, line := range wrapWords(body, inner) {
		lines = append(lines, strings.Repeat(" ", statsPad)+
			lipgloss.NewStyle().Foreground(p.TextMuted).Background(p.BackgroundPanel).Render(line))
	}
	return lines
}

// statsBody builds the scrollable body: the aggregate sections, then the
// per-model and per-session breakdowns. Called by the note dialog on every
// render, so a landed stats batch refreshes the open panel.
func (a *App) statsBody(p dialog.Palette, w int) []string {
	inner := max(8, w-2*statsPad)
	agg := a.computeStatsAggregate()

	// The three reasons there is nothing to show, told apart. Reporting all
	// of them as a table of zeros is what makes a batch still in flight
	// indistinguishable from a fresh install with no history.
	switch {
	case agg.sessionCount == 0:
		return a.statsEmptyView(p, inner, false, "No sessions",
			"Usage is reported per session. Start one and its tokens, cost and cache hit rate will appear here.")
	case agg.loaded == 0 && agg.pending > 0:
		return a.statsEmptyView(p, inner, false, "Loading usage",
			fmt.Sprintf("Fetching stats for %s.", plural(agg.pending, "session")))
	case agg.loaded == 0:
		return a.statsEmptyView(p, inner, true, "No usage reported",
			"The server returned no stats for any session. It may not implement the stats endpoint.")
	}

	lines := []string{
		a.statsHeading(p, inner, "Overview"),
		"",
		a.statsRow(p, inner, "Sessions", localeNumber(agg.sessionCount)),
		a.statsRow(p, inner, "Total Cost", formatMoney(agg.cost)),
		a.statsRow(p, inner, "Total Messages", localeNumber(agg.messages)),
	}
	// Say so while the picture is still incomplete, rather than presenting a
	// partial total as a final one.
	if agg.pending > 0 {
		lines = append(lines, a.statsNote(p, inner,
			fmt.Sprintf("%s still loading", plural(agg.pending, "session"))))
	}
	if agg.unavailable > 0 {
		lines = append(lines, a.statsNote(p, inner,
			fmt.Sprintf("%s reported no stats", plural(agg.unavailable, "session"))))
	}

	lines = append(lines,
		"",
		a.statsHeading(p, inner, "Tokens"),
		"",
		a.statsRow(p, inner, "Input (fresh)", localeNumber(agg.input)),
		a.statsRow(p, inner, "Output", localeNumber(agg.output)),
		a.statsRow(p, inner, "Reasoning", localeNumber(agg.reasoning)),
		a.statsRow(p, inner, "Cache Read", localeNumber(agg.cacheRead)),
		a.statsRow(p, inner, "Cache Write", localeNumber(agg.cacheWrite)),
	)

	totalPrompt := agg.input + agg.cacheRead
	var hitPct float64
	if totalPrompt > 0 {
		hitPct = float64(agg.cacheRead) / float64(totalPrompt) * 100
	}
	lines = append(lines,
		"",
		a.statsHeading(p, inner, "Prompt Cache"),
		"",
		a.statsRow(p, inner, "Total Prompt", localeNumber(totalPrompt)),
		a.statsRow(p, inner, "Cache Hits", localeNumber(agg.cacheRead)),
		a.statsRow(p, inner, "Hit Rate", statsPercent(hitPct)),
	)

	if len(agg.models) > 0 {
		lines = append(lines, "", a.statsHeading(p, inner, "By Model"), "")
		for i, m := range agg.models {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines,
				a.statsEntry(p, inner, m.providerID+"/"+m.modelID),
				a.statsDetail(p, inner, fmt.Sprintf("%s · %s · %s hit",
					plural(m.sessionCount, "session"), formatMoney(m.cost), statsPercent(m.hitRate))),
				a.statsDetail(p, inner, fmt.Sprintf("in %s · out %s · cache %s",
					localeNumber(m.input), localeNumber(m.output), localeNumber(m.cacheRead))),
			)
		}
	}

	if len(agg.sessions) > 0 {
		lines = append(lines, "", a.statsHeading(p, inner, "By Session"), "")
		for i, s := range agg.sessions {
			if i > 0 {
				lines = append(lines, "")
			}
			title := s.title
			if title == "" {
				title = s.sessionID
			}
			modelLabel := "—"
			if s.modelID != "" {
				modelLabel = s.providerID + "/" + s.modelID
			}
			// The recency the section is ordered by, on the row itself: an
			// ordering the reader cannot see is indistinguishable from an
			// arbitrary one.
			detail := fmt.Sprintf("%s · %s · %s",
				modelLabel, formatMoney(s.cost), plural(s.messages, "msg"))
			if when := relativeTime(sessionRecency(s)); when != "" {
				detail += " · " + when
			}
			lines = append(lines,
				a.statsEntry(p, inner, title),
				a.statsDetail(p, inner, detail),
			)
			// A session the batch has not resolved has nothing to break
			// down; the zeros a token line would print are not its usage.
			if s.resolved {
				lines = append(lines, a.statsDetail(p, inner,
					fmt.Sprintf("in %s · out %s · cache %s · %s hit",
						localeNumber(s.input), localeNumber(s.output),
						localeNumber(s.cacheRead), statsPercent(s.hitRate))))
			} else {
				lines = append(lines, a.statsDetail(p, inner, s.pendingLabel))
			}
		}
	}
	return lines
}
