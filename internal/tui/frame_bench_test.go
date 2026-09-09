package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// buildRawChat produces a frame-scale string without calling frame().
func buildRawChat(b *testing.B, app *App) string {
	b.Helper()
	lines, _, _, _ := app.buildTimeline()
	return strings.Join(lines, "\n")
}

// BenchmarkFrameManual is the current frame(): manual string manipulation.
func BenchmarkFrameManual(b *testing.B) {
	app := benchApp(b, 60)
	content := buildRawChat(b, app)
	b.ResetTimer()
	for range b.N {
		_ = app.frame(content)
	}
}

// BenchmarkFrameStyle is lipgloss's Padding+MaxHeight on the same content.
func BenchmarkFrameStyle(b *testing.B) {
	app := benchApp(b, 60)
	content := buildRawChat(b, app)
	b.ResetTimer()
	for range b.N {
		_ = lipgloss.NewStyle().Padding(0, 1).MaxHeight(app.height).Render(content)
	}
}
