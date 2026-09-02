package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anomalyco/opencode-go/internal/tui/client"
)

func testApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	return &App{
		models: newModelStore(),
		theme:  themeResolve("opencode-dark"),
		// update() drives these on catalogMsg; New() builds them, so a
		// hand-rolled App has to as well.
		agentMetaFade: newFadeAnim(false),
		modelMetaFade: newFadeAnim(false),
	}
}

func categoriesOf(items []overlayItem) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range items {
		if item.category == "" || seen[item.category] {
			continue
		}
		seen[item.category] = true
		out = append(out, item.category)
	}
	return out
}

func findItem(items []overlayItem, value string) (overlayItem, bool) {
	for _, item := range items {
		if item.value == value {
			return item, true
		}
	}
	return overlayItem{}, false
}

var testModels = []client.Model{
	{ProviderID: "opencode", ID: "free-one", Name: "Free One", CostInput: 0},
	{ProviderID: "opencode", ID: "gpt-5.4-nano", Name: "Nano", CostInput: 0},
	{ProviderID: "opencode", ID: "paid-one", Name: "Paid One", CostInput: 3},
	{ProviderID: "anthropic", ID: "claude-x", Name: "Claude X", CostInput: 3},
	{ProviderID: "anthropic", ID: "claude-a", Name: "Claude A", CostInput: 3},
}

// TestModelDialogHasProviderAction is the reported gap: the dialog had no
// footer actions at all, so there was no way to reach the connect-a-provider
// flow from the model picker.
func TestModelDialogHasProviderAction(t *testing.T) {
	app := testApp(t)
	actions := app.modelDialogActions(testModels)
	if len(actions) == 0 {
		t.Fatal("the model dialog must offer footer actions")
	}
	if actions[0].title != "Connect provider" {
		t.Errorf("first action = %q, want %q", actions[0].title, "Connect provider")
	}
	var favorite bool
	for _, action := range actions {
		favorite = favorite || action.title == "Favorite"
	}
	if !favorite {
		t.Error("a connected user must get the Favorite action")
	}
}

// TestModelDialogActionsWhenNotConnected: with only the free tier the wording
// changes and the favorite action is withheld, matching useConnected().
func TestModelDialogActionsWhenNotConnected(t *testing.T) {
	app := testApp(t)
	freeOnly := []client.Model{{ProviderID: "opencode", ID: "free", CostInput: 0}}
	actions := app.modelDialogActions(freeOnly)
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want only the providers action", len(actions))
	}
	if actions[0].title != "View all providers" {
		t.Errorf("action = %q, want %q", actions[0].title, "View all providers")
	}
}

// TestModelItemsGroupsByProviderWithOpencodeFirst covers the grouping the flat
// list never had.
func TestModelItemsGroupsByProviderWithOpencodeFirst(t *testing.T) {
	app := testApp(t)
	items := app.modelItems(testModels, "")
	categories := categoriesOf(items)
	if len(categories) < 2 {
		t.Fatalf("expected per-provider categories, got %v", categories)
	}
	if categories[0] != app.providerName("opencode") {
		t.Errorf("first category = %q, want the opencode group first", categories[0])
	}
}

// TestModelItemsMarksFreeAndHidesNano covers the Free label and the omission
// of the utility models.
func TestModelItemsMarksFreeAndHidesNano(t *testing.T) {
	app := testApp(t)
	items := app.modelItems(testModels, "")

	free, ok := findItem(items, "opencode/free-one")
	if !ok {
		t.Fatal("missing the free model")
	}
	if free.footer != "Free" {
		t.Errorf("footer = %q, want %q", free.footer, "Free")
	}

	paid, _ := findItem(items, "opencode/paid-one")
	if paid.footer != "" {
		t.Errorf("a paid model must not be labelled Free, got %q", paid.footer)
	}

	// DialogSelect filters disabled options out of the list, so the original
	// never shows opencode's utility nano models at all.
	if _, ok := findItem(items, "opencode/gpt-5.4-nano"); ok {
		t.Error("opencode's nano models are utility models and must not be listed")
	}
}

// TestModelItemsSortsFreeFirstThenTitle covers sortModelOptions.
func TestModelItemsSortsFreeFirstThenTitle(t *testing.T) {
	models := []client.Model{
		{ProviderID: "opencode", ID: "z-paid", Name: "Z Paid", CostInput: 5},
		{ProviderID: "opencode", ID: "b-free", Name: "B Free", CostInput: 0},
		{ProviderID: "opencode", ID: "a-free", Name: "A Free", CostInput: 0},
	}
	sortModelOptions(models)
	got := []string{models[0].ID, models[1].ID, models[2].ID}
	want := []string{"a-free", "b-free", "z-paid"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v (free first, then title)", got, want)
		}
	}
}

// TestFavoritesAndRecentSections is the sectioning the original has and the
// flat list did not.
func TestFavoritesAndRecentSections(t *testing.T) {
	app := testApp(t)
	app.models.toggleFavorite(modelRef{"anthropic", "claude-x"})
	app.models.markRecent(modelRef{"opencode", "paid-one"})

	items := app.modelItems(testModels, "")
	if len(items) == 0 {
		t.Fatal("no items")
	}
	if items[0].category != "Favorites" || items[0].value != "anthropic/claude-x" {
		t.Errorf("first row = %+v, want the favorite at the top under Favorites", items[0])
	}
	var recent bool
	for _, item := range items {
		if item.category == "Recent" && item.value == "opencode/paid-one" {
			recent = true
		}
	}
	if !recent {
		t.Error("expected a Recent section holding the last-used model")
	}
	// A model must not be listed twice: once promoted into a section it is
	// dropped from its provider group.
	count := 0
	for _, item := range items {
		if item.value == "anthropic/claude-x" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("favorite appears %d times, want 1", count)
	}
}

// TestSectionsHiddenWhileFiltering matches TS collapsing to one ranked list
// as soon as the user types.
func TestSectionsHiddenWhileFiltering(t *testing.T) {
	app := testApp(t)
	app.models.toggleFavorite(modelRef{"anthropic", "claude-x"})
	items := app.modelItems(testModels, "claude")
	for _, item := range items {
		if item.category == "Favorites" || item.category == "Recent" {
			t.Fatalf("sections must not appear while filtering, got %q", item.category)
		}
	}
}

func TestParseModelLabel(t *testing.T) {
	ref, ok := parseModelLabel("anthropic/claude-sonnet-4-5")
	if !ok || ref.ProviderID != "anthropic" || ref.ModelID != "claude-sonnet-4-5" {
		t.Errorf("parseModelLabel = %+v ok=%v", ref, ok)
	}
	if _, ok := parseModelLabel("no-slash"); ok {
		t.Error("a label without a slash must not parse")
	}
}

// TestModelStoreSharesFileWithTypeScript: the store reads and writes the same
// document the TS client uses, and must not discard the keys it does not read.
func TestModelStoreSharesFileWithTypeScript(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	path := filepath.Join(state, "opencode", "model.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `{"recent":[{"providerID":"anthropic","modelID":"claude-x"}],` +
		`"favorite":[{"providerID":"openai","modelID":"gpt-5"}],` +
		`"variant":{"anthropic/claude-x":"thinking"}}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	store := newModelStore()
	if got := store.recents(); len(got) != 1 || got[0].ModelID != "claude-x" {
		t.Errorf("recents = %v, want the TS-written entry", got)
	}
	if !store.isFavorite(modelRef{"openai", "gpt-5"}) {
		t.Error("expected the TS-written favorite to load")
	}

	store.toggleFavorite(modelRef{"anthropic", "claude-x"})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("wrote invalid JSON: %v", err)
	}
	// The variant map belongs to the TS client; writing from Go must not drop it.
	variant, ok := parsed["variant"].(map[string]any)
	if !ok || variant["anthropic/claude-x"] != "thinking" {
		t.Errorf("variant key was lost on write: %s", data)
	}
	if len(parsed["favorite"].([]any)) != 2 {
		t.Errorf("favorite = %v, want both entries", parsed["favorite"])
	}
}

func TestModelStoreRecentMovesToFront(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store := newModelStore()
	store.markRecent(modelRef{"a", "one"})
	store.markRecent(modelRef{"a", "two"})
	store.markRecent(modelRef{"a", "one"})

	recents := store.recents()
	if len(recents) != 2 {
		t.Fatalf("recents = %v, want two distinct entries", recents)
	}
	if recents[0].ModelID != "one" {
		t.Errorf("head = %q, want the most recently used", recents[0].ModelID)
	}
}

func TestModelStoreToggleFavorite(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	store := newModelStore()
	ref := modelRef{"anthropic", "claude"}
	if added := store.toggleFavorite(ref); !added {
		t.Error("first toggle should add")
	}
	if !store.isFavorite(ref) {
		t.Error("expected the model to be a favorite")
	}
	if added := store.toggleFavorite(ref); added {
		t.Error("second toggle should remove")
	}
	if store.isFavorite(ref) {
		t.Error("expected the favorite to be removed")
	}
}

// TestModelDialogRendersSections is an end-to-end check that the rendered
// panel actually shows the headings and the action row.
func TestModelDialogRendersSections(t *testing.T) {
	app := testApp(t)
	app.width, app.height = 120, 40
	app.openList("Select model", app.modelItems(testModels, ""))
	app.overlay.actions = app.modelDialogActions(testModels)

	frame, _ := app.overlayPanel()
	if !strings.Contains(frame, "Connect provider") {
		t.Errorf("the rendered dialog is missing the provider action:\n%s", frame)
	}
	if !strings.Contains(frame, "Free") {
		t.Errorf("the rendered dialog is missing the Free label:\n%s", frame)
	}
}
