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
// PaddingTop, the header and its blank, the blank above the footer, the
// hint row, and the trailing blank.
const statsChrome = 6

// openHelpDialog opens the help panel, optionally with caller-supplied rows
// (the diff viewer's shortcut sheet) in place of the default paragraph.
func (a *App) openHelpDialog(title string, helpLines []string) {
	a.mount(dialog.NewNote(dialog.KindHelp, nil, nil, nil).WithHelpLines(title, helpLines))
}

// openStatusDialog opens the MCP/formatter/plugin status panel.
func (a *App) openStatusDialog() {
	a.mount(dialog.NewNote(dialog.KindStatus, a.statusBody, nil, nil))
}

// statusBody ports component/dialog-status.tsx: MCP servers, then the
// formatter and plugin sections with their empty-state fallbacks.
func (a *App) statusBody(p dialog.Palette, w int) []string {
	pad := strings.Repeat(" ", 2)
	onPanel := func(fg color.Color, bold bool) lipgloss.Style {
		style := lipgloss.NewStyle().Foreground(fg).Background(p.BackgroundPanel)
		if bold {
			style = style.Bold(true)
		}
		return style
	}
	text := func(s string) string { return onPanel(p.Text, false).Render(s) }
	bold := func(s string) string { return onPanel(p.Text, true).Render(s) }
	muted := func(s string) string { return onPanel(p.TextMuted, false).Render(s) }

	lines := []string{""}
	if len(a.mcpServers) > 0 {
		lines = append(lines, pad+text(fmt.Sprintf("%d MCP Servers", len(a.mcpServers))))
		for _, server := range a.mcpServers {
			dot := lipgloss.NewStyle().Foreground(mcpDotColor(a.theme, server.Status)).Render("•")
			lines = append(lines, pad+dot+" "+bold(server.Name)+" "+muted(mcpStatusLabel(server)))
		}
	} else {
		lines = append(lines, pad+text("No MCP Servers"))
	}
	lines = append(lines,
		"",
		pad+text("No Formatters"),
		"",
		pad+text(fmt.Sprintf("%d Plugins", len(a.plugins))),
	)
	for _, plugin := range a.plugins {
		dot := lipgloss.NewStyle().Foreground(pluginDotColor(a.theme, plugin.State)).Render("•")
		lines = append(lines, pad+dot+" "+bold(plugin.ID)+" "+muted(plugin.Source+" · "+plugin.State))
	}
	lines = append(lines, "")
	return lines
}

// openStatsOverlay opens the /stats panel and kicks off the fetch of per-
// session stats. The panel renders immediately with whatever data is already
// loaded (the active session's stats, at minimum) and refreshes when
// allStatsMsg arrives — the "open synchronously from cache, refresh in the
// background" rule every other dialog follows.
func (a *App) openStatsOverlay() tea.Msg {
	a.mount(dialog.NewNote(dialog.KindStats, a.statsBody, a.statsBudget, a.statsHints))
	return a.loadAllStats()
}

// statsBudget ports the stats panel's row arithmetic: the balanced margin
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
// there is nothing to report. An error title reuses the list dialog's
// emptyView colors (Error for the title, muted for the body).
func (a *App) statsEmptyView(p dialog.Palette, inner int, title, body string) []string {
	lines := []string{strings.Repeat(" ", statsPad) +
		lipgloss.NewStyle().Foreground(p.Error).Background(p.BackgroundPanel).Bold(true).
			Render(truncateRunes(title, inner))}
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
		return a.statsEmptyView(p, inner, "No sessions",
			"Usage is reported per session. Start one and its tokens, cost and cache hit rate will appear here.")
	case agg.loaded == 0 && agg.pending > 0:
		return a.statsEmptyView(p, inner, "Loading usage",
			fmt.Sprintf("Fetching stats for %s.", plural(agg.pending, "session")))
	case agg.loaded == 0:
		return a.statsEmptyView(p, inner, "No usage reported",
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

// statsHints is the panel's key hint row: every affordance names its key, and
// the scroll keys appear only while there is something to scroll.
func (a *App) statsHints(p dialog.Palette, w int, scrollable bool) string {
	hint := func(key, label string) string {
		return lipgloss.NewStyle().Foreground(p.Text).Background(p.BackgroundPanel).Render(key) + " " +
			lipgloss.NewStyle().Foreground(p.TextMuted).Background(p.BackgroundPanel).Render(label)
	}
	parts := []string{}
	if scrollable {
		parts = append(parts, hint("↑↓", "scroll"), hint("pgup/pgdn", "page"))
	}
	parts = append(parts, hint("esc", "close"))
	return strings.Repeat(" ", statsPad) +
		truncateRunes(strings.Join(parts, "  "), max(1, w-2*statsPad))
}
