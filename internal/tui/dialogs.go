package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/dialog"
	"github.com/langazov/gocode-go/internal/tui/theme"
)

// This file is the App-side half of the dialog system: it builds dialog
// shells (internal/tui/dialog) with the interface's behavior wired in, and
// holds the panel-width constants the TS size props name. The rendering and
// keyboard engine itself lives in the dialog package, on charm.land/huh/v2.
//
// Dialog panel widths, from the size prop in ui/dialog.tsx.
const (
	dialogMedium = dialog.Medium
	dialogLarge  = dialog.Large
	dialogXLarge = dialog.XLarge
)

// Type aliases keep the rest of the TUI reading the way it always did.
type (
	overlayItem  = dialog.Item
	dialogAction = dialog.Action
)

// palette converts the App's theme into the dialog renderer's palette —
// the one place the token mapping happens.
func (a *App) palette() dialog.Palette {
	return dialog.PaletteFrom(
		a.theme.Primary, a.theme.Accent, a.theme.Error, a.theme.Success,
		a.theme.Text, a.theme.TextMuted,
		a.theme.BackgroundPanel, a.theme.BackgroundElement,
		a.theme.SelectedListItemText,
	)
}

// mount installs a shell as the open dialog, applying the current geometry
// and theme so its first render is correct without a resize round trip.
func (a *App) mount(s *dialog.Shell) *dialog.Shell {
	s.SetGeometry(a.width, a.height, a.palette())
	a.overlay = s
	return s
}

// openAlert mirrors DialogAlert.show: a titled message with a single ok
// button, dismissed by enter or escape.
func (a *App) openAlert(title, message string, onConfirm func() tea.Msg) {
	a.mount(dialog.NewAlert(title, message, onConfirm))
}

// openConfirm mirrors DialogConfirm.show. cancelLabel overrides the left
// button's text; onCancel also runs when the dialog is dismissed with escape,
// matching the TS promise resolving via the dialog's onClose.
func (a *App) openConfirm(title, message, cancelLabel string, onConfirm, onCancel func() tea.Msg) {
	a.mount(dialog.NewConfirm(title, message, cancelLabel, onConfirm, onCancel))
}

func (a *App) openList(title string, items []overlayItem) {
	a.mount(dialog.NewList(title, items))
}

// openInput opens a prompt dialog. value is what the field starts out
// holding, placeholder the muted text shown while it is empty — two
// different things, spelled differently at every call site.
//
// They used to share one argument named "placeholder" that was in fact the
// initial value, and the provider dialog took the name at its word: the API
// key field opened pre-filled with the literal string "Paste your API key",
// which had to be deleted before a key could be typed and was submitted as
// the key by anyone who just pressed enter.
func (a *App) openInput(title, value, placeholder string, onSubmit func(string) tea.Msg) {
	s := a.mount(dialog.NewInput(title, placeholder, value))
	s.OnInputSubmit(onSubmit)
}

func (a *App) closeOverlay() {
	a.overlay = nil
}

// resolveOverlay closes the dialog and dispatches the chosen branch, the
// shared tail of the alert and confirm button handlers (mouse.go's click
// path and the shell's keyboard path both land here).
func (a *App) resolveOverlay(branch func() tea.Msg) tea.Cmd {
	a.closeOverlay()
	if branch == nil {
		return nil
	}
	if result := branch(); result != nil {
		if cmd, ok := result.(tea.Cmd); ok {
			return cmd
		}
		return staticMsg(result)
	}
	return nil
}

// handleOverlayKey routes one key name through the open shell and adapts the
// returned commands: a CloseMsg drops the dialog, anything else flows on as
// a message the App's Update loop resolves (staticMsg/tea.Cmd both accepted).
func (a *App) handleOverlayKey(key string) tea.Cmd {
	s := a.overlay
	if s == nil {
		return nil
	}
	cmd := s.Key(key)
	return a.wrapDialogCmd(cmd)
}

// wrapDialogCmd flattens what a shell's key dispatch returns: a CloseMsg
// closes the dialog; a wrapped tea.Cmd from a branch is run; nil stays nil.
func (a *App) wrapDialogCmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	switch m := msg.(type) {
	case dialog.CloseMsg:
		a.closeOverlay()
		return nil
	case dialog.CloseThenMsg:
		// Close first, then hand the carried command back to the runtime —
		// the action may open its own dialog (the variant picker hand-off).
		a.closeOverlay()
		if m.Then == nil {
			return nil
		}
		return m.Then()
	case tea.BatchMsg:
		var cmds []tea.Cmd
		for _, inner := range m {
			if c := a.wrapDialogCmd(inner); c != nil {
				cmds = append(cmds, c)
			}
		}
		return tea.Batch(cmds...)
	default:
		return staticMsg(msg)
	}
}

// activateItem runs the selected item's action, mirroring DialogSelect's
// enter/onSelect: closes the dialog first, then dispatches whatever the
// action returns. Shared by the enter key and a mouse click/release on the
// row (see mouse.go's overlayMouseTarget/handleClick).
func (a *App) activateItem(item overlayItem) tea.Cmd {
	if a.overlay == nil {
		return nil
	}
	// An onActivate hook replaces the default close-then-run wholesale —
	// including with a nil return (the plugins detail level swallows enter
	// so a row of information cannot close the dialog).
	if cmd := a.overlay.OnActivate(item); cmd != nil || a.overlay.HasActivateHook() {
		return a.wrapDialogCmd(cmd)
	}
	// Default: close, then run — the same order the shell's enter key uses
	// (listField.Activate), so keyboard and mouse cannot disagree.
	a.closeOverlay()
	return runItemAction(item)
}

// runItemAction runs a registry item's action and returns the command it
// implies.
//
// An action's declared result is tea.Msg, but several of them return a
// tea.Cmd — the real implementations they delegate to (newSession,
// modelsOverlay, compactNow) are command-producing. Returning one as a message
// would leave it sitting in the update loop unexecuted, so it is unwrapped
// here. Both the palette and the inline "/" popup go through this, or one of
// them silently does nothing.
func runItemAction(item overlayItem) tea.Cmd {
	if item.Action == nil {
		return nil
	}
	return itemResult(item.Action())
}

// runItemActionWithArgs dispatches "/name args". A command that declares no
// argAction ignores the arguments, which is how every interface command
// behaved before argAction existed.
func runItemActionWithArgs(item overlayItem, arguments string) tea.Cmd {
	return dialog.RunItemActionWithArgs(item, arguments)
}

// itemResult normalizes what an action returns: a tea.Cmd is run as-is,
// anything else is delivered as a message, and nil does nothing.
func itemResult(result tea.Msg) tea.Cmd {
	if result == nil {
		return nil
	}
	if cmd, ok := result.(tea.Cmd); ok {
		return cmd
	}
	return staticMsg(result)
}

// wrapWords is the exported alias for the dialog package's shared wrapper;
// the stats and status panels still call it by its old name.
func wrapWords(text string, width int) []string {
	return dialog.WrapWords(text, width)
}

// --- compositing ---------------------------------------------------------------

// viewOverlay composites the dialog panel over the underlying route at
// height/4, centered — the Dialog backdrop in ui/dialog.tsx.
//
// The base is framed first (its 1-column side margins are part of the
// terminal's coordinate space the panel is placed against), then parsed into
// a canvas, dimmed cell by cell, and composited with the panel layer in one
// pass — see composite.go for why this replaced the two-pass
// dimBackdrop+spliceAt chain.
func (a *App) viewOverlay() string {
	panel, _ := a.overlayPanel()
	base := a.frame(a.underlay())
	top, left := a.overlayOrigin(lipgloss.Width(panel))
	return a.compositeDialog(base, panel, top, left)
}

func (a *App) underlay() string {
	if a.diff != nil {
		return a.viewDiff()
	}
	if a.view == viewHome {
		return a.viewHome()
	}
	return a.viewChat()
}

// overlayPanel renders the active dialog panel, alongside the hit map mouse
// handling needs (see mouse.go's overlayMouseTarget). Geometry is re-applied
// first so a render always measures with the App's current terminal size —
// tests (and any path that resized outside WindowSizeMsg) mutate a.width/
// a.height directly.
func (a *App) overlayPanel() (string, *dialog.Hits) {
	a.overlay.SetGeometry(a.width, a.height, a.palette())
	return a.overlay.Panel()
}

// overlayOrigin is the panel's top-left screen cell within compositeOverlay's
// layout, shared with mouse hit-testing so both agree on where the panel is.
func (a *App) overlayOrigin(panelW int) (top, left int) {
	return a.overlay.Origin(panelW)
}

// spliceAt splices panel into base at the given absolute screen row/col,
// the ANSI-safe cell-slicing technique behind the toast panel's top-right
// placement (feature.go's compositeToast) and the narrow-terminal sidebar
// overlay (views.go's compositeSidebarOverlay). The dialog backdrop no
// longer uses it — see composite.go's compositeDialog.
func (a *App) spliceAt(base, panel string, top, left int) string {
	baseLines := strings.Split(base, "\n")
	for i, line := range baseLines {
		baseLines[i] = padPlain(line, a.width)
	}
	for len(baseLines) < a.height {
		baseLines = append(baseLines, strings.Repeat(" ", a.width))
	}
	panelLines := strings.Split(panel, "\n")
	panelW := lipgloss.Width(panel)
	for i, pline := range panelLines {
		row := top + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		prefix := sliceCells(baseLines[row], 0, left)
		suffix := sliceCells(baseLines[row], left+panelW, a.width)
		baseLines[row] = prefix + pline + suffix
	}
	return strings.Join(baseLines, "\n")
}

func padPlain(line string, width int) string {
	gap := width - lipgloss.Width(line)
	if gap <= 0 {
		return line
	}
	return line + strings.Repeat(" ", gap)
}

// sliceCells returns the ANSI-safe substring of line between cell columns
// start (inclusive) and end (exclusive), keeping escape sequences that apply
// to cells inside the window.
func sliceCells(line string, start, end int) string {
	if start == 0 && lipgloss.Width(line) <= end {
		return line
	}
	var out, pending, carry strings.Builder
	cells := 0
	inEscape := false
	started := false
	for _, r := range line {
		if inEscape {
			pending.WriteRune(r)
			if r != 'm' {
				continue
			}
			inEscape = false
			sequence := pending.String()
			pending.Reset()
			// carry is the style still in force at the current cell. An
			// earlier version kept only the escapes immediately preceding the
			// window's first cell, which silently dropped any style opened
			// further left — a line-level style (the dialog backdrop opens
			// one per line, see dim.go) was lost entirely for the slice after
			// the dialog panel, leaving those cells at terminal defaults.
			if sequence == "[m" || sequence == "[0m" {
				carry.Reset()
			} else {
				carry.WriteString(sequence)
			}
			if started {
				out.WriteString(sequence)
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			pending.WriteRune(r)
			continue
		}
		if cells >= start && cells < end {
			if !started {
				out.WriteString(carry.String())
				started = true
			}
			out.WriteRune(r)
		}
		cells++
		if cells >= end {
			break
		}
	}
	return out.String()
}

// --- dialog content builders ----------------------------------------------------

func (a *App) sessionsOverlay() {
	items := make([]overlayItem, 0, len(a.sessions))
	today := time.Now().Format("Mon Jan 2 2006")
	currentID := ""
	if a.active != nil {
		currentID = a.active.ID
	}
	for i := range a.sessions {
		session := a.sessions[i]
		// Children stay out of the session list (dialog-session-list.tsx's
		// `.filter(x => x.parentID === undefined)`): a subagent belongs to
		// its parent's task row and the children overlay, and a fork belongs
		// to the message it was forked from. Listing them here would bury
		// the user's own conversations under every spawn.
		if session.ParentID != "" {
			continue
		}
		category := ""
		if session.TimeUpdated > 0 {
			category = time.UnixMilli(session.TimeUpdated).Format("Mon Jan 2 2006")
			if category == today {
				category = "Today"
			}
		}
		footer := ""
		if session.Directory != "" {
			footer = truncateRunes(filepath.Base(session.Directory), 20)
		}
		sessionRef := session
		items = append(items, overlayItem{
			Label:    sessionTitleOf(session),
			Value:    session.ID,
			Category: category,
			Footer:   footer,
			Action: func() tea.Msg {
				// Same reasoning as the children overlay's action: through a
				// sessionOpenedMsg so the per-session state (child tracking,
				// subagent siblings, queue, run status) resets and reloads
				// instead of surviving the switch.
				return sessionOpenedMsg{session: &sessionRef}
			},
		})
	}
	a.openList("Sessions", items)
	o := a.overlay
	o.SetSize(dialogLarge)
	o.SetCurrent(currentID)
	o.SetActions([]dialogAction{
		{Title: "delete", Keys: "ctrl+d", OnTrigger: a.deleteSessionAction},
		{Title: "rename", Keys: "ctrl+r", OnTrigger: a.renameSessionAction},
	})
}

// deleteSessionAction mirrors the sessions dialog's two-press delete: the
// first press arms the row, the second deletes and refreshes the list.
func (a *App) deleteSessionAction(item overlayItem) tea.Cmd {
	o := a.overlay
	if o == nil {
		return nil
	}
	if o.Armed() != item.Value {
		o.Arm(item.Value, "ctrl+d")
		return nil
	}
	o.Disarm()
	c := a.client
	id := item.Value
	if a.active != nil && a.active.ID == id {
		a.active = nil
		a.view = viewHome
		a.timeline = nil
	}
	return func() tea.Msg {
		if err := c.Delete(a.ctx, id); err != nil {
			return statusMsg{text: "delete failed: " + err.Error()}
		}
		sessions, err := c.Sessions(a.ctx)
		if err != nil {
			return statusMsg{text: "failed to load sessions: " + err.Error()}
		}
		return sessionsMsg{sessions: sessions}
	}
}

// renameSessionAction swaps the sessions dialog for the rename prompt
// (DialogSessionRename).
func (a *App) renameSessionAction(item overlayItem) tea.Cmd {
	sessionID := item.Value
	title := ""
	for _, session := range a.sessions {
		if session.ID == sessionID {
			title = sessionTitleOf(session)
		}
	}
	a.openInput("Rename Session", title, "", func(value string) tea.Msg {
		if err := a.client.Rename(a.ctx, sessionID, value); err != nil {
			return statusMsg{text: "rename failed: " + err.Error()}
		}
		if a.active != nil && a.active.ID == sessionID {
			a.active.Title = value
		}
		for i := range a.sessions {
			if a.sessions[i].ID == sessionID {
				a.sessions[i].Title = value
			}
		}
		return statusMsg{text: "renamed"}
	})
	return nil
}

// agentsOverlay opens the agent dialog from the cached list and refreshes in
// the background, for the same reason modelsOverlay does.
func (a *App) agentsOverlay() tea.Cmd {
	a.openAgentDialog(a.agentList)
	return a.loadAgentListCmd()
}

func (a *App) openAgentDialog(agents []client.Agent) {
	items := make([]overlayItem, 0, len(agents))
	for _, agent := range agents {
		agent := agent
		items = append(items, overlayItem{
			Label: agent.ID,
			Hint:  agent.Description,
			Value: agent.ID,
			Action: func() tea.Msg {
				if a.active == nil {
					return statusMsg{text: "open a session first"}
				}
				if err := a.client.SetAgent(a.ctx, a.active.ID, agent.ID); err != nil {
					return statusMsg{text: "agent switch failed: " + err.Error()}
				}
				a.activeAgent = agent.ID
				return statusMsg{text: "agent: " + agent.ID}
			},
		})
	}
	a.openList("Select agent", items)
	o := a.overlay
	o.SetCurrent(a.activeAgentOr("build"))
	if len(agents) == 0 {
		o.SetEmptyView("Loading agents", "Fetching the agent list...")
	}
}

// loadAgentListCmd refreshes the agent list.
func (a *App) loadAgentListCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		agents, err := c.Agents(a.ctx)
		if err != nil {
			return nil
		}
		return agentListMsg{agents: agents}
	}
}

// agentListMsg carries a refreshed agent list.
type agentListMsg struct{ agents []client.Agent }

func (a *App) themesOverlay() {
	themes := theme.Names()
	items := make([]overlayItem, 0, len(themes))
	for _, name := range themes {
		name := name
		items = append(items, overlayItem{
			Label: name,
			Value: name,
			Action: func() tea.Msg {
				return statusMsg{text: "theme: " + name}
			},
		})
	}
	a.openList("Themes", items)
	o := a.overlay
	o.SetCurrent(a.theme.Name)
	initial := a.theme
	o.SetOnMove(func(item overlayItem) {
		a.setTheme(themeResolve(item.Value)) // live preview like DialogThemeList
		a.invalidateRenderCache()
	})
	// dialog-theme-list.tsx's onCleanup: escaping without confirming puts
	// the pre-dialog theme back, undoing whatever the live preview above
	// applied (and persisted) while browsing.
	o.SetOnCancel(func() tea.Msg {
		a.setTheme(initial)
		a.invalidateRenderCache()
		return nil
	})
}

// variantsOverlay ports DialogVariant (component/dialog-variant.tsx): a flat
// list of "Default" plus each variant, the current selection marked with the
// ● bullet via overlay.current. The caller has already checked that the
// model has variants — upstream's variant.list command toasts and stays put
// when there are none.
func (a *App) variantsOverlay() {
	items := []overlayItem{{
		Label: "Default",
		Value: "default",
		Action: func() tea.Msg {
			return a.setVariant("")
		},
	}}
	for _, variant := range a.variantList() {
		variant := variant
		items = append(items, overlayItem{
			Label: variant,
			Value: variant,
			Action: func() tea.Msg {
				return a.setVariant(variant)
			},
		})
	}
	a.openList("Select variant", items)
	// flat={true} upstream: no category headers, one straight list — which is
	// what a single-category openList already renders. current is the *raw*
	// stored selection (variant.selected(), "default" included), and the
	// Default row carries "default" as its value — so the bullet lands on
	// Default exactly when an explicit cycle-off stored it, and no row is
	// marked before the model ever had a selection. DialogSelect also moves
	// the *selection* onto the current row (its createEffect over
	// props.current), so the cursor and the bullet start together.
	if ref, ok := a.variantRef(); ok {
		a.overlay.SetCurrent(a.models.selectedVariant(ref))
		a.overlay.SelectValue(a.overlay.Current())
	}
}

// commandsRegistry lists the palette commands, mirroring the TS palette
// entry for entry (command-palette.tsx over the command tables in app.tsx,
// routes/session/index.tsx and component/prompt/index.tsx): label is the
// row title the original shows, value the dotted command name (upstream
// command.name), category the group header, and footer the keybind
// annotation the original formats from config/keybind.ts — "<leader>x"
// renders as "ctrl+x x", multiple bindings join with ", ", and a "none"
// keybind renders no footer at all. hint is upstream's desc, which none of
// these commands set, so rows read "title … keybind" like the original.
//
// suggested and hidden mirror command-palette.tsx's flags: hidden entries
// never show in the palette or the "/" popup (its isVisiblePaletteCommand)
// but still resolve as slash commands, and suggested ones are repeated
// under a "Suggested" header while the palette filter is empty.
//
// gocode-only entries — memory.list, stats.view, session.delete,
// getting_started.dismiss — keep the same shape. Upstream reaches delete
// only from the session list and dismisses the card with its "✕"; the
// other two have no upstream counterpart.
func (a *App) commandsRegistry() []overlayItem {
	c := a.client
	// Dynamic titles, verbatim from index.tsx: each names the action the
	// command is about to *do*, not the state it is about to enter.
	sidebarTitle := "Show sidebar"
	if a.sidebar {
		sidebarTitle = "Hide sidebar"
	}
	timestampsTitle := "Show timestamps"
	if a.timestamps {
		timestampsTitle = "Hide timestamps"
	}
	items := []overlayItem{
		{Label: "Switch session", Value: "session.list", Slash: "sessions", SlashAliases: []string{"resume", "continue"}, Category: "Session", Footer: "ctrl+x l",
			Suggested: len(a.sessions) > 0, Action: func() tea.Msg {
				a.sessionsOverlay()
				return nil
			}},
		{Label: "New session", Value: "session.new", Slash: "new", SlashAliases: []string{"clear"}, Category: "Session", Footer: "ctrl+x n",
			Suggested: a.view == viewChat, Action: func() tea.Msg {
				// The same call the ctrl+x n keybind makes. This used to return
				// reloadMsg, which only reloads the *open* session's messages and
				// is a no-op on the home screen — so the command did nothing.
				return a.newSession()
			}},
		// Hidden exactly like upstream's prompt/index.tsx session.interrupt
		// (hidden: true): esc is the affordance, the palette never lists it,
		// and "/interrupt" still resolves.
		{Label: "Interrupt session", Value: "session.interrupt", Slash: "interrupt", Category: "Session", Footer: "esc", Hidden: true, Action: func() tea.Msg {
			// Say why nothing happened. Every other command reports when it
			// cannot act; this one returned silently, which from a command
			// palette or a "/" prompt is indistinguishable from being broken.
			if a.active == nil {
				return statusMsg{text: "open a session first"}
			}
			if !a.busy {
				return statusMsg{text: "nothing is running"}
			}
			_ = c.Interrupt(a.ctx, a.active.ID)
			a.busy = false
			return statusMsg{text: "interrupted"}
		}},
		{Label: "Rename session", Value: "session.rename", Slash: "rename", Category: "Session", Footer: "ctrl+r", Action: func() tea.Msg {
			if a.active == nil {
				return statusMsg{text: "open a session first"}
			}
			a.renameSessionAction(overlayItem{Value: a.active.ID, Label: a.sessionTitle()})
			return nil
		}},
		{Label: "Delete session", Value: "session.delete", Slash: "delete", Category: "Session", Footer: "ctrl+d", Action: func() tea.Msg {
			if a.active == nil {
				return statusMsg{text: "open a session first"}
			}
			// The original only exposes session.delete as an action inside
			// the session list, where an armed second press confirms it.
			// Reaching it from the palette has no list row to arm, so it
			// confirms through DialogConfirm instead of deleting outright.
			id := a.active.ID
			title := a.sessionTitle()
			a.openConfirm("Delete Session",
				fmt.Sprintf("Are you sure you want to delete %q?", title), "",
				func() tea.Msg {
					a.active = nil
					a.view = viewHome
					a.timeline = nil
					go func() { _ = c.Delete(context.Background(), id) }()
					return reloadMsg{}
				}, nil)
			return nil
		}},
		{Label: "Compact session", Value: "session.compact", Slash: "compact", SlashAliases: []string{"summarize"}, Category: "Session", Footer: "ctrl+x c", Action: func() tea.Msg {
			// Was a placeholder message even though the server endpoint and
			// the ctrl+x c binding both exist.
			return a.compactNow()
		}},
		{Label: "Jump to message", Value: "session.timeline", Slash: "timeline", Category: "Session", Footer: "ctrl+x g", Action: func() tea.Msg {
			a.openList("Timeline", a.timelineOverlayItems())
			a.overlay.SetSize(dialogLarge)
			return nil
		}},
		{Label: sidebarTitle, Value: "session.sidebar.toggle", Category: "Session", Footer: "ctrl+x b", Action: func() tea.Msg {
			a.sidebar = !a.sidebar
			return nil
		}},
		{Label: timestampsTitle, Value: "session.toggle.timestamps", Slash: "timestamps", SlashAliases: []string{"toggle-timestamps"}, Category: "Session", Action: func() tea.Msg {
			a.timestamps = !a.timestamps
			return nil
		}},
		{Label: thinkingToggleHint(a.thinkingMode), Value: "session.toggle.thinking", Slash: "thinking", SlashAliases: []string{"toggle-thinking"}, Category: "Session", Action: func() tea.Msg {
			a.thinkingMode = nextThinkingMode(a.thinkingMode)
			a.invalidateRenderCache()
			return nil
		}},
		// session.copy: the transcript to the clipboard.
		{Label: "Copy session transcript", Value: "session.copy", Slash: "copy", Category: "Session", Action: func() tea.Msg {
			return a.copyTranscript()
		}},
		// prompt.editor shares the ctrl+x e binding with exportToEditor —
		// the original's editor_open keybind maps to exactly this command.
		{Label: "Open editor", Value: "prompt.editor", Slash: "editor", Category: "Session", Footer: "ctrl+x e", Action: func() tea.Msg {
			return a.exportToEditor()
		}},
		{Label: "Switch model", Value: "model.list", Slash: "models", SlashAliases: []string{"mo"}, Category: "Agent", Footer: "ctrl+x m",
			Suggested: true, Action: func() tea.Msg {
				return a.modelsOverlay()
			}},
		{Label: "Switch agent", Value: "agent.list", Slash: "agents", Category: "Agent", Footer: "ctrl+x a", Action: func() tea.Msg {
			return a.agentsOverlay()
		}},
		// variant.cycle, no slash of its own upstream — ctrl+t is the whole
		// affordance, same as model.cycle_recent lives on f2.
		{Label: "Variant cycle", Value: "variant.cycle", Category: "Agent", Footer: "ctrl+t", Action: func() tea.Msg {
			return a.cycleVariant()
		}},
		// variant.list, hidden when the current model has no variants
		// (upstream: hidden: list().length === 0) — and the toast when it is
		// reached anyway (a race between open and catalog, or /variants
		// typed by hand).
		{Label: "Switch model variant", Value: "variant.list", Slash: "variants", Category: "Agent", Hidden: len(a.variantList()) == 0, Action: func() tea.Msg {
			if len(a.variantList()) == 0 {
				return a.showToastOptions(toastOptions{
					title:   "No variants available",
					message: "The current model does not support any variants.",
					variant: toastInfo,
				})
			}
			a.variantsOverlay()
			return nil
		}},
		// provider.connect, suggested while nothing is connected (upstream
		// `suggested: !connected()`; paidProviderAvailable ports has()).
		{Label: "Connect provider", Value: "provider.connect", Slash: "connect", Category: "Provider",
			Suggested: !a.paidProviderAvailable(), Action: func() tea.Msg {
				return a.providersOverlay()
			}},
		{Label: "Skills", Value: "prompt.skills", Slash: "skills", Category: "Prompt", Action: func() tea.Msg {
			return a.skillsOverlay()
		}},
		{Label: "Plugins", Value: "plugins.list", Slash: "plugins", Category: "System", Action: func() tea.Msg {
			return a.pluginsOverlay()
		}},
		{Label: "Manage memories", Value: "memory.list", Slash: "memory", Category: "System",
			Action: func() tea.Msg { return a.memoriesOverlay() },
			// "/memory <instruction>" saves without opening the dialog.
			ArgAction: func(arguments string) tea.Msg { return a.quickAddMemory(arguments) }},
		{Label: "Switch theme", Value: "theme.switch", Slash: "themes", Category: "System", Footer: "ctrl+x t", Action: func() tea.Msg {
			a.themesOverlay()
			return nil
		}},
		{Label: "Help", Value: "help.show", Slash: "help", Category: "System", Action: func() tea.Msg {
			a.openHelpDialog("Help", nil)
			return nil
		}},
		// session.background: push the running foreground subagents to the
		// background (index.tsx's "Background subagents" entry, hidden like
		// upstream — ctrl+b and the task row's hint are the affordances).
		{Label: "Background subagents", Value: "session.background", Slash: "background", Category: "Session",
			Hidden: true, Footer: "ctrl+b", Action: func() tea.Msg {
				return a.backgroundSubagents()
			}},
		// session.child.first / session.parent / session.child.next /
		// session.child.previous are the subagent navigation commands behind
		// the footer's Parent/Prev/Next and the up/left/right keys; they are
		// also reachable as slash commands.
		{Label: "Go to child session", Value: "session.child.first", Slash: "subagents", SlashAliases: []string{"children"}, Category: "Session", Footer: "ctrl+x ↓", Action: func() tea.Msg {
			return a.childrenOverlay()
		}},
		{Label: "Go to parent session", Value: "session.parent", Slash: "parent", Category: "Session", Action: func() tea.Msg {
			if cmd, handled := a.openParentSession(); handled {
				return cmd
			}
			return statusMsg{text: "the open session has no parent"}
		}},
		{Label: "Open diff viewer", Value: "diff.open", Slash: "diff", SlashAliases: []string{"dif"}, Category: "VCS", Action: func() tea.Msg {
			return a.openDiffViewer()
		}},
		{Label: "View status", Value: "opencode.status", Slash: "status", Category: "System", Footer: "ctrl+x s", Action: func() tea.Msg {
			a.openStatusDialog()
			return nil
		}},
		{Label: "Usage statistics", Value: "stats.view", Slash: "stats", Category: "System", Action: func() tea.Msg {
			return a.openStatsOverlay()
		}},
		{Label: "Exit the app", Value: "app.exit", Slash: "exit", SlashAliases: []string{"quit", "q"}, Category: "System", Footer: "ctrl+c, ctrl+d, ctrl+x q", Action: func() tea.Msg { return quitMsg{} }},
	}
	// The sidebar footer's getting-started card is dismissed by clicking its
	// "✕" upstream. This port has no per-widget mouse targets inside the
	// sidebar panel (see link.go on why clickable regions have to be recorded
	// in absolute screen cells, which the panel does not know), so the
	// dismissal is exposed as a palette command instead — offered only while
	// the card is actually showing, like the "✕" itself.
	if !a.paidProviderAvailable() && !a.dismissedGettingStarted {
		items = append(items, overlayItem{
			Label: "Dismiss getting started", Value: "getting_started.dismiss", Category: "System",
			Action: func() tea.Msg {
				a.dismissedGettingStarted = true
				return nil
			},
		})
	}
	return items
}

// commandPalette opens the ctrl+p dialog.
//
// command-palette.tsx lists every reachable palette command that is not
// hidden and — only while the filter is empty — repeats the suggested ones
// first under their own "Suggested" header, with a "suggested:"-prefixed
// value so the rows stay distinct while selecting either dispatches the
// same command.
func (a *App) commandPalette() {
	all := a.commandsRegistry()
	items := make([]overlayItem, 0, len(all))
	for _, item := range all {
		if item.Hidden {
			continue
		}
		if item.Suggested {
			first := item
			first.Value = "suggested:" + first.Value
			first.Category = "Suggested"
			items = append(items, first)
		}
		items = append(items, item)
	}
	a.openList("Commands", items)
}

// fileMentions lists workspace files for @ completion.
func fileMentions(query string) []overlayItem {
	root, err := os.Getwd()
	if err != nil {
		return nil
	}
	var items []overlayItem
	count := 0
	filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= 400 {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		count++
		if query != "" && !strings.Contains(strings.ToLower(rel), strings.ToLower(query)) {
			return nil
		}
		items = append(items, overlayItem{Label: rel, Value: rel})
		if len(items) >= 20 {
			return filepath.SkipAll
		}
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items
}

var _ = client.Model{}
