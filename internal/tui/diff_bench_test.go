package tui

import (
	"context"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// benchDiff opens the viewer on a realistically large diff (30 files x 400
// lines) — the shape that made wheel scrolling swim.
func benchDiff(b *testing.B) *App {
	b.Helper()
	files := make([]client.FileDiff, 30)
	for i := range files {
		name := "dir" + integerDigits(i/10) + "/file" + integerDigits(i) + ".txt"
		files[i] = client.FileDiff{File: name, Additions: 400, Deletions: 400, Status: "modified", Patch: bigPatch(name, 400)}
	}
	// newTestApp takes *testing.T; benchmarks replicate its two lines
	// directly (the XDG isolation is irrelevant to a benchmark).
	b.Setenv("XDG_STATE_HOME", b.TempDir()+"/state")
	app := New(context.Background(), client.New("http://example.invalid"), "gocode-dark")
	app.width, app.height = 160, 50
	app.diffStatePath = ""
	app.Update(app.openDiffViewer()())
	app.Update(diffLoadedMsg{mode: diffModeGit, files: files})
	return app
}

// BenchmarkDiffWheelScroll measures one full wheel notch: Update + View.
func BenchmarkDiffWheelScroll(b *testing.B) {
	app := benchDiff(b)
	app.View() // settle
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.diffMouseWheel(false)
		_ = app.View()
	}
}

// BenchmarkDiffLayoutRebuild measures the worst case: a forced full rebuild
// (what every notch paid before the fix).
func BenchmarkDiffLayoutRebuild(b *testing.B) {
	app := benchDiff(b)
	app.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.diff.loadSeq++
		app.diff.layoutKey = ""
		app.buildDiffLayout()
	}
}

// BenchmarkDiffView measures the settled render: window slice + join.
func BenchmarkDiffView(b *testing.B) {
	app := benchDiff(b)
	app.View()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = app.View()
	}
}
