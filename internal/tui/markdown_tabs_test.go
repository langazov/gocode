package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// mdApp builds a chat session whose timeline holds one assistant message
// with the given markdown body, sidebar docked.
func mdApp(t *testing.T, w, h int, body string) *App {
	t.Helper()
	app := benchApp(t, 0)
	app.width, app.height = w, h
	app.sidebar = true
	data, err := json.Marshal(map[string]any{
		"agent":  "build",
		"finish": "stop",
		"model":  map[string]string{"providerID": "anthropic", "id": "claude"},
		"content": []map[string]any{{
			"type": "text", "id": "t1", "text": body,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.timeline = append(app.timeline, client.Message{
		ID: "m1", Type: "assistant", TimeCreated: 1, Data: data,
	})
	app.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return app
}

// sidebarColumn finds the screen column the sidebar panel starts at, by
// locating its "Context" heading in the joined frame — the same anchor a
// reader perceives as "where the sidebar is".
func sidebarColumn(t *testing.T, app *App) int {
	t.Helper()
	for _, line := range strings.Split(app.viewChat(), "\n") {
		if idx := strings.Index(ansi.Strip(line), "Context"); idx > 0 {
			return idx
		}
	}
	return -1
}

// A tab is width-ambiguous: lipgloss.Width and ansi.StringWidth count it as
// one cell, but JoinHorizontal's getLines expands it to four spaces before
// measuring. A tab-indented code block therefore made the join measure three
// cells wider per tab than the chat column was rendered for, pushing the
// docked sidebar right whenever one was visible.
//
// This pins the fix: the column must measure the same with and without a
// code block, and the frame must never exceed the terminal.
func TestCodeBlockDoesNotShiftSidebar(t *testing.T) {
	// Each body pairs a tab-indented fence with its space-indented twin, so
	// the test also asserts the two render identically wide.
	cases := []struct {
		name   string
		tabbed string
		spaced string
	}{
		{
			name:   "one tab",
			tabbed: "Before.\n\n```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\n\nAfter.",
			spaced: "Before.\n\n```go\nfunc main() {\n    println(\"hi\")\n}\n```\n\nAfter.",
		},
		{
			name:   "nested tabs",
			tabbed: "```go\nfunc f() error {\n\tfor i := range 10 {\n\t\tif i > 2 {\n\t\t\treturn nil\n\t\t}\n\t}\n\treturn nil\n}\n```",
			spaced: "```go\nfunc f() error {\n    for i := range 10 {\n        if i > 2 {\n            return nil\n        }\n    }\n    return nil\n}\n```",
		},
		{
			name:   "plain fence, no language",
			tabbed: "```\n\tindented\tline\n```",
			spaced: "```\n    indented    line\n```",
		},
	}

	for _, w := range []int{140, 160, 200} {
		baseline := sidebarColumn(t, mdApp(t, w, 40, "Short paragraph."))
		if baseline < 0 {
			t.Fatalf("term=%d: sidebar not found in frame", w)
		}
		for _, tc := range cases {
			got := sidebarColumn(t, mdApp(t, w, 40, tc.tabbed))
			if got != baseline {
				t.Errorf("term=%d %s: sidebar shifted %d -> %d (+%d) with a tab-indented code block",
					w, tc.name, baseline, got, got-baseline)
			}
			// The tabbed and spaced renderings must agree.
			if spaced := sidebarColumn(t, mdApp(t, w, 40, tc.spaced)); spaced != got {
				t.Errorf("term=%d %s: tabbed column %d != spaced column %d", w, tc.name, got, spaced)
			}
		}
	}
}

// The joined frame must never exceed the terminal width, whatever the
// markdown contains. (Below the wide() breakpoint the sidebar is an overlay
// that frame() pads past the terminal by its own 2 margin columns — a
// separate pre-existing overhang, not this bug.)
func TestChatFrameFitsTerminalWithCodeBlocks(t *testing.T) {
	bodies := []string{
		"Short paragraph.",
		"```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```",
		"```go\n" + strings.Repeat("\tveryLongIdentifier := someCall(arg, arg2, arg3)\n", 6) + "```",
		strings.Repeat("para text ", 40),
	}
	for _, w := range []int{140, 160, 200} {
		for i, body := range bodies {
			app := mdApp(t, w, 40, body)
			frame := app.viewChat()
			for r, line := range strings.Split(frame, "\n") {
				if width := lipgloss.Width(line); width > w {
					t.Errorf("term=%d body=%d row=%d: %d cells > terminal %d: %q",
						w, i, r, width, w, ansi.Strip(line))
					break
				}
			}
		}
	}
}

// The rendered markdown itself must be tab-free, so every width measurer in
// the app agrees on what a block occupies.
func TestRenderedMarkdownContainsNoTabs(t *testing.T) {
	app := benchApp(t, 0)
	app.width, app.height = 160, 40
	for _, src := range []string{
		"```go\n\tif x {\n\t\treturn\n\t}\n```",
		"```\n\tplain fence\n```",
		"Text with a\ttab in a paragraph.",
	} {
		if out := app.renderMarkdown(src, 100); strings.ContainsRune(out, '\t') {
			t.Errorf("rendered markdown kept a tab: %q", out)
		}
	}
}
