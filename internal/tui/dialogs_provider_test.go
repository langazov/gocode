package tui

import (
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

var testProviders = []client.Provider{
	{ID: "zzz-obscure", Name: "Zzz Obscure"},
	{ID: "anthropic", Name: "Anthropic", Connected: true},
	{ID: "opencode", Name: "OpenCode"},
	{ID: "aaa-obscure", Name: "Aaa Obscure"},
	{ID: "openai", Name: "OpenAI"},
}

// TestProviderDialogOrdersByPriority covers PROVIDER_PRIORITY: the well-known
// providers lead in a fixed order, everything else follows by name.
func TestProviderDialogOrdersByPriority(t *testing.T) {
	app := testApp(t)
	items := app.providerItems(testProviders)

	var ids []string
	for _, item := range items {
		ids = append(ids, item.value)
	}
	want := []string{"opencode", "openai", "anthropic", "aaa-obscure", "zzz-obscure", customProviderValue}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("order = %v, want %v", ids, want)
		}
	}
}

// TestProviderDialogHasCustomOption is the "add a provider that is not in the
// catalog" row the reported gap was missing.
func TestProviderDialogHasCustomOption(t *testing.T) {
	app := testApp(t)
	items := app.providerItems(testProviders)
	last := items[len(items)-1]
	if last.value != customProviderValue {
		t.Fatalf("last row = %q, want the custom-provider option", last.value)
	}
	if last.label != "Other" || last.hint != "Custom provider" {
		t.Errorf("custom row = %+v, want Other/Custom provider", last)
	}
}

func TestProviderDialogCategories(t *testing.T) {
	app := testApp(t)
	items := app.providerItems(testProviders)
	for _, item := range items {
		want := "Providers"
		if _, popular := providerPriority[item.value]; popular {
			want = "Popular"
		}
		if item.category != want {
			t.Errorf("%s category = %q, want %q", item.value, item.category, want)
		}
	}
}

// TestProviderDialogTicksConnected covers the ✓ gutter.
func TestProviderDialogTicksConnected(t *testing.T) {
	app := testApp(t)
	items := app.providerItems(testProviders)

	connected, _ := findItem(items, "anthropic")
	if connected.gutter != "✓" || !connected.gutterOK {
		t.Errorf("connected provider = %+v, want a success-colored ✓", connected)
	}
	unconnected, _ := findItem(items, "openai")
	if unconnected.gutter != "" {
		t.Errorf("unconnected provider must have no tick, got %q", unconnected.gutter)
	}
}

func TestProviderDialogDescriptions(t *testing.T) {
	app := testApp(t)
	items := app.providerItems(testProviders)
	opencode, _ := findItem(items, "opencode")
	if opencode.hint != "(Recommended)" {
		t.Errorf("opencode hint = %q, want %q", opencode.hint, "(Recommended)")
	}
	obscure, _ := findItem(items, "zzz-obscure")
	if obscure.hint != "" {
		t.Errorf("a provider with no blurb should have none, got %q", obscure.hint)
	}
}

// TestCustomProviderIDValidation ports CUSTOM_PROVIDER_ID.
func TestCustomProviderIDValidation(t *testing.T) {
	valid := []string{"acme", "acme-ai", "acme_ai", "a1", "0acme"}
	invalid := []string{"", "-acme", "_acme", "Acme", "acme ai", "acme/ai", "acme.ai"}
	for _, id := range valid {
		if !customProviderID.MatchString(id) {
			t.Errorf("%q should be a valid provider id", id)
		}
	}
	for _, id := range invalid {
		if customProviderID.MatchString(id) {
			t.Errorf("%q should be rejected", id)
		}
	}
}

// TestProviderDialogRenders is the end-to-end check that the panel shows the
// headings, the tick and the custom row.
func TestProviderDialogRenders(t *testing.T) {
	app := testApp(t)
	app.width, app.height = 120, 40
	app.openList("Connect a provider", app.providerItems(testProviders))
	app.overlay.size = dialogLarge

	frame, _ := app.overlayPanel()
	for _, want := range []string{"Connect a provider", "Popular", "Providers", "Other", "✓"} {
		if !strings.Contains(frame, want) {
			t.Errorf("rendered dialog is missing %q:\n%s", want, frame)
		}
	}
}

// TestOAuthWaitShowsCodeAndURL: a device flow is unusable unless the code and
// URL are actually on screen while it polls.
func TestOAuthWaitShowsCodeAndURL(t *testing.T) {
	app := testApp(t)
	app.width, app.height = 120, 40
	app.showOAuthWait("GitHub Copilot", &client.OAuthAttempt{
		URL:  "https://github.com/login/device",
		Code: "ABCD-1234",
	})

	frame, _ := app.overlayPanel()
	for _, want := range []string{"GitHub Copilot", "https://github.com/login/device", "ABCD-1234", "Waiting"} {
		if !strings.Contains(frame, want) {
			t.Errorf("oauth wait panel is missing %q:\n%s", want, frame)
		}
	}
	if !app.overlay.locked {
		t.Error("the wait panel must be locked: there is nothing to select while polling")
	}
}
