package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// benchApp builds a session the size and shape of a real one: markdown with
// headings, lists, prose and fenced code, which is what makes a render
// expensive (glamour parses it and chroma highlights every fence).
func benchApp(tb testing.TB, n int) *App {
	tb.Setenv("XDG_STATE_HOME", tb.TempDir()+"/state")
	app := New(context.Background(), client.New("http://example.invalid"), "gocode-dark")
	app.width, app.height = 160, 50
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Directory: "/tmp/p"}
	code := "```go\nfunc Example() error {\n\tfor i := range 10 {\n\t\tfmt.Println(i)\n\t}\n\treturn nil\n}\n```"
	for i := range n {
		body := fmt.Sprintf(
			"## Section %d\n\nSome **prose** with `code` spans and a list:\n\n- one\n- two\n- three\n\n%s\n\nA trailing paragraph long enough to wrap across the content width several times over.",
			i, code)
		data, err := json.Marshal(map[string]any{
			"agent": "build", "finish": "stop",
			"model":   map[string]string{"providerID": "anthropic", "id": "claude"},
			"content": []map[string]any{{"type": "text", "id": "t", "text": body}},
		})
		if err != nil {
			tb.Fatal(err)
		}
		app.timeline = append(app.timeline, client.Message{
			ID: fmt.Sprintf("m%d", i), Type: "assistant", TimeCreated: 1, Data: data,
		})
	}
	return app
}

// BenchmarkKeypress is the number that matters: one keystroke is an Update
// plus the View that follows it, and a key held down repeats this ~30 times a
// second. Before the render cache this was ~84ms — every keystroke re-parsed
// and re-highlighted the whole visible history — which is what made a held key
// visibly lag behind.
func BenchmarkKeypress(b *testing.B) {
	app := benchApp(b, 60)
	app.View() // warm the cache, as a running session's would be
	b.ResetTimer()
	for range b.N {
		app.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
		_ = app.View()
	}
}

// The cold path: every message rendered from scratch. This is what the cache
// avoids paying on each frame.
func BenchmarkColdTimeline(b *testing.B) {
	app := benchApp(b, 60)
	b.ResetTimer()
	for range b.N {
		app.messageCache = nil
		app.buildTimeline()
	}
}

// busyApp is benchApp mid-turn: the settled history behind a live assistant
// message of roughly liveKB kilobytes, with a.busy set. The live message is
// the expensive one — it is the response still being written, so it is both
// the longest and the only one whose content changes — and it used to be
// exempt from the render cache.
func busyApp(tb testing.TB, n, liveKB int) *App {
	app := benchApp(tb, n)
	code := "```go\nfunc Example() error {\n\tfor i := range 10 {\n\t\tfmt.Println(i)\n\t}\n\treturn nil\n}\n```"
	var body strings.Builder
	for body.Len() < liveKB*1024 {
		fmt.Fprintf(&body,
			"## Live %d\n\nProse with `spans` and a list:\n\n- one\n- two\n\n%s\n\n", body.Len(), code)
	}
	data, err := json.Marshal(map[string]any{
		"agent":   "build",
		"model":   map[string]string{"providerID": "anthropic", "id": "claude"},
		"content": []map[string]any{{"type": "text", "id": "live", "text": body.String()}},
	})
	if err != nil {
		tb.Fatal(err)
	}
	app.timeline = append(app.timeline, client.Message{ID: "live", Type: "assistant", TimeCreated: 2, Data: data})
	app.busy = true
	return app
}

// One frame of a running turn. The spinner ticks every 40ms, so this is the
// budget: over it, the animation cannot keep time and every keystroke queues
// behind a render. With the live message exempt from the cache this was
// 107ms at 16KB and 314ms at 48KB — it grew with the response, which is why
// the interface got less responsive the longer an answer ran.
func BenchmarkBusyFrame16KB(b *testing.B) {
	app := busyApp(b, 60, 16)
	app.View()
	b.ResetTimer()
	for range b.N {
		app.Update(spinnerTickMsg{})
		_ = app.View()
	}
}

func BenchmarkBusyFrame48KB(b *testing.B) {
	app := busyApp(b, 60, 48)
	app.View()
	b.ResetTimer()
	for range b.N {
		app.Update(spinnerTickMsg{})
		_ = app.View()
	}
}

// A keystroke typed while a turn is running: the editor only sees the key
// once View returns, so this number is the prompt's input latency.
func BenchmarkBusyKeypress16KB(b *testing.B) {
	app := busyApp(b, 60, 16)
	app.View()
	b.ResetTimer()
	for range b.N {
		app.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
		_ = app.View()
	}
}
