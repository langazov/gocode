package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// The /memory manager.
//
// It follows the sessions dialog rather than the skills dialog, because unlike
// skills these rows are editable: a list plus footer actions that operate on
// the selected row, with delete behind the same arm-then-confirm the session
// delete uses (press once to arm, again to commit). Memories are permanent by
// design, so an accidental keystroke should not be able to end one.

// memoriesOverlay opens the manager from the cached list and refreshes in the
// background, so it never sits on a stale "Loading" once the request lands.
func (a *App) memoriesOverlay() tea.Cmd {
	a.openMemoryDialog(a.memoryList)
	return a.loadMemoryListCmd()
}

func (a *App) openMemoryDialog(memories []client.Memory) {
	a.openList("Memories", a.memoryItems(memories))
	o := a.overlay
	o.size = dialogLarge
	o.placeholder = "Search memories..."
	o.actions = []dialogAction{
		// standalone: adding must work from the empty list, which is where a
		// user most needs it. ctrl+a rather than ctrl+n, which the list
		// dialog already binds to "move down".
		{title: "new", keys: "ctrl+a", standalone: true, onTrigger: a.newMemoryAction},
		{title: "edit", keys: "ctrl+r", onTrigger: a.editMemoryAction},
		{title: "delete", keys: "ctrl+d", onTrigger: a.deleteMemoryAction},
		{title: "scope", keys: "ctrl+g", onTrigger: a.toggleMemoryScopeAction},
		{title: "mute", keys: "ctrl+t", onTrigger: a.toggleMemoryMutedAction},
	}
	if len(memories) == 0 {
		switch {
		case a.memoryListErr != "":
			o.emptyTitle = "Could not load memories"
			o.emptyBody = a.memoryListErr
			o.locked = true
			o.hideFilter = true
		case !a.memoryListLoaded:
			o.emptyTitle = "Loading memories"
			o.emptyBody = "Fetching saved memories..."
		default:
			o.emptyTitle = "No memories yet"
			o.emptyBody = "Press ctrl+a to add one, or type /memory <instruction> in the prompt."
		}
	}
}

// memoryItems maps memories onto rows, grouped by scope so the ones that
// follow the user everywhere are visibly separate from this project's.
//
// The order matches the store's: pinned first, then most recently updated —
// the same precedence the render budget applies, so what sits at the top here
// is what survives into the prompt.
func (a *App) memoryItems(memories []client.Memory) []overlayItem {
	sorted := append([]client.Memory(nil), memories...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Pinned != sorted[j].Pinned {
			return sorted[i].Pinned
		}
		return false
	})

	items := make([]overlayItem, 0, len(sorted))
	for _, item := range sorted {
		item := item
		category := "Project"
		if item.Scope == scopeGlobal {
			category = "Global"
		}
		items = append(items, overlayItem{
			label:    strings.Join(strings.Fields(item.Content), " "),
			hint:     memoryHint(item),
			value:    item.ID,
			category: category,
			gutter:   memoryGutter(item),
		})
	}
	return items
}

// scopeGlobal mirrors memory.ScopeGlobal. It is duplicated rather than
// imported because the interface is an HTTP client and must not depend on the
// server's packages — the wire format is the contract between them.
const scopeGlobal = "global"

func memoryHint(item client.Memory) string {
	var parts []string
	if item.Disabled {
		parts = append(parts, "muted")
	}
	if item.Category != "" {
		parts = append(parts, item.Category)
	}
	if item.Origin != "" {
		parts = append(parts, "by "+item.Origin)
	}
	return strings.Join(parts, " · ")
}

func memoryGutter(item client.Memory) string {
	if item.Pinned {
		return "*"
	}
	return ""
}

func (a *App) memoryByID(id string) (client.Memory, bool) {
	for _, item := range a.memoryList {
		if item.ID == id {
			return item, true
		}
	}
	return client.Memory{}, false
}

// newMemoryAction prompts for a new memory and saves it to the project scope.
// It ignores the selected row — there may not be one — and is why dialogAction
// grew a standalone flag.
//
// New memories land in the project scope; ctrl+g on the resulting row widens
// one to every project. Making the narrower choice the default, and the wider
// one a deliberate second step, is the same trade the tool and the HTTP routes
// make.
func (a *App) newMemoryAction(overlayItem) tea.Cmd {
	// openInput closes the manager before running this callback, so the
	// refresh has to put it back — otherwise saving a memory makes the list
	// the user was working in disappear.
	a.openInput("New Memory", "", func(value string) tea.Msg {
		saved, err := a.client.CreateMemory(a.ctx, value, "project")
		if err != nil {
			return statusMsg{text: "could not save memory: " + err.Error()}
		}
		return a.memoryRefresh("remembered for "+scopeLabel(saved.Scope), true)
	})
	return nil
}

func (a *App) editMemoryAction(item overlayItem) tea.Cmd {
	existing, ok := a.memoryByID(item.value)
	if !ok {
		return nil
	}
	id := existing.ID
	// As with a new memory, the input dialog has replaced the manager by the
	// time this runs, so the refresh reopens it.
	a.openInput("Edit Memory", existing.Content, func(value string) tea.Msg {
		if _, err := a.client.UpdateMemory(a.ctx, id, client.MemoryPatch{Content: &value}); err != nil {
			return statusMsg{text: "edit failed: " + err.Error()}
		}
		return a.memoryRefresh("memory updated", true)
	})
	return nil
}

// deleteMemoryAction arms on the first press and commits on the second, the
// same guard the session delete uses. A memory is permanent, so the second
// press is the point.
func (a *App) deleteMemoryAction(item overlayItem) tea.Cmd {
	o := a.overlay
	if o == nil {
		return nil
	}
	if o.armValue != item.value {
		o.armValue = item.value
		o.armKeys = "ctrl+d"
		return nil
	}
	o.armValue = ""
	id := item.value
	return func() tea.Msg {
		if err := a.client.DeleteMemory(a.ctx, id); err != nil {
			return statusMsg{text: "delete failed: " + err.Error()}
		}
		return a.memoryRefreshMsg("memory deleted")
	}
}

func (a *App) toggleMemoryScopeAction(item overlayItem) tea.Cmd {
	existing, ok := a.memoryByID(item.value)
	if !ok {
		return nil
	}
	id := existing.ID
	scope := scopeGlobal
	if existing.Scope == scopeGlobal {
		scope = "project"
	}
	return func() tea.Msg {
		if _, err := a.client.UpdateMemory(a.ctx, id, client.MemoryPatch{Scope: &scope}); err != nil {
			return statusMsg{text: "scope change failed: " + err.Error()}
		}
		return a.memoryRefreshMsg("memory now applies to " + scopeLabel(scope))
	}
}

// toggleMemoryMutedAction silences a memory without losing what it said —
// the reason `disabled` exists as a column rather than the user having to
// delete and retype.
func (a *App) toggleMemoryMutedAction(item overlayItem) tea.Cmd {
	existing, ok := a.memoryByID(item.value)
	if !ok {
		return nil
	}
	id := existing.ID
	disabled := !existing.Disabled
	return func() tea.Msg {
		if _, err := a.client.UpdateMemory(a.ctx, id, client.MemoryPatch{Disabled: &disabled}); err != nil {
			return statusMsg{text: "mute failed: " + err.Error()}
		}
		if disabled {
			return a.memoryRefreshMsg("memory muted")
		}
		return a.memoryRefreshMsg("memory unmuted")
	}
}

func scopeLabel(scope string) string {
	if scope == scopeGlobal {
		return "every project"
	}
	return "this project"
}

// quickAddMemory handles "/memory <instruction>": save it and say so, without
// opening the dialog. The common case is recording something the user just
// said, and making that cost a dialog would be the wrong trade.
func (a *App) quickAddMemory(content string) tea.Cmd {
	content = strings.TrimSpace(content)
	if content == "" {
		return a.memoriesOverlay()
	}
	return func() tea.Msg {
		saved, err := a.client.CreateMemory(a.ctx, content, "project")
		if err != nil {
			return statusMsg{text: "could not save memory: " + err.Error()}
		}
		return a.memoryRefreshMsg("remembered for " + scopeLabel(saved.Scope))
	}
}

// memoryRefreshMsg re-reads the list after a mutation and carries a status
// line. Refetching rather than patching the cache locally keeps the dialog
// honest about what the server actually stored — a create can collide with an
// existing memory and come back as an update to it.
func (a *App) memoryRefreshMsg(status string) tea.Msg {
	return a.memoryRefresh(status, false)
}

// memoryRefresh is memoryRefreshMsg with control over whether the manager is
// reopened. reopen is for the mutations that ran through an input dialog,
// which closes the manager on submit; the in-place actions (delete, scope,
// mute) leave it open and must not have it rebuilt from under them.
func (a *App) memoryRefresh(status string, reopen bool) tea.Msg {
	memories, err := a.client.Memories(a.ctx)
	if err != nil {
		return memoryListMsg{err: err, status: status, reopen: reopen}
	}
	return memoryListMsg{memories: memories, status: status, reopen: reopen}
}

func (a *App) loadMemoryListCmd() tea.Cmd {
	c := a.client
	return func() tea.Msg {
		memories, err := c.Memories(a.ctx)
		if err != nil {
			return memoryListMsg{err: err}
		}
		return memoryListMsg{memories: memories}
	}
}

// memoryListMsg carries a refreshed list, or the error that stopped one
// arriving — reported rather than dropped, matching the skills dialog's own
// error state. status, when set, is shown in the footer.
type memoryListMsg struct {
	memories []client.Memory
	err      error
	status   string
	// reopen asks for the manager to be shown again, for a mutation that ran
	// through an input dialog and therefore closed it.
	reopen bool
}
