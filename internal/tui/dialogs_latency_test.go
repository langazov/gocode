package tui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anomalyco/opencode-go/internal/tui/client"
)

// slowServer stands in for a backend that is not instant, and counts the
// requests made while a dialog is being opened.
func slowServer(t *testing.T, delay time.Duration, calls *atomic.Int32) *client.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(delay)
		w.Write([]byte(`[]`))
	}))
	t.Cleanup(server.Close)
	return client.New(server.URL)
}

// TestDialogsOpenWithoutWaitingOnTheNetwork is the regression for "when
// pressing ctrl+key combinations, the dialogs are shown with delays".
//
// The dialogs used to fetch and only then call openList, so the panel did not
// appear until a full round trip finished. Opening must be synchronous against
// cached state; the refresh is what happens afterwards.
func TestDialogsOpenWithoutWaitingOnTheNetwork(t *testing.T) {
	const backendDelay = 300 * time.Millisecond

	cases := []struct {
		name  string
		title string
		open  func(a *App) any
	}{
		{"model", "Select model", func(a *App) any { return a.modelsOverlay() }},
		{"provider", "Connect a provider", func(a *App) any { return a.providersOverlay() }},
		{"agent", "Select agent", func(a *App) any { return a.agentsOverlay() }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			app := testApp(t)
			app.ctx = context.Background()
			app.client = slowServer(t, backendDelay, &calls)
			// Seed the caches the way a running session would have them.
			app.catalogModels = testModels
			app.providers = testProviders
			app.agentList = []client.Agent{{ID: "build"}, {ID: "plan"}}

			start := time.Now()
			c.open(app)
			elapsed := time.Since(start)

			if app.overlay == nil {
				t.Fatal("the dialog must be open as soon as the key is handled")
			}
			if app.overlay.title != c.title {
				t.Errorf("title = %q, want %q", app.overlay.title, c.title)
			}
			if len(app.overlay.items) == 0 {
				t.Error("the dialog must render the cached list, not an empty one")
			}
			// The assertion that matters: opening did not block on the backend.
			if elapsed >= backendDelay {
				t.Errorf("opening took %v with a %v backend — the dialog is waiting on the network", elapsed, backendDelay)
			}
			if got := calls.Load(); got != 0 {
				t.Errorf("opening made %d requests; the refresh belongs in the returned command, not the open path", got)
			}
		})
	}
}

// TestModelDialogShowsLoadingBeforeCatalogArrives: on the very first open the
// cache is empty, and "No results found" would read as "you have no models".
func TestModelDialogShowsLoadingBeforeCatalogArrives(t *testing.T) {
	var calls atomic.Int32
	app := testApp(t)
	app.ctx = context.Background()
	app.client = slowServer(t, time.Millisecond, &calls)
	app.catalogModels = nil

	app.modelsOverlay()
	if app.overlay == nil {
		t.Fatal("expected the dialog to open")
	}
	if app.overlay.emptyTitle != "Loading models" {
		t.Errorf("emptyTitle = %q, want a loading state", app.overlay.emptyTitle)
	}
}

// TestCatalogRefreshUpdatesOpenDialog: the background refresh has to land in
// the open panel, or the first open of a session stays empty.
func TestCatalogRefreshUpdatesOpenDialog(t *testing.T) {
	app := testApp(t)
	app.ctx = context.Background()
	app.catalogModels = nil
	app.modelsOverlay()
	if len(app.overlay.items) != 0 {
		t.Fatal("expected an empty dialog before the catalog arrives")
	}

	app.update(catalogMsg{models: testModels, providers: testProviders})

	if app.overlay == nil || app.overlay.title != "Select model" {
		t.Fatal("the refresh must not close the dialog")
	}
	if len(app.overlay.items) == 0 {
		t.Error("the refreshed catalog must populate the open dialog")
	}
}

// TestCatalogRefreshKeepsSelection: a refresh arriving while the user is
// moving through the list must not move the cursor under them.
func TestCatalogRefreshKeepsSelection(t *testing.T) {
	app := testApp(t)
	app.ctx = context.Background()
	app.catalogModels = testModels
	app.modelsOverlay()

	target := 2
	if len(app.overlay.items) <= target {
		t.Fatalf("only %d items", len(app.overlay.items))
	}
	app.overlay.selected = target
	want := app.overlay.items[target].value

	app.update(catalogMsg{models: testModels, providers: testProviders})

	got := app.overlay.items[app.overlay.selected].value
	if got != want {
		t.Errorf("selection moved from %q to %q across a refresh", want, got)
	}
}
