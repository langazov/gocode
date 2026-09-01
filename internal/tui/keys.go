package tui

// Keybinding helpers, against packages/tui/src/config/keybind.ts.
//
// The bindings themselves live where they are handled (handleKey in app.go,
// handleOverlayKey in dialogs.go); this file holds the actions that had no
// implementation to bind to.

import (
	tea "charm.land/bubbletea/v2"

	"github.com/anomalyco/opencode-go/internal/tui/client"
)

// scrollMessages moves the timeline window by delta lines, positive being
// backwards through history (messages_page_up and friends).
func (a *App) scrollMessages(delta int) tea.Cmd {
	a.scrollOffset += delta
	if max := a.maxScrollOffset(); a.scrollOffset > max {
		a.scrollOffset = max
	}
	if a.scrollOffset < 0 {
		a.scrollOffset = 0
	}
	return nil
}

// maxScrollOffset is how far back the timeline can scroll: everything above
// the viewport. viewChat clamps to the same bound as it windows.
func (a *App) maxScrollOffset() int {
	lines, _ := a.buildTimeline()
	if over := len(lines) - a.viewportHeight(); over > 0 {
		return over
	}
	return 0
}

// cycleAgent is agent_cycle / agent_cycle_reverse (tab / shift+tab): step to
// the next agent in the roster and pin the session to it, the same switch the
// agent dialog performs. The prompt's meta row and the hint row both already
// referred to this ("tab agents") with nothing bound to it.
func (a *App) cycleAgent(direction int) tea.Cmd {
	if len(a.agents2) == 0 {
		return a.loadAgentsCmd(direction)
	}
	current := 0
	for i, agent := range a.agents2 {
		if agent.ID == a.activeAgentOr("build") {
			current = i
			break
		}
	}
	next := a.agents2[((current+direction)%len(a.agents2)+len(a.agents2))%len(a.agents2)]
	a.activeAgent = next.ID
	if a.active == nil {
		return staticMsg(statusMsg{text: "agent: " + next.ID})
	}
	c, sessionID := a.client, a.active.ID
	return func() tea.Msg {
		if err := c.SetAgent(a.ctx, sessionID, next.ID); err != nil {
			return statusMsg{text: "agent switch failed: " + err.Error()}
		}
		return statusMsg{text: "agent: " + next.ID}
	}
}

// loadAgentsCmd fetches the roster, then retries the cycle that needed it.
func (a *App) loadAgentsCmd(direction int) tea.Cmd {
	c := a.client
	return func() tea.Msg {
		agents, err := c.Agents(a.ctx)
		if err != nil || len(agents) == 0 {
			return nil
		}
		return agentsLoadedMsg{agents: agents, cycle: direction}
	}
}

// agentsLoadedMsg carries the roster back, with the pending cycle direction so
// the keypress that triggered the fetch still takes effect.
type agentsLoadedMsg struct {
	agents []client.Agent
	cycle  int
}
