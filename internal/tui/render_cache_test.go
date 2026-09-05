package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

func settledAssistant(t *testing.T, id, text string) client.Message {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"agent": "build", "finish": "stop",
		"model":   map[string]string{"providerID": "anthropic", "id": "claude"},
		"content": []map[string]any{{"type": "text", "id": "t", "text": text}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client.Message{ID: id, Type: "assistant", TimeCreated: 1, Data: data}
}

func cacheApp(t *testing.T) *App {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = 120, 40
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp"}
	app.timeline = []client.Message{
		settledAssistant(t, "m1", "## One\n\nfirst"),
		settledAssistant(t, "m2", "## Two\n\nsecond"),
	}
	return app
}

// A settled message renders once and is served from the cache after that.
func TestSettledMessagesAreRenderedOnce(t *testing.T) {
	app := cacheApp(t)
	app.buildTimeline()
	if len(app.messageCache) != 2 {
		t.Fatalf("expected both messages cached, got %d", len(app.messageCache))
	}
	first := app.messageCache["m1"]
	app.buildTimeline()
	if app.messageCache["m1"].signature != first.signature {
		t.Fatal("a second pass should hit the cache, not re-key it")
	}
}

// Every input the render actually reads has to invalidate it.
func TestRenderCacheInvalidatesOnEveryRenderInput(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*App)
	}{
		{"width", func(a *App) { a.width = 80 }},
		{"busy", func(a *App) { a.busy = true }},
		{"timestamps", func(a *App) { a.timestamps = true }},
		{"message data", func(a *App) {
			a.timeline[0] = settledAssistant(t, "m1", "## One\n\nedited")
		}},
		{"theme", func(a *App) { a.theme = themeResolve("gocode-light"); a.invalidateRenderCache() }},
		{"thinking mode", func(a *App) { a.thinkingMode = "show"; a.invalidateRenderCache() }},
		{"model catalog", func(a *App) {
			a.modelNames = map[string]string{"anthropic/claude": "Claude"}
			a.invalidateRenderCache()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := cacheApp(t)
			app.buildTimeline()
			before := app.messageCache["m1"].signature

			tc.apply(app)
			after := app.renderSignature(app.timeline[0], false)
			if after == before {
				t.Fatalf("changing %s must invalidate the cached render", tc.name)
			}
		})
	}
}

// The catalog arriving after the first render is the concrete case: a
// settlement line resolved before it would otherwise keep the raw model ID.
func TestModelNamesArrivingLateReRenderTheSettlementLine(t *testing.T) {
	app := cacheApp(t)
	app.buildTimeline()

	drive(t, app, catalogMsg{
		models:    []client.Model{{ProviderID: "anthropic", ID: "claude", Name: "Claude Sonnet"}},
		providers: []client.Provider{{ID: "anthropic", Name: "Anthropic"}},
	})
	lines, _ := app.buildTimeline()
	if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "Claude Sonnet") {
		t.Fatal("the settlement line should pick up the model's display name")
	}
}

// The live message is the longest one and the one that changes most, so it is
// cached like the rest — and its spinner still has to advance from frame to
// frame, which is what spinnerPlaceholder buys.
func TestTheLiveMessageIsCachedAndStillSpins(t *testing.T) {
	app := cacheApp(t)
	running, err := json.Marshal(map[string]any{
		"agent": "build",
		"model": map[string]string{"providerID": "anthropic", "id": "claude"},
		"content": []map[string]any{
			{"type": "tool", "id": "c1", "name": "bash",
				"state": map[string]any{"status": "running", "input": map[string]any{"command": "ls"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.timeline = append(app.timeline, client.Message{ID: "m3", Type: "assistant", TimeCreated: 2, Data: running})
	app.busy = true

	app.animationsEnabled = true
	first, _ := app.buildTimeline()
	if _, cached := app.messageCache["m3"]; !cached {
		t.Fatal("the live message should be cached like the rest")
	}
	if _, cached := app.messageCache["m1"]; !cached {
		t.Fatal("the settled history behind it too")
	}
	if !strings.Contains(strings.Join(first, "\n"), spinnerFrames[0]) {
		t.Fatalf("expected the frame-0 glyph in the rendered timeline")
	}

	// A spinner tick must move the glyph even though the block itself is now
	// served from the cache.
	app.spinnerFrame += spinnerBrailleEvery
	second, _ := app.buildTimeline()
	if !strings.Contains(strings.Join(second, "\n"), spinnerFrames[1]) {
		t.Fatal("the cached live message stopped animating")
	}
	if strings.Contains(strings.Join(second, "\n"), spinnerPlaceholder) {
		t.Fatal("the placeholder leaked into rendered output")
	}
}

// The cache must not grow without bound across a long-lived session.
func TestRenderCacheIsBounded(t *testing.T) {
	app := cacheApp(t)
	for i := range 400 {
		app.timeline = []client.Message{settledAssistant(t, "m"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+i/26)), "body")}
		app.buildTimeline()
	}
	if len(app.messageCache) > 256 {
		t.Fatalf("cache grew to %d entries", len(app.messageCache))
	}
}
