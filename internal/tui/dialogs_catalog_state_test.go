package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// The reported symptom, on a fresh install with no credentials: opening the
// model dialog showed "Fetching the model catalog..." forever. Nothing was
// actually being fetched. The catalog had arrived and was legitimately empty,
// because /api/model lists only providers the user holds a credential for —
// but the dialog decided its empty state from list length alone, so "not
// loaded yet" and "loaded, and you have nothing connected" rendered the same.
func TestModelDialogEmptyStateDistinguishesLoadingFromNothingConnected(t *testing.T) {
	cases := []struct {
		name       string
		prepare    func(a *App)
		wantTitle  string
		wantInBody string
	}{
		{
			name:       "before the catalog arrives",
			prepare:    func(a *App) {},
			wantTitle:  "Loading models",
			wantInBody: "Fetching the model catalog",
		},
		{
			name: "catalog arrived empty",
			prepare: func(a *App) {
				a.update(catalogMsg{models: nil, providers: nil, providersOK: true})
			},
			wantTitle:  "No models available",
			wantInBody: "ctrl+p",
		},
		{
			name: "the request failed",
			prepare: func(a *App) {
				a.update(catalogMsg{err: errors.New("connection refused")})
			},
			wantTitle:  "Could not load models",
			wantInBody: "connection refused",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			app := testApp(t)
			c.prepare(app)
			app.openModelDialog(app.catalogModels)

			if got := app.overlay.emptyTitle; got != c.wantTitle {
				t.Errorf("emptyTitle = %q, want %q", got, c.wantTitle)
			}
			if body := app.overlay.emptyBody; !strings.Contains(body, c.wantInBody) {
				t.Errorf("emptyBody = %q, want it to mention %q", body, c.wantInBody)
			}
		})
	}
}

// TestCatalogRefreshDoesNotEmptyTheConnectDialog is the regression for the
// second half of the bug, and the reason the provider list looked broken too.
//
// Two commands wrote the same field from different endpoints:
// loadAllProvidersCmd fetched /api/provider?all=true (every provider in the
// catalog, which is what the connect dialog offers) and loadCatalogCmd
// fetched /api/provider (only the ones holding a credential — empty on a
// fresh machine). Both landed in a.providers and both re-rendered the open
// dialog, so whichever replied last won.
//
// On a fresh machine loadCatalogCmd is reliably the slower of the two: it
// makes the /api/model call that triggers the cold 4.4MB catalog download.
// So the dialog filled with every provider and was then wiped back to empty a
// moment later, which is exactly what "it starts loading and nothing happens"
// looks like from the outside.
func TestCatalogRefreshDoesNotEmptyTheConnectDialog(t *testing.T) {
	app := testApp(t)

	// The connect dialog is open and its fetch has landed.
	app.providersOverlay()
	app.update(providerListMsg{providers: testProviders})

	if len(app.overlay.items) == 0 {
		t.Fatal("the connect dialog should list the providers it just fetched")
	}
	before := len(app.overlay.items)

	// Now the slower catalog reply arrives carrying the reachable-only list,
	// which on a fresh machine is empty.
	app.update(catalogMsg{models: nil, providers: nil, providersOK: true})

	if got := len(app.overlay.items); got != before {
		t.Errorf("connect dialog dropped to %d items after the catalog refresh (was %d); "+
			"the reachable-only list must not overwrite the unfiltered one", got, before)
	}
	if _, ok := findItem(app.overlay.items, "anthropic"); !ok {
		t.Error("the provider list was emptied by the catalog refresh")
	}
}

// A provider fetch that fails must not blank the cached list either: that list
// feeds paidProviderAvailable, which would otherwise decide the user has
// connected nothing and pop the getting-started card on a transient error.
func TestFailedProviderFetchKeepsTheCachedList(t *testing.T) {
	app := testApp(t)
	app.update(catalogMsg{models: testModels, providers: testProviders, providersOK: true})

	app.update(catalogMsg{models: testModels, providers: nil, providersOK: false})

	if len(app.providers) != len(testProviders) {
		t.Errorf("providers = %d entries, want the cached %d kept on a failed fetch",
			len(app.providers), len(testProviders))
	}
}

// Display names come from both lists, so a provider the user has not connected
// still renders under its real name in the connect dialog rather than its id,
// and a catalog refresh does not drop the names the connect dialog supplied.
func TestProviderNamesSurviveACatalogRefresh(t *testing.T) {
	app := testApp(t)
	app.update(providerListMsg{providers: testProviders})
	app.update(catalogMsg{
		models:      testModels,
		providers:   []client.Provider{{ID: "anthropic", Name: "Anthropic"}},
		providersOK: true,
	})

	if got := app.providerNames["zzz-obscure"]; got != "Zzz Obscure" {
		t.Errorf("providerNames[zzz-obscure] = %q, want %q — the catalog refresh dropped it",
			got, "Zzz Obscure")
	}
}
