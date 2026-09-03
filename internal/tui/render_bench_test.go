package tui

import (
	"context"
	"encoding/json"
	"fmt"
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
