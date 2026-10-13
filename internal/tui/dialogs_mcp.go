package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// The /mcp dialog: every configured MCP server — enabled or not — with live
// enable/disable, delete and a details drill-in.
//
// It is an ordinary DialogSelect built the way the plugins dialog is: rows
// from the cached management list, the shared overlay hooks for the
// behavior, and mutations that run against the HTTP API so the same actions
// from `gocode mcp enable/disable/remove` do the same thing live.
//
// Unlike the plugins dialog — which edits a working copy and saves once on
// close, because a plugin load needs a restart — every MCP mutation is
// applied the moment it is requested: enable connects and registers tools,
// disable disconnects and unregisters them, delete removes the config entry.
// The dialog therefore never needs a save-on-close path, only a refresh
// after each mutation so the row the user just acted on shows the result.
//
// The overlay hooks it uses:
//
//	onActivate  enter (and a click on a row) toggles instead of selecting,
//	            so the dialog stays open while several servers are flipped.
//	actions     toggle ctrl+t (the same key the plugins dialog uses, for
//	            muscle memory), delete ctrl+d behind the same arm-then-confirm
//	            the session and memory deletes use, details →, ← back.

// mcpInfoMsg carries the management list a refresh fetched.
type mcpInfoMsg struct {
	servers []client.MCPServerInfo
}

// loadMCPInfoCmd fetches the management list (GET /api/mcp/servers).
func (a *App) loadMCPInfoCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		servers, err := c.MCPServersInfo(a.ctx)
		if err != nil {
			return statusMsg{text: "failed to load MCP servers: " + err.Error(), isErr: true}
		}
		return mcpInfoMsg{servers: servers}
	}
}

// mcpMutationMsg carries a finished mutation's toast text.
type mcpMutationMsg struct {
	text  string
	isErr bool
}

// mcpOverlay opens the dialog from the cached list and refreshes in the
// background — the open-synchronously-from-cache rule every dialog follows.
func (a *App) mcpOverlay() tea.Cmd {
	a.openMCPDialog(a.mcpInfo)
	return a.loadMCPInfoCmd()
}

// openMCPDialog opens (or returns to) the dialog's list level.
func (a *App) openMCPDialog(servers []client.MCPServerInfo) {
	a.mcpDetail = ""
	a.openList("MCP Servers", mcpItems(servers))
	o := a.overlay
	o.SetSize(dialogLarge)
	o.SetPlaceholder("Search servers...")
	o.SetOnActivate(func(item overlayItem) tea.Cmd {
		return a.toggleMCPServer(item)
	})
	o.SetActions([]dialogAction{
		{Title: "toggle", Keys: "ctrl+t", OnTrigger: func(item overlayItem) tea.Cmd {
			return a.toggleMCPServer(item)
		}},
		{Title: "delete", Keys: "ctrl+d", OnTrigger: a.deleteMCPServerAction},
		{Title: "details", Keys: "right", OnTrigger: a.drillMCPItem},
	})
	if !o.HasItems() {
		o.SetEmptyView("No MCP servers",
			"Add one with `gocode mcp add`, then it appears here.")
	}
}

// mcpItems renders the list rows. Enabled servers carry the ✓ gutter the
// plugins dialog uses; the hint repeats the word so the filter finds both
// states, then the live status — the same words the sidebar's status labels
// use, so a row and the sidebar never disagree about vocabulary.
func mcpItems(servers []client.MCPServerInfo) []overlayItem {
	items := make([]overlayItem, 0, len(servers))
	for _, server := range servers {
		gutter := ""
		if server.Enabled {
			gutter = "✓"
		}
		// The right-aligned footer is the server's context cost, the way
		// the model dialog right-aligns pricing. Connected: what it costs
		// right now. Disabled mid-run: the last-known cost, prefixed "+"
		// — what enabling it would cost back. Never measured (booted
		// disabled, needs auth, failed): "—", because the number would be
		// a guess, not a measurement.
		footer := "—"
		switch {
		case server.CostLive:
			footer = fmt.Sprintf("%d tools · %s", server.ToolCount, formatTokens(server.EstTokens))
		case server.ToolCount > 0:
			footer = fmt.Sprintf("+%d tools · %s", server.ToolCount, formatTokens(server.EstTokens))
		}
		items = append(items, overlayItem{
			Label:    server.Name,
			Hint:     enabledWord(server.Enabled) + " — " + mcpStatusWord(server.Status),
			Value:    server.Name,
			Footer:   footer,
			Gutter:   gutter,
			GutterOK: true,
		})
	}
	return items
}

// mcpStatusWord maps a status string to the short word the row's hint uses.
// It deliberately reuses the sidebar's vocabulary: connected, disabled,
// failed, needs authentication — the long hint forms stay in the details.
func mcpStatusWord(status string) string {
	switch status {
	case "connected":
		return "connected"
	case "disabled":
		return "disabled"
	case "needs_auth":
		return "needs authentication"
	case "needs_client_registration":
		return "needs client registration"
	case "connecting":
		return "connecting"
	default:
		return "failed"
	}
}

// toggleMCPServer enables or disables the selected server and refreshes the
// dialog. Optimistic on the row: the flip is applied locally so the ✓ moves
// with the keypress, and the refresh corrects the status half a beat later.
func (a *App) toggleMCPServer(item overlayItem) tea.Cmd {
	name := item.Value
	if name == "" {
		return nil
	}
	enabled := mcpEnabled(a.mcpInfo, name)
	for i := range a.mcpInfo {
		if a.mcpInfo[i].Name == name {
			a.mcpInfo[i].Enabled = !enabled
			a.mcpInfo[i].Status = "connecting"
		}
	}
	a.rebuildMCPItems()
	c := a.client
	return func() tea.Msg {
		var err error
		var action string
		if !enabled {
			err = c.EnableMCP(a.ctx, name)
			action = "enabled"
		} else {
			err = c.DisableMCP(a.ctx, name)
			action = "disabled"
		}
		if err != nil {
			return mcpMutationMsg{text: "failed to " + action + " " + name + ": " + err.Error(), isErr: true}
		}
		return tea.Batch(a.loadMCPInfoCmd(), staticMsg(mcpMutationMsg{text: name + " " + action}))
	}
}

// deleteMCPServerAction is the two-press delete the session and memory lists
// use: the first press arms the row, the second commits.
func (a *App) deleteMCPServerAction(item overlayItem) tea.Cmd {
	o := a.overlay
	if o == nil {
		return nil
	}
	if o.Armed() != item.Value {
		o.Arm(item.Value, "ctrl+d")
		return nil
	}
	o.Disarm()
	name := item.Value
	// Drop the row immediately: the refresh re-adds nothing for a server
	// the config no longer names.
	kept := a.mcpInfo[:0]
	for _, info := range a.mcpInfo {
		if info.Name != name {
			kept = append(kept, info)
		}
	}
	a.mcpInfo = kept
	a.rebuildMCPItems()
	c := a.client
	return func() tea.Msg {
		if err := c.DeleteMCP(a.ctx, name); err != nil {
			return mcpMutationMsg{text: "failed to delete " + name + ": " + err.Error(), isErr: true}
		}
		return tea.Batch(a.loadMCPInfoCmd(), staticMsg(mcpMutationMsg{text: name + " removed"}))
	}
}

// drillMCPItem opens the detail level for a server: the same list surface,
// its rows replaced by the server's identity and state.
func (a *App) drillMCPItem(item overlayItem) tea.Cmd {
	name := item.Value
	info, ok := mcpLookup(a.mcpInfo, name)
	if !ok {
		return nil
	}
	a.openMCPDetail(name, info)
	return nil
}

// openMCPDetail opens the detail level for a server, shared by the drill-in
// action and the refresh folding a newer info list into an open detail view.
func (a *App) openMCPDetail(name string, info client.MCPServerInfo) {
	a.mcpDetail = name
	a.openList("MCP / "+name, mcpDetailItems(info))
	o := a.overlay
	o.SetSize(dialogLarge)
	// A detail row is information, not a choice: enter must not fall
	// through to activateItem, which would toggle the server.
	o.SetOnActivate(func(overlayItem) tea.Cmd { return nil })
	o.SetActions([]dialogAction{
		{Title: "back", Keys: "left", OnTrigger: func(overlayItem) tea.Cmd {
			a.openMCPDialog(a.mcpInfo)
			return nil
		}},
	})
}

// mcpDetailItems is the drill-in body: identity and state as label/value
// rows. Field names carry no padding of their own — listRow aligns every
// hint at one column (§9.7: never hand-count the padding per row).
func mcpDetailItems(info client.MCPServerInfo) []overlayItem {
	var items []overlayItem
	field := func(name, value string) {
		if value == "" {
			return
		}
		items = append(items, overlayItem{Label: name, Hint: value, Value: name})
	}
	field("type", info.Type)
	if info.Type == "local" && len(info.Command) > 0 {
		field("command", strings.Join(info.Command, " "))
	}
	field("url", info.URL)
	field("enabled", enabledWord(info.Enabled))
	field("status", mcpStatusWord(info.Status))
	field("error", info.Error)
	switch {
	case info.CostLive:
		field("tools", fmt.Sprintf("%d", info.ToolCount))
		field("context cost", fmt.Sprintf("~%s tokens (estimated: tool names, descriptions and schemas)", formatTokens(info.EstTokens)))
	case info.ToolCount > 0:
		field("tools", fmt.Sprintf("%d (last known)", info.ToolCount))
		field("context cost", fmt.Sprintf("~%s tokens would return on enable — not in the context now", formatTokens(info.EstTokens)))
	default:
		field("context cost", "0 — contributes no tools while not connected")
	}
	if info.Status == "connected" {
		field("note", "changes apply immediately")
	} else {
		field("note", "changes apply immediately; connect issues show as failed")
	}
	return items
}

// rebuildMCPItems refills the list rows after a toggle or delete, keeping
// whatever the filter input holds.
func (a *App) rebuildMCPItems() {
	o := a.overlay
	if o == nil {
		return
	}
	o.SetAllItemsKeepIndex(mcpItems(a.mcpInfo))
}

// mcpLookup finds one server's info by name.
func mcpLookup(servers []client.MCPServerInfo, name string) (client.MCPServerInfo, bool) {
	for _, info := range servers {
		if info.Name == name {
			return info, true
		}
	}
	return client.MCPServerInfo{}, false
}

// mcpEnabled reports the cached enable state for name.
func mcpEnabled(servers []client.MCPServerInfo, name string) bool {
	info, ok := mcpLookup(servers, name)
	return ok && info.Enabled
}
