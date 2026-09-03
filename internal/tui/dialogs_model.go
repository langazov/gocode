package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/tui/client"
)

// This file ports packages/tui/src/component/dialog-model.tsx and
// dialog-provider.tsx. The model dialog was previously a flat list of every
// model with the provider as its only grouping; the original has Favorites and
// Recent sections, per-provider grouping, cost-aware ordering, and two footer
// actions — one of which opens the provider dialog, the way a new provider is
// connected from inside the interface.

// providerPriority ports PROVIDER_PRIORITY in dialog-provider.tsx: the
// providers listed first, in this order, under a "Popular" heading.
var providerPriority = map[string]int{
	"opencode":       0,
	"opencode-go":    1,
	"openai":         2,
	"github-copilot": 3,
	"anthropic":      4,
	"google":         5,
}

// providerBlurb ports the description map in providerOptions().
var providerBlurb = map[string]string{
	"opencode":    "(Recommended)",
	"anthropic":   "(API key)",
	"openai":      "(ChatGPT Plus/Pro or API key)",
	"opencode-go": "Low cost subscription for everyone",
}

// customProviderValue marks the "Other" row, ported from
// CUSTOM_PROVIDER_OPTION_VALUE.
const customProviderValue = "__gocode_custom_provider__"

// connected ports useConnected(): the user has a provider beyond the free
// opencode tier — either a different provider entirely, or a paid opencode
// model. It decides whether the dialog shows sections and favorites, or
// steers a new user toward connecting something.
func (a *App) connected(models []client.Model) bool {
	for _, model := range models {
		if model.ProviderID != "opencode" || model.CostInput != 0 {
			return true
		}
	}
	return false
}

// modelsOverlay builds the model dialog.
//
// It opens synchronously from the cached catalog and refreshes in the
// background. Fetching first and opening after meant the panel did not appear
// until a full HTTP round trip completed — measured at 140ms+ once a provider
// with a published model list was configured, which reads as the dialog
// lagging the keypress. TS has no equivalent wait because DialogModel renders
// from `sync.data`, state that is already in the client.
func (a *App) modelsOverlay() tea.Cmd {
	a.openModelDialog(a.catalogModels)
	return a.loadCatalogCmd()
}

func (a *App) openModelDialog(models []client.Model) {
	a.openList("Select model", a.modelItems(models, ""))
	o := a.overlay
	o.size = dialogLarge
	o.current = a.currentModelLabel()
	o.actions = a.modelDialogActions(models)
	if len(models) == 0 {
		// Three different reasons produce an empty list, and they need three
		// different messages. Reporting all of them as "Loading" is what made
		// a fresh install look like a hung download: the catalog had in fact
		// arrived, it was just empty because no provider is connected.
		switch {
		case a.catalogErr != "":
			o.emptyTitle = "Could not load models"
			o.emptyBody = a.catalogErr
		case !a.catalogLoaded:
			o.emptyTitle = "Loading models"
			o.emptyBody = "Fetching the model catalog..."
		default:
			o.emptyTitle = "No models available"
			o.emptyBody = "No provider is connected yet. Press ctrl+p to connect one."
		}
	}
}

// refreshOpenCatalogDialog re-renders whichever catalog-backed dialog is open
// when a fresh catalog arrives, preserving the filter and selected row.
func (a *App) refreshOpenCatalogDialog() {
	o := a.overlay
	if o == nil || o.kind != overlayList {
		return
	}
	switch o.title {
	case "Select model":
		filter, selected := o.filter, a.selectedOverlayValue()
		a.openModelDialog(a.catalogModels)
		a.restoreOverlaySelection(filter, selected)
	case "Connect a provider":
		filter, selected := o.filter, a.selectedOverlayValue()
		a.openProviderDialog()
		a.restoreOverlaySelection(filter, selected)
	}
}

func (a *App) selectedOverlayValue() string {
	o := a.overlay
	if o == nil || o.selected < 0 || o.selected >= len(o.items) {
		return ""
	}
	return o.items[o.selected].value
}

// restoreOverlaySelection reapplies a filter and moves the cursor back to the
// row it was on, so a background refresh does not move the selection.
func (a *App) restoreOverlaySelection(filter, value string) {
	o := a.overlay
	if o == nil {
		return
	}
	o.filter = filter
	o.applyFilter()
	for i, item := range o.items {
		if item.value == value {
			o.selected = i
			return
		}
	}
}

// modelDialogActions ports the `actions` array on dialog-model.tsx's
// DialogSelect: connect a provider, and toggle a favorite.
func (a *App) modelDialogActions(models []client.Model) []dialogAction {
	isConnected := a.connected(models)
	title := "View all providers"
	if isConnected {
		title = "Connect provider"
	}
	actions := []dialogAction{{
		title: title,
		keys:  "ctrl+p",
		onTrigger: func(overlayItem) tea.Cmd {
			return a.providersOverlay()
		},
	}}
	// TS hides the favorite action until the user is connected, since the
	// free tier has nothing worth pinning.
	if isConnected {
		actions = append(actions, dialogAction{
			title: "Favorite",
			keys:  "ctrl+f",
			onTrigger: func(item overlayItem) tea.Cmd {
				ref, ok := parseModelLabel(item.value)
				if !ok {
					return nil
				}
				added := a.models.toggleFavorite(ref)
				// Rebuild in place so the Favorites section updates under the
				// cursor without closing the dialog.
				a.refreshModelItems(models)
				verb := "removed from"
				if added {
					verb = "added to"
				}
				return staticMsg(statusMsg{text: item.label + " " + verb + " favorites"})
			},
		})
	}
	return actions
}

// refreshModelItems rebuilds the open model dialog's rows, preserving the
// filter and the selected row's identity.
func (a *App) refreshModelItems(models []client.Model) {
	o := a.overlay
	if o == nil || o.kind != overlayList {
		return
	}
	var selectedValue string
	if o.selected >= 0 && o.selected < len(o.items) {
		selectedValue = o.items[o.selected].value
	}
	o.all = a.modelItems(models, o.filter)
	o.applyFilter()
	for i, item := range o.items {
		if item.value == selectedValue {
			o.selected = i
			break
		}
	}
}

// modelItems assembles the rows, porting the options() memo.
//
// The section headings only appear on an unfiltered list: once the user is
// searching, TS collapses to a single ranked list so a match is not buried
// under a heading it does not belong to.
func (a *App) modelItems(models []client.Model, filter string) []overlayItem {
	isConnected := a.connected(models)
	showSections := isConnected && strings.TrimSpace(filter) == ""

	// DialogSelect drops options flagged `disabled` from the list outright
	// (dialog-select.tsx:155,159), so the models dialog-model.tsx marks
	// disabled are absent from the original's list rather than shown greyed
	// out. opencode's nano models are utility models for title generation.
	visible := make([]client.Model, 0, len(models))
	for _, model := range models {
		if model.ProviderID == "opencode" && strings.Contains(model.ID, "-nano") {
			continue
		}
		visible = append(visible, model)
	}
	models = visible

	byLabel := make(map[string]client.Model, len(models))
	for _, model := range models {
		byLabel[model.ProviderID+"/"+model.ID] = model
	}

	var items []overlayItem
	seen := map[string]bool{}

	if showSections {
		favorites := a.models.favorites()
		for _, ref := range favorites {
			if model, ok := byLabel[ref.label()]; ok {
				items = append(items, a.modelRow(model, "Favorites", false))
				seen[ref.label()] = true
			}
		}
		for _, ref := range a.models.recents() {
			if seen[ref.label()] {
				continue
			}
			if model, ok := byLabel[ref.label()]; ok {
				items = append(items, a.modelRow(model, "Recent", false))
				seen[ref.label()] = true
			}
		}
	}

	// Group by provider, opencode first then alphabetically, matching
	// sortBy(provider.id !== "opencode", provider.name).
	grouped := map[string][]client.Model{}
	for _, model := range models {
		if seen[model.ProviderID+"/"+model.ID] {
			continue
		}
		grouped[model.ProviderID] = append(grouped[model.ProviderID], model)
	}
	providerIDs := make([]string, 0, len(grouped))
	for id := range grouped {
		providerIDs = append(providerIDs, id)
	}
	sort.Slice(providerIDs, func(i, j int) bool {
		a1, b1 := providerIDs[i] != "opencode", providerIDs[j] != "opencode"
		if a1 != b1 {
			return !a1
		}
		return providerIDs[i] < providerIDs[j]
	})

	for _, providerID := range providerIDs {
		group := grouped[providerID]
		sortModelOptions(group)
		category := ""
		if isConnected {
			category = a.providerName(providerID)
		}
		for _, model := range group {
			items = append(items, a.modelRow(model, category, a.models.isFavorite(modelRef{model.ProviderID, model.ID})))
		}
	}
	return items
}

// modelRow builds one option, porting the object literal in providerOptions.
func (a *App) modelRow(model client.Model, category string, favorite bool) overlayItem {
	label := model.ProviderID + "/" + model.ID
	title := model.Name
	if title == "" {
		title = label
	}
	hint := ""
	if favorite {
		hint = "(Favorite)"
	}
	footer := ""
	if model.ProviderID == "opencode" && model.CostInput == 0 {
		footer = "Free"
	}
	return overlayItem{
		label:    title,
		hint:     hint,
		value:    label,
		category: category,
		footer:   footer,
		action: func() tea.Msg {
			if a.active == nil {
				return statusMsg{text: "open a session first"}
			}
			if err := a.client.SetModel(a.ctx, a.active.ID, model.ProviderID, model.ID); err != nil {
				return statusMsg{text: "model switch failed: " + err.Error()}
			}
			a.activeModel = label
			a.models.markRecent(modelRef{ProviderID: model.ProviderID, ModelID: model.ID})
			return statusMsg{text: "model: " + label}
		},
	}
}

// sortModelOptions ports sortModelOptions(options, newestFirst=false): free
// models first, then newest by release date, then by title.
//
// The Go model wire type carries no release date, so the middle key is absent
// and ordering falls back to title within a cost tier. Adding release_date to
// the /api/model response would close that gap.
func sortModelOptions(models []client.Model) {
	sort.SliceStable(models, func(i, j int) bool {
		freeI := models[i].ProviderID == "opencode" && models[i].CostInput == 0
		freeJ := models[j].ProviderID == "opencode" && models[j].CostInput == 0
		if freeI != freeJ {
			return freeI
		}
		return modelTitle(models[i]) < modelTitle(models[j])
	})
}

func modelTitle(model client.Model) string {
	if model.Name != "" {
		return model.Name
	}
	return model.ID
}

func parseModelLabel(label string) (modelRef, bool) {
	providerID, modelID, ok := strings.Cut(label, "/")
	if !ok || providerID == "" || modelID == "" {
		return modelRef{}, false
	}
	return modelRef{ProviderID: providerID, ModelID: modelID}, true
}
