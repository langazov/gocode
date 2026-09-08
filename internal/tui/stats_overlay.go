package tui

// Stats overlay: a read-only panel showing per-session, per-model and
// aggregate token usage, cost, and prompt-cache hit rates. Opened with
// "/stats" (see commandsRegistry in dialogs.go).
//
// The panel fetches stats for every session the server knows (loadAllStats in
// app.go) and groups them by model — the dimension that answers the question
// "how much is this model costing me?" and the one where cache hit rates vary
// the most, since different providers cache at different breakpoints.
//
// Rendering follows the dialog contract in
// documentation/recomendations/TUI_RECOMENDATIONS.md, which this panel
// previously diverged from in five ways:
//
//   - Key/value rows were padded by hand ("Cache Hits:" carried one space too
//     many), so the value column did not line up. They now go through
//     statsRow, one control with one column width.
//   - Long titles and model labels were rendered untruncated, which overflows
//     the panel width and tears the row spliceAt composites it into.
//   - The header scrolled away with the body, because windowing was applied to
//     the whole panel rather than to the body alone.
//   - scrollTop was never clamped at the bottom (the key handler's comment
//     said "clamp at render time" and nothing did), so "end" — and enough
//     presses of "down" — left a single row on screen.
//   - Nothing named the scroll keys, and the three reasons a section can be
//     empty (no sessions, batch still in flight, a session's stats
//     unavailable) all rendered as silent zeros.

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// Panel geometry. statsPad is the left inset shared with statusOverlay and
// helpOverlay (dialogHeader's `pad` argument for a non-select dialog);
// statsLabelWidth is the key column every statsRow aligns its value against,
// sized to the longest label below ("Total Messages").
const (
	statsPad        = 2
	statsLabelWidth = 16
	statsIndent     = 2
	// statsChrome is every panel row that is not body: overlayPanel's
	// PaddingTop, the header and its blank, the blank above the footer, the
	// hint row, and the trailing blank.
	statsChrome = 6
)

// openStatsOverlay opens the /stats panel and kicks off the fetch of per-
// session stats. The panel renders immediately with whatever data is already
// loaded (the active session's stats, at minimum) and refreshes when
// allStatsMsg arrives — the "open synchronously from cache, refresh in the
// background" rule every other dialog follows.
func (a *App) openStatsOverlay() tea.Msg {
	a.overlay = &overlay{kind: overlayStats, title: "Stats", size: dialogXLarge}
	a.overlay.scrollTop = 0
	return a.loadAllStats()
}

// statsRow renders one aligned key/value row: the label muted in a fixed
// column, then the value in Text — the number is the content, the label is
// the annotation, the same hierarchy the sidebar's sections use.
func (a *App) statsRow(inner int, label, value string) string {
	key := truncateRunes(label, statsLabelWidth)
	key += strings.Repeat(" ", statsLabelWidth-lipgloss.Width(key))
	value = truncateRunes(value, max(1, inner-statsLabelWidth-1))
	return strings.Repeat(" ", statsPad) +
		a.onPanel(a.theme.TextMuted, false).Render(key) + " " +
		a.onPanel(a.theme.Text, false).Render(value)
}

// statsHeading renders a section title, matching the sidebar's bold-Text
// section headers.
func (a *App) statsHeading(inner int, title string) string {
	return strings.Repeat(" ", statsPad) +
		a.onPanel(a.theme.Text, true).Render(truncateRunes(title, inner))
}

// statsEntry renders the title line of a per-model / per-session entry.
func (a *App) statsEntry(inner int, title string) string {
	return strings.Repeat(" ", statsPad) +
		a.onPanel(a.theme.Text, true).Render(truncateRunes(title, inner))
}

// statsDetail renders an entry's indented muted detail line.
func (a *App) statsDetail(inner int, text string) string {
	return strings.Repeat(" ", statsPad+statsIndent) +
		a.onPanel(a.theme.TextMuted, false).Render(
			truncateRunes(text, max(1, inner-statsIndent)))
}

// statsNote renders an advisory line — a partial load, an unavailable
// session — in the same column as the section's rows.
func (a *App) statsNote(inner int, text string) string {
	return strings.Repeat(" ", statsPad) +
		a.onPanel(a.theme.TextMuted, false).Render(truncateRunes(text, inner))
}

// statsEmptyView is the panel's empty state, distinguishing the three reasons
// there is nothing to report. An error title reuses the list dialog's
// emptyView colors (Error for the title, muted for the body).
func (a *App) statsEmptyView(inner int, title, body string) []string {
	lines := []string{strings.Repeat(" ", statsPad) +
		a.onPanel(a.theme.Error, true).Render(truncateRunes(title, inner))}
	for _, line := range wrapWords(body, inner) {
		lines = append(lines, strings.Repeat(" ", statsPad)+
			a.onPanel(a.theme.TextMuted, false).Render(line))
	}
	return lines
}

// percent formats a cache hit rate. One control, so the aggregate, per-model
// and per-session figures cannot drift apart in precision.
func statsPercent(rate float64) string { return fmt.Sprintf("%.2f%%", rate) }

// statsOverlay renders the full stats panel. It is called from overlayPanel
// (dialogs.go) whenever the active overlay is overlayStats.
//
// The header and the hint row are fixed; only the body between them scrolls.
func (a *App) statsOverlay(w int) string {
	// The panel's own content column: the width minus the left inset and a
	// matching right margin, so nothing reaches the edge spliceAt cuts at.
	inner := max(8, w-2*statsPad)

	agg := a.computeStatsAggregate()
	body := a.statsBody(inner, agg)

	// Row budget. overlayPanel wraps this content in PaddingTop(1), so the
	// panel occupies
	//
	//   1 (paddingTop) + 2 (header + blank) + body + 1 (blank) + indicator
	//     + 1 (hints) + 1 (trailing blank)
	//
	// rows, i.e. statsChrome + body + indicator.
	//
	// It is anchored at overlayOrigin's height/4 and leaves the same margin
	// below, so it reads as a panel floating over the page rather than one
	// glued to the bottom edge. Growing to fill every row down to the last is
	// what a docked column does; a dialog does not — every other dialog here
	// caps its body well short of the screen (listBody's height/2-6).
	//
	// The screen fit is the hard bound and the bottom margin is the
	// preference, so the margin is what gives way first on a short terminal.
	// The floor is 1, not 3: a floor that violates the fit is exactly the
	// overflow it is meant to prevent, and frame()'s crop takes the hint row —
	// the one part naming the way out — first.
	panelTop := a.height / 4
	balanced := a.height - 2*panelTop - statsChrome
	fits := a.height - panelTop - statsChrome
	avail := max(1, min(balanced, fits))
	rows := avail
	scrollable := len(body) > rows
	if scrollable {
		rows = max(1, avail-1) // the "more lines" indicator costs a row
		scrollable = len(body) > rows
	}

	// Clamp scrollTop against the real maximum and write it back, the same way
	// listBody settles o.scrollTop during its own render — this is what makes
	// "end" (which parks scrollTop at a sentinel) and a long press of "down"
	// stop at the last screenful instead of running off it.
	start := 0
	if scrollable {
		start = min(max(a.overlay.scrollTop, 0), len(body)-rows)
	}
	a.overlay.scrollTop = start

	lines := []string{a.dialogHeader(statsPad, "Stats", "esc", w), ""}
	if scrollable {
		lines = append(lines, body[start:start+rows]...)
		// The blank row separates the footer from the body the same way it
		// separates every section above it.
		lines = append(lines, "", a.statsScrollIndicator(inner, start, rows, len(body)))
	} else {
		lines = append(lines, body...)
		lines = append(lines, "")
	}
	return strings.Join(append(lines, a.statsHints(w, scrollable), ""), "\n")
}

// statsScrollIndicator reports what the window is hiding, in the timeline's
// own "↑ N more lines" wording.
func (a *App) statsScrollIndicator(inner, start, rows, total int) string {
	var parts []string
	if start > 0 {
		parts = append(parts, fmt.Sprintf("↑ %d more", start))
	}
	if end := start + rows; end < total {
		parts = append(parts, fmt.Sprintf("↓ %d more", total-end))
	}
	return strings.Repeat(" ", statsPad) +
		a.onPanel(a.theme.TextMuted, false).Render(
			truncateRunes(strings.Join(parts, "   "), inner))
}

// statsHints is the panel's key hint row: every affordance names its key, and
// the scroll keys appear only while there is something to scroll.
func (a *App) statsHints(w int, scrollable bool) string {
	hint := func(key, label string) string {
		return a.onPanel(a.theme.Text, false).Render(key) + " " +
			a.onPanel(a.theme.TextMuted, false).Render(label)
	}
	parts := []string{}
	if scrollable {
		parts = append(parts, hint("↑↓", "scroll"), hint("pgup/pgdn", "page"))
	}
	parts = append(parts, hint("esc", "close"))
	return strings.Repeat(" ", statsPad) +
		truncateRunes(strings.Join(parts, "  "), max(1, w-2*statsPad))
}

// statsBody builds the scrollable body: the aggregate sections, then the
// per-model and per-session breakdowns.
func (a *App) statsBody(inner int, agg statsAggregate) []string {
	// The three reasons there is nothing to show, told apart. Reporting all
	// of them as a table of zeros is what makes a batch still in flight
	// indistinguishable from a fresh install with no history.
	switch {
	case agg.sessionCount == 0:
		return a.statsEmptyView(inner, "No sessions",
			"Usage is reported per session. Start one and its tokens, cost and cache hit rate will appear here.")
	case agg.loaded == 0 && agg.pending > 0:
		return a.statsEmptyView(inner, "Loading usage",
			fmt.Sprintf("Fetching stats for %s.", plural(agg.pending, "session")))
	case agg.loaded == 0:
		return a.statsEmptyView(inner, "No usage reported",
			"The server returned no stats for any session. It may not implement the stats endpoint.")
	}

	lines := []string{
		a.statsHeading(inner, "Overview"),
		"",
		a.statsRow(inner, "Sessions", localeNumber(agg.sessionCount)),
		a.statsRow(inner, "Total Cost", formatMoney(agg.cost)),
		a.statsRow(inner, "Total Messages", localeNumber(agg.messages)),
	}
	// Say so while the picture is still incomplete, rather than presenting a
	// partial total as a final one.
	if agg.pending > 0 {
		lines = append(lines, a.statsNote(inner,
			fmt.Sprintf("%s still loading", plural(agg.pending, "session"))))
	}
	if agg.unavailable > 0 {
		lines = append(lines, a.statsNote(inner,
			fmt.Sprintf("%s reported no stats", plural(agg.unavailable, "session"))))
	}

	lines = append(lines,
		"",
		a.statsHeading(inner, "Tokens"),
		"",
		a.statsRow(inner, "Input (fresh)", localeNumber(agg.input)),
		a.statsRow(inner, "Output", localeNumber(agg.output)),
		a.statsRow(inner, "Reasoning", localeNumber(agg.reasoning)),
		a.statsRow(inner, "Cache Read", localeNumber(agg.cacheRead)),
		a.statsRow(inner, "Cache Write", localeNumber(agg.cacheWrite)),
	)

	totalPrompt := agg.input + agg.cacheRead
	var hitPct float64
	if totalPrompt > 0 {
		hitPct = float64(agg.cacheRead) / float64(totalPrompt) * 100
	}
	lines = append(lines,
		"",
		a.statsHeading(inner, "Prompt Cache"),
		"",
		a.statsRow(inner, "Total Prompt", localeNumber(totalPrompt)),
		a.statsRow(inner, "Cache Hits", localeNumber(agg.cacheRead)),
		a.statsRow(inner, "Hit Rate", statsPercent(hitPct)),
	)

	if len(agg.models) > 0 {
		lines = append(lines, "", a.statsHeading(inner, "By Model"), "")
		for i, m := range agg.models {
			if i > 0 {
				lines = append(lines, "")
			}
			lines = append(lines,
				a.statsEntry(inner, m.providerID+"/"+m.modelID),
				a.statsDetail(inner, fmt.Sprintf("%s · %s · %s hit",
					plural(m.sessionCount, "session"), formatMoney(m.cost), statsPercent(m.hitRate))),
				a.statsDetail(inner, fmt.Sprintf("in %s · out %s · cache %s",
					localeNumber(m.input), localeNumber(m.output), localeNumber(m.cacheRead))),
			)
		}
	}

	if len(agg.sessions) > 0 {
		lines = append(lines, "", a.statsHeading(inner, "By Session"), "")
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
				a.statsEntry(inner, title),
				a.statsDetail(inner, detail),
			)
			// A session the batch has not resolved has nothing to break
			// down; the zeros a token line would print are not its usage.
			if s.resolved {
				lines = append(lines, a.statsDetail(inner,
					fmt.Sprintf("in %s · out %s · cache %s · %s hit",
						localeNumber(s.input), localeNumber(s.output),
						localeNumber(s.cacheRead), statsPercent(s.hitRate))))
			} else {
				lines = append(lines, a.statsDetail(inner, s.pendingLabel))
			}
		}
	}
	return lines
}

// --- aggregate types --------------------------------------------------------

type statsAggregate struct {
	sessionCount int
	// loaded / pending / unavailable partition sessionCount by what the
	// stats batch actually knows about each session, so the panel can tell
	// "still fetching" from "the server has nothing" instead of rendering
	// both as zeros. loadAllStats swallows per-session errors (a missing
	// endpoint for one session must not blank the whole overlay), so a
	// session absent from a batch that HAS landed is unavailable, and one
	// absent because no batch has landed yet is pending.
	loaded      int
	pending     int
	unavailable int
	messages    int
	cost        float64
	input       int
	output      int
	reasoning   int
	cacheRead   int
	cacheWrite  int
	models      []modelStats
	sessions    []sessionStats
}

type modelStats struct {
	providerID   string
	modelID      string
	sessionCount int
	cost         float64
	input        int
	output       int
	cacheRead    int
	hitRate      float64
}

type sessionStats struct {
	sessionID string
	title     string
	// resolved reports whether stats for this session arrived; pendingLabel
	// is what the breakdown says in their place when they did not.
	resolved     bool
	pendingLabel string
	// updated / created are the session's own timestamps, carried so the
	// breakdown can be ordered by recency. updated is the last activity and
	// is what "latest" means here; created is the tie-break for a session
	// the server has never stamped an update on.
	updated    int64
	created    int64
	providerID string
	modelID    string
	cost       float64
	input      int
	output     int
	cacheRead  int
	messages   int
	hitRate    float64
}

// modelUsage is the sort key for the By Model section: every token attributed
// to the model.
func modelUsage(m modelStats) int { return m.input + m.output + m.cacheRead }

// sessionRecency is the sort key for the By Session section: last activity,
// falling back to creation for a session with no recorded update.
func sessionRecency(s sessionStats) int64 {
	if s.updated > 0 {
		return s.updated
	}
	return s.created
}

// computeStatsAggregate walks the loaded sessions + allStats (and the active
// session's own stats as a fallback) and produces the aggregate the overlay
// renders.
func (a *App) computeStatsAggregate() statsAggregate {
	agg := statsAggregate{sessionCount: len(a.sessions)}

	// Build a set of (providerID, modelID) -> per-model accumulator.
	type modelKey struct {
		providerID, modelID string
	}
	modelAccum := map[modelKey]*modelStats{}
	sessionList := make([]sessionStats, 0, len(a.sessions))

	for _, s := range a.sessions {
		// Resolve stats: prefer the allStats batch, fall back to the active
		// session's own stats (which may have arrived before allStats).
		var st *client.Stats
		if a.allStats != nil {
			st = a.allStats[s.ID]
		}
		if st == nil && a.active != nil && s.ID == a.active.ID {
			st = a.stats
		}
		if st == nil {
			// No stats for this session. Which of the two reasons applies
			// decides both the counter and what its row says: a batch that
			// has not landed yet is still coming, one that has landed
			// without this session is not.
			ss := sessionStats{
				sessionID: s.ID,
				title:     s.Title,
				updated:   s.TimeUpdated,
				created:   s.TimeCreated,
			}
			if a.allStats == nil {
				agg.pending++
				ss.pendingLabel = "loading…"
			} else {
				agg.unavailable++
				ss.pendingLabel = "no stats reported"
			}
			if s.Model != nil {
				ss.providerID = s.Model.ProviderID
				ss.modelID = s.Model.ID
			}
			sessionList = append(sessionList, ss)
			continue
		}
		agg.loaded++

		agg.cost += st.Cost
		agg.input += st.TokensInput
		agg.output += st.TokensOutput
		agg.reasoning += st.TokensReasoning
		agg.cacheRead += st.TokensCacheRead
		agg.cacheWrite += st.TokensCacheWrite
		agg.messages += st.Messages

		var providerID, modelID string
		if s.Model != nil {
			providerID = s.Model.ProviderID
			modelID = s.Model.ID
		}

		prompt := st.TokensInput + st.TokensCacheRead
		var hitRate float64
		if prompt > 0 {
			hitRate = float64(st.TokensCacheRead) / float64(prompt) * 100
		}

		ss := sessionStats{
			sessionID:  s.ID,
			title:      s.Title,
			resolved:   true,
			updated:    s.TimeUpdated,
			created:    s.TimeCreated,
			providerID: providerID,
			modelID:    modelID,
			cost:       st.Cost,
			input:      st.TokensInput,
			output:     st.TokensOutput,
			cacheRead:  st.TokensCacheRead,
			messages:   st.Messages,
			hitRate:    hitRate,
		}
		sessionList = append(sessionList, ss)

		key := modelKey{providerID, modelID}
		m, ok := modelAccum[key]
		if !ok {
			m = &modelStats{providerID: providerID, modelID: modelID}
			modelAccum[key] = m
		}
		m.sessionCount++
		m.cost += st.Cost
		m.input += st.TokensInput
		m.output += st.TokensOutput
		m.cacheRead += st.TokensCacheRead
	}

	// Finalize model hit rates.
	for _, m := range modelAccum {
		prompt := m.input + m.cacheRead
		if prompt > 0 {
			m.hitRate = float64(m.cacheRead) / float64(prompt) * 100
		} else {
			m.hitRate = 0
		}
	}

	// Models are ordered by usage, heaviest first — the question this
	// section answers is "where is the work (and the money) going", and the
	// answer belongs at the top. Usage is every token attributed to the
	// model, not just the prompt side: an expensive model earning its place
	// on output alone should not sort below a cheap one with a large cached
	// prompt.
	agg.models = make([]modelStats, 0, len(modelAccum))
	for _, m := range modelAccum {
		agg.models = append(agg.models, *m)
	}
	sort.Slice(agg.models, func(i, j int) bool {
		a, b := agg.models[i], agg.models[j]
		if ua, ub := modelUsage(a), modelUsage(b); ua != ub {
			return ua > ub
		}
		// Deterministic tie-breaks, so a model with no usage yet does not
		// shuffle between renders.
		if a.cost != b.cost {
			return a.cost > b.cost
		}
		return a.providerID+"/"+a.modelID < b.providerID+"/"+b.modelID
	})

	// Sessions are ordered by recency, latest first — a session list is read
	// as "what have I been doing", so the one just worked in belongs at the
	// top. TimeUpdated is the last activity; TimeCreated is the fallback for
	// a session the server has never stamped an update on, and the ID breaks
	// a remaining tie so the order is stable across renders.
	sort.Slice(sessionList, func(i, j int) bool {
		a, b := sessionList[i], sessionList[j]
		if at, bt := sessionRecency(a), sessionRecency(b); at != bt {
			return at > bt
		}
		return a.sessionID > b.sessionID
	})
	agg.sessions = sessionList

	return agg
}
