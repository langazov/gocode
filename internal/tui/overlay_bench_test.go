package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
)

// BenchmarkDialogFrame measures a full View() with a dialog overlay open —
// the path compositeDialog sits on.
func BenchmarkDialogFrame(b *testing.B) {
	app := benchApp(b, 60)
	app.View()
	app.openList("Commands", app.commandsRegistry())
	app.View()
	b.ResetTimer()
	for range b.N {
		_ = app.View()
	}
}

// BenchmarkDialogComposite isolates the compositeDialog pass itself.
func BenchmarkDialogComposite(b *testing.B) {
	app := benchApp(b, 60)
	app.openList("Commands", app.commandsRegistry())
	base := app.frame(app.underlay())
	panel, _ := app.overlayPanel()
	top, left := app.overlayOrigin(lipgloss.Width(panel))
	b.ResetTimer()
	for range b.N {
		_ = app.compositeDialog(base, panel, top, left)
	}
}
