package tui

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// --- cycle ------------------------------------------------------------------

// variantFixture builds an app whose catalog has arrived (claude-sonnet-4-5
// with high/max, opus with none) and whose session pins sonnet.
func variantFixture(t *testing.T) (*App, *mockAPI, *httptest.Server) {
	t.Helper()
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	driveCmd(t, app, app.loadCatalogCmd())
	openSession(t, app)
	// ses_1 carries no pinned model; pin sonnet so currentModelParts and the
	// variant helpers resolve a real model — the state a live session that
	// picked a model (or was created for one) is in.
	app.active.Model = &client.ModelRef{ProviderID: "anthropic", ID: "claude-sonnet-4-5"}
	return app, api, server
}

// TestVariantCycleWalksTheCatalogOrder ports variant.cycle's walk: none ->
// first -> ... -> last -> none, using the catalog's own order (high before
// max, not map order).
func TestVariantCycleWalksTheCatalogOrder(t *testing.T) {
	app, _, _ := variantFixture(t)

	// none -> high
	drive(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if got := app.variantCurrent(); got != "high" {
		t.Fatalf("first ctrl+t = %q, want high", got)
	}
	// high -> max
	drive(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if got := app.variantCurrent(); got != "max" {
		t.Fatalf("second ctrl+t = %q, want max", got)
	}
	// max -> none (cycle-off stores "default")
	drive(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if got := app.variantCurrent(); got != "" {
		t.Fatalf("third ctrl+t = %q, want none", got)
	}
}

// TestVariantCycleNoOpWithoutVariants: a model with no variants does
// nothing on ctrl+t — upstream's `if (variants.length === 0) return`.
func TestVariantCycleNoOpWithoutVariants(t *testing.T) {
	app, _, _ := variantFixture(t)
	// Switch the pinned model to the variant-less opus.
	app.active.Model = &client.ModelRef{ProviderID: "anthropic", ID: "claude-opus-4-5"}

	drive(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if got := app.variantCurrent(); got != "" {
		t.Fatalf("ctrl+t on a model without variants selected %q, want none", got)
	}
}

// TestVariantCyclePinsTheSession: cycling pushes the selection onto the
// open session's pinned model, so the next turn runs with it — the server
// reads the variant from the session row, which is this port's equivalent
// of sending variant with each prompt.
func TestVariantCyclePinsTheSession(t *testing.T) {
	app, api, _ := variantFixture(t)
	drive(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})

	if app.active.Model == nil || app.active.Model.Variant != "high" {
		t.Fatalf("session model = %+v, want variant high", app.active.Model)
	}
	// The pin is async; poll for the API to see it.
	waitForVariantPin(t, api, "high")
	if got, ok := api.lastModel(); !ok || got.model != "claude-sonnet-4-5" || got.variant != "high" {
		t.Fatalf("SetModel call = %+v, want sonnet/high", got)
	}
}

// --- the dialog -------------------------------------------------------------

// TestVariantsDialogListsDefaultAndVariants ports DialogVariant: "Default"
// first, then the variants, the ● on the raw stored selection.
func TestVariantsDialogListsDefaultAndVariants(t *testing.T) {
	app, _, _ := variantFixture(t)
	driveCmd(t, app, runItemAction(variantEntry(t, app, "variants")))

	if app.overlay == nil || app.overlay.title != "Select variant" {
		t.Fatalf("expected the Select variant dialog, got %+v", app.overlay)
	}
	var labels []string
	for _, item := range app.overlay.items {
		labels = append(labels, item.label)
	}
	if strings.Join(labels, ",") != "Default,high,max" {
		t.Fatalf("dialog rows = %v, want [Default high max]", labels)
	}
	if app.overlay.current != "" {
		t.Fatalf("no selection yet, but current = %q", app.overlay.current)
	}

	// Pick max, reopen: the ● sits on max.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyDown})
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := app.variantCurrent(); got != "max" {
		t.Fatalf("after picking the third row, variant = %q, want max", got)
	}
	driveCmd(t, app, runItemAction(variantEntry(t, app, "variants")))
	if app.overlay.current != "max" {
		t.Fatalf("dialog current = %q, want max", app.overlay.current)
	}

	// Pick Default: the selection clears and the ● sits on Default.
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyUp})
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyUp})
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := app.variantCurrent(); got != "" {
		t.Fatalf("after picking Default, variant = %q, want none", got)
	}
	driveCmd(t, app, runItemAction(variantEntry(t, app, "variants")))
	if app.overlay.current != "default" {
		t.Fatalf("dialog current = %q, want default", app.overlay.current)
	}
}

// TestVariantListCommandHiddenWithoutVariants: upstream hides variant.list
// when the current model has none, so the palette (and /variants, which
// still resolves) never offers a picker with nothing in it.
func TestVariantListCommandHiddenWithoutVariants(t *testing.T) {
	app, _, _ := variantFixture(t)
	// Sonnet has variants: listed.
	if entry, ok := variantEntryOk(app, "variants"); !ok || entry.hidden {
		t.Fatal("variant.list must list (not hidden) for a model with variants")
	}
	// Switch to opus: hidden.
	app.active.Model = &client.ModelRef{ProviderID: "anthropic", ID: "claude-opus-4-5"}

	if entry, ok := variantEntryOk(app, "variants"); !ok || !entry.hidden {
		t.Fatal("variant.list must be hidden for a model without variants")
	}
	// The toast when /variants is dispatched anyway.
	cmd := app.runSlashCommand("variants")
	if cmd == nil {
		t.Fatal("/variants on a variant-less model should toast")
	}
}

// --- the meta row -----------------------------------------------------------

// TestVariantMetaRow: the prompt's meta row shows "· variant" in the
// warning color while a variant is in effect, and drops it on Default.
func TestVariantMetaRow(t *testing.T) {
	app, _, _ := variantFixture(t)
	app.width, app.height = 120, 40

	if meta := ansi.Strip(app.modelMeta()); strings.Contains(meta, "high") {
		t.Fatalf("meta row before any selection mentions a variant: %q", meta)
	}
	drive(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	meta := ansi.Strip(app.modelMeta())
	if !strings.Contains(meta, "· high") {
		t.Fatalf("meta row = %q, want it to end with the variant segment", meta)
	}
	// Bold warning color, like prompt/index.tsx's variant span: SGR 1 is
	// emitted with the fg color in one sequence ("\x1b[1;38;2;...").
	styled := app.modelMeta()
	if !strings.Contains(styled, "\x1b[1;") || !strings.Contains(styled, "high") {
		t.Fatalf("variant segment not bold: %q", styled)
	}
}

// --- persistence -------------------------------------------------------------

// TestVariantPersistsAcrossRestart: the selection survives a new App over
// the same state directory — model.json is the shared store, exactly like
// the TS client's.
func TestVariantPersistsAcrossRestart(t *testing.T) {
	app, api, _ := variantFixture(t)
	drive(t, app, tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	waitForVariantPin(t, api, "high")

	// The file itself carries the TS shape: "provider/model" -> selection.
	raw, err := os.ReadFile(app.models.path)
	if err != nil {
		t.Fatalf("model.json unreadable: %v", err)
	}
	var parsed struct {
		Variant map[string]string `json:"variant"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("model.json unparseable: %v", err)
	}
	if parsed.Variant["anthropic/claude-sonnet-4-5"] != "high" {
		t.Fatalf("model.json variant map = %v", parsed.Variant)
	}

	// A fresh store over the same file reads the selection back, which is
	// what a restarted TUI resolves variant.current() through — the same
	// construction newModelStore performs against the same state dir.
	fresh := newModelStore()
	fresh.path = app.models.path
	if got := fresh.selectedVariant(modelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4-5"}); got != "high" {
		t.Fatalf("variant after restart = %q, want high (persisted)", got)
	}
}

// --- the model-dialog hand-off ----------------------------------------------

// TestModelDialogHandsOffToVariantPicker: picking a model with variants and
// no valid stored selection opens the variant picker instead of closing,
// exactly like dialog-model.tsx's onSelect.
func TestModelDialogHandsOffToVariantPicker(t *testing.T) {
	app, _, _ := variantFixture(t)
	// Make the stored selection invalid for sonnet (as if the last model
	// was another one).
	app.models.setVariant(modelRef{ProviderID: "anthropic", ModelID: "claude-sonnet-4-5"}, "bogus")

	driveCmd(t, app, app.modelsOverlay())
	o := app.overlay
	for i, item := range o.items {
		if item.value == "anthropic/claude-sonnet-4-5" {
			o.selected = i
			break
		}
	}
	drive(t, app, tea.KeyPressMsg{Code: tea.KeyEnter})
	if app.overlay == nil || app.overlay.title != "Select variant" {
		t.Fatalf("model pick should hand off to the variant picker, got %+v", app.overlay)
	}
}

// --- helpers -----------------------------------------------------------------

// waitForVariantPin polls the mock API until the variant pin lands (it is
// posted from a background goroutine, so the test has to yield — through
// the mutex-guarded reader, since the handler appends on its own goroutine).
func waitForVariantPin(t *testing.T, api *mockAPI, variant string) {
	t.Helper()
	for range 200 {
		if call, ok := api.lastModel(); ok && call.variant == variant {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	call, _ := api.lastModel()
	t.Fatalf("variant pin %q never reached the API; last call: %+v", variant, call)
}

func variantEntry(t *testing.T, app *App, slash string) overlayItem {
	t.Helper()
	entry, ok := variantEntryOk(app, slash)
	if !ok {
		t.Fatalf("no /%s command in the registry", slash)
	}
	return entry
}

func variantEntryOk(app *App, slash string) (overlayItem, bool) {
	for _, item := range app.commandsRegistry() {
		if item.slash == slash {
			return item, true
		}
	}
	return overlayItem{}, false
}
