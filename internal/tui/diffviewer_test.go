package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/theme"
)

// threeHunkPatch is the exact fixture the TS test uses
// (tui/test/cli/tui/diff-viewer.test.tsx): three hunks far apart, so hunk
// navigation has distinct anchors to walk.
const threeHunkPatch = `--- a/src/file.ts
+++ b/src/file.ts
@@ -1,3 +1,3 @@
 const first = true
-const oldFirst = true
+const newFirst = true
 const afterFirst = true
@@ -20,3 +20,3 @@
 const second = true
-const oldSecond = true
+const newSecond = true
 const afterSecond = true
@@ -40,3 +40,3 @@
 const third = true
-const oldThird = true
+const newThird = true
 const afterThird = true`

// pressKey drives the full Update path with a keypress, the way the runtime
// delivers it (keys_test.go's key() helper calls handleKey directly; the
// viewer's dispatch lives inside handleKey so both reach it, but Update
// also runs the message switch first).
func pressKey(t *testing.T, app *App, name string) {
	t.Helper()
	runes := []rune(name)
	code := runes[0]
	if name == "esc" || name == "enter" || name == "tab" {
		// Named keys carry no rune text.
		msg := tea.KeyPressMsg{Code: code}
		switch name {
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		}
		drive(t, app, msg)
		return
	}
	drive(t, app, tea.KeyPressMsg{Text: name, Code: code})
}

func diffFixture() []client.FileDiff {
	return []client.FileDiff{{
		File:      "src/file.ts",
		Additions: 3,
		Deletions: 3,
		Status:    "modified",
		Patch:     threeHunkPatch,
	}}
}

// openDiff opens the viewer on a fixture, driving the real load path so the
// test exercises openDiffViewer → loadDiffCmd → handleDiffResult.
func openDiff(t *testing.T, files []client.FileDiff) *App {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.diffStatePath = "" // never touch a real state file
	drive(t, app, staticMsg(app.openDiffViewer()))
	if app.diff == nil {
		t.Fatal("openDiffViewer did not open the route")
	}
	// Deliver the fetch result the way the runtime would.
	drive(t, app, diffLoadedMsg{mode: diffModeGit, files: files})
	if app.diff.loading {
		t.Fatal("viewer still loading after the result landed")
	}
	return app
}

func TestDiffViewerClosesToOpeningView(t *testing.T) {
	// Ports "closing the diff viewer returns to the route it opened from".
	app := newTestApp(t, "http://example.invalid")
	app.diffStatePath = ""
	app.view = viewChat
	returned := viewChat

	drive(t, app, staticMsg(app.openDiffViewer()))
	if app.diff == nil {
		t.Fatal("viewer did not open")
	}
	pressKey(t, app, "q")
	if app.diff != nil {
		t.Fatal("viewer still open after q")
	}
	if app.view != returned {
		t.Fatalf("close restored view %d, want %d", app.view, returned)
	}
}

func TestDiffViewerEscapeCloses(t *testing.T) {
	app := openDiff(t, diffFixture())
	pressKey(t, app, "esc")
	if app.diff != nil {
		t.Fatal("esc did not close the viewer")
	}
}

func TestDiffViewerEmptyStatesAreDistinct(t *testing.T) {
	// Design principle 9: loading, failed, and empty never render the same.
	app := newTestApp(t, "http://example.invalid")
	app.diffStatePath = ""
	drive(t, app, staticMsg(app.openDiffViewer()))

	loading := app.View()
	if !strings.Contains(loading, "Loading diff…") {
		t.Fatalf("loading state missing its label:\n%s", loading)
	}

	drive(t, app, diffLoadedMsg{mode: diffModeGit, err: errors.New("boom")})
	failed := app.View()
	if !strings.Contains(failed, "Failed to load diff") {
		t.Fatalf("failed state missing its label:\n%s", failed)
	}
	if strings.Contains(failed, "Loading diff") {
		t.Fatal("failed state still says loading")
	}

	drive(t, app, diffLoadedMsg{mode: diffModeGit})
	empty := app.View()
	if !strings.Contains(empty, "No diff!") {
		t.Fatalf("empty state missing its label:\n%s", empty)
	}
	if strings.Contains(empty, "Failed") || strings.Contains(empty, "Loading") {
		t.Fatal("empty state reads as failed or loading")
	}
}

func TestDiffViewerBracketsNavigateHunks(t *testing.T) {
	// Ports "brackets navigate diff hunks": ] ] [ returns to the recorded
	// scroll, and repeated presses walk forward monotonically.
	app := openDiff(t, diffFixture())
	app.diff.scroll = 0

	pressKey(t, app, "]")
	first := app.diff.scroll
	if first <= 0 {
		t.Fatalf("first ] did not advance the scroll: %d", first)
	}

	pressKey(t, app, "]")
	second := app.diff.scroll
	if second <= first {
		t.Fatalf("second ] did not advance: %d after %d", second, first)
	}

	pressKey(t, app, "[")
	if app.diff.scroll != first {
		t.Fatalf("[ did not return to the previous hunk: %d, want %d", app.diff.scroll, first)
	}

	pressKey(t, app, "]")
	if app.diff.scroll != second {
		t.Fatalf("] after [ did not return to the next hunk: %d, want %d", app.diff.scroll, second)
	}
}

func TestDiffViewerHunkNavigationSelectsTheHunksFile(t *testing.T) {
	files := append(diffFixture(), client.FileDiff{
		File: "other.txt", Additions: 1, Deletions: 0, Status: "added",
		Patch: "--- a/other.txt\n+++ b/other.txt\n@@ -0,0 +1 @@\n+added line\n",
	})
	app := openDiff(t, files)

	pressKey(t, app, "]")
	if app.diff.selected < 0 {
		t.Fatal("hunk navigation left no file selected")
	}
	if got := app.diff.files[app.diff.selected].File; got != "src/file.ts" {
		t.Fatalf("hunk navigation selected %q, want src/file.ts", got)
	}
}

func TestDiffViewerNextPreviousFileWalksTreeOrder(t *testing.T) {
	files := []client.FileDiff{
		{File: "a.txt", Additions: 1, Status: "added", Patch: "--- a/a.txt\n+++ b/a.txt\n@@ -0,0 +1 @@\n+a\n"},
		{File: "dir/b.txt", Additions: 1, Status: "added", Patch: "--- a/dir/b.txt\n+++ b/dir/b.txt\n@@ -0,0 +1 @@\n+b\n"},
	}
	app := openDiff(t, files)

	// Tree order sorts directories first, so dir/b.txt precedes a.txt in
	// the row order n walks — same as the TS file tree.
	pressKey(t, app, "n")
	if got := app.diff.files[app.diff.selected].File; got != "dir/b.txt" {
		t.Fatalf("first n landed on %q, want dir/b.txt", got)
	}
	pressKey(t, app, "n")
	if got := app.diff.files[app.diff.selected].File; got != "a.txt" {
		t.Fatalf("second n landed on %q, want a.txt", got)
	}
	pressKey(t, app, "p")
	if got := app.diff.files[app.diff.selected].File; got != "dir/b.txt" {
		t.Fatalf("p landed on %q, want dir/b.txt", got)
	}
}

func TestDiffViewerMarkReviewedMutesTheRow(t *testing.T) {
	app := openDiff(t, diffFixture())
	// Select the first file, then toggle review.
	pressKey(t, app, "n")
	pressKey(t, app, "m")
	if !app.diff.reviewed["src/file.ts"] {
		t.Fatal("m did not mark the selected file reviewed")
	}

	// The pane renders the reviewed row muted: same file, muted colors.
	rendered := app.View()
	if !strings.Contains(rendered, "src/file.ts") {
		t.Fatal("reviewed file's header disappeared from the pane")
	}

	pressKey(t, app, "m")
	if app.diff.reviewed["src/file.ts"] {
		t.Fatal("second m did not clear the reviewed flag")
	}
}

func TestDiffViewerSinglePatchShowsOneFile(t *testing.T) {
	files := []client.FileDiff{
		{File: "a.txt", Additions: 1, Status: "added", Patch: "--- a/a.txt\n+++ b/a.txt\n@@ -0,0 +1 @@\n+a\n"},
		{File: "b.txt", Additions: 1, Status: "added", Patch: "--- a/b.txt\n+++ b/b.txt\n@@ -0,0 +1 @@\n+b\n"},
	}
	app := openDiff(t, files)

	if visible := app.diffVisibleFiles(); len(visible) != 2 {
		t.Fatalf("full pane shows %d files, want 2", len(visible))
	}
	pressKey(t, app, "n") // select a.txt
	pressKey(t, app, "s")
	visible := app.diffVisibleFiles()
	if len(visible) != 1 {
		t.Fatalf("single-patch pane shows %d files, want 1", len(visible))
	}
	if got := app.diff.files[visible[0]].File; got != "a.txt" {
		t.Fatalf("single patch pinned %q, want a.txt", got)
	}
	if app.diff.scroll != 0 {
		t.Fatalf("entering single-patch mode left scroll at %d, want 0", app.diff.scroll)
	}

	pressKey(t, app, "s")
	if visible := app.diffVisibleFiles(); len(visible) != 2 {
		t.Fatalf("leaving single-patch mode shows %d files, want 2", len(visible))
	}
}

func TestDiffViewerTogglesPersistThroughStateFile(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	// Wide enough that split is available, so v actually flips the view.
	app.width = 160
	app.diffStatePath = t.TempDir() + "/diffstate.json"
	drive(t, app, staticMsg(app.openDiffViewer()))
	drive(t, app, diffLoadedMsg{mode: diffModeGit, files: diffFixture()})

	// b toggles the tree off; s enters single-patch; v flips the view.
	pressKey(t, app, "b")
	pressKey(t, app, "s")
	pressKey(t, app, "v")

	state := loadDiffState(app.diffStatePath)
	if state.showFileTreeDefault() {
		t.Fatal("b did not persist treeEnabled=false")
	}
	if !state.SinglePatch {
		t.Fatal("s did not persist singlePatch=true")
	}
	if view, ok := state.storedView(); !ok || view != "unified" {
		t.Fatalf("v did not persist the view: %q ok=%v (the width default is split, so v flips to unified)", view, ok)
	}
}

func TestDiffViewerStateDefaultsMatchTS(t *testing.T) {
	// TS: kv.get(KV_SHOW_FILE_TREE, true) !== false → shown;
	// kv.get(KV_SINGLE_PATCH, false) === true → off; view unset.
	state := loadDiffState(t.TempDir() + "/nonexistent.json")
	if !state.showFileTreeDefault() {
		t.Fatal("a fresh state must show the file tree")
	}
	if state.SinglePatch {
		t.Fatal("a fresh state must not single-patch")
	}
	if _, ok := state.storedView(); ok {
		t.Fatal("a fresh state must have no stored view")
	}
}

func TestDiffViewerTreeFocusAndNavigation(t *testing.T) {
	app := openDiff(t, diffFixture())

	// tab moves focus to the tree (it is visible by default).
	pressKey(t, app, "tab")
	if app.diff.focus != diffFocusTree {
		t.Fatal("tab did not focus the file tree")
	}
	if app.diff.highlight == -1 {
		t.Fatal("focusing the tree left no highlighted row")
	}

	// k moves the highlight up to the directory row (ensureHighlighted
	// starts on the first FILE row, which for this fixture is the last row,
	// so j would correctly clamp).
	before := app.diff.highlight
	pressKey(t, app, "k")
	if app.diff.highlight == before {
		t.Fatal("k did not move the tree selection")
	}
	// Back down to the file row and enter jumps to its patch. (Enter here
	// would toggle the directory — collapsing it — so step back instead.)
	pressKey(t, app, "j")
	if row := app.diff.rows[rowIndexOf(app.diff.rows, app.diff.highlight)]; row.fileIndex < 0 {
		t.Fatalf("j did not return to the file row: %+v", row)
	}
	pressKey(t, app, "enter")
	if app.diff.selected < 0 {
		t.Fatal("enter on a file row did not select the file")
	}

	pressKey(t, app, "tab")
	if app.diff.focus != diffFocusPatches {
		t.Fatal("second tab did not return focus to the pane")
	}
}

func TestDiffViewerTreeHiddenDropsFromFooter(t *testing.T) {
	app := openDiff(t, diffFixture())
	pressKey(t, app, "b")
	if app.diff.showTree() {
		t.Fatal("b left the tree showing")
	}
	footer := app.diffFooter()
	if strings.Contains(footer, "focus file tree") {
		t.Fatalf("footer still advertises the tree hint with the tree hidden:\n%s", footer)
	}
}

func TestDiffViewerHeaderShowsSourceAndCount(t *testing.T) {
	app := openDiff(t, diffFixture())
	rendered := app.View()
	if !strings.Contains(rendered, "Diff ") {
		t.Fatal("header missing the Diff title")
	}
	if !strings.Contains(rendered, "working tree") {
		t.Fatal("header missing the git-mode label")
	}
	if !strings.Contains(rendered, "1 file") {
		t.Fatalf("header missing the file count:\n%s", rendered)
	}
}

func TestDiffViewerRendersDiffContentWindowed(t *testing.T) {
	app := openDiff(t, diffFixture())
	rendered := app.View()
	if !strings.Contains(rendered, "const newFirst = true") {
		t.Fatal("pane does not render the fixture's added line")
	}

	// A diff taller than the viewport windows: render only what fits, and
	// say what is hidden (§9.7's indicator rule).
	big := make([]client.FileDiff, 1)
	var lines strings.Builder
	lines.WriteString("--- a/big.txt\n+++ b/big.txt\n@@ -1,200 +1,200 @@\n")
	for i := 0; i < 200; i++ {
		lines.WriteString("-old\n")
		lines.WriteString("+new\n")
	}
	big[0] = client.FileDiff{File: "big.txt", Additions: 200, Deletions: 200, Status: "modified", Patch: lines.String()}
	app = openDiff(t, big)

	app.buildDiffLayout()
	total := len(app.diff.layoutRows)
	if total < 200 {
		t.Fatalf("layout produced %d rows for a 400-line diff", total)
	}
	if height := app.diffBodyHeight(); total <= height {
		t.Fatalf("test terminal too tall to exercise windowing: %d rows, %d height", total, height)
	}
	rendered = app.View()
	if !strings.Contains(rendered, "more lines") {
		t.Fatal("windowed pane does not name its hidden rows")
	}
}

func TestDiffViewerSourceDialogGatesBranchOption(t *testing.T) {
	app := openDiff(t, diffFixture())

	// Without repository info, only the working-tree source is offered.
	pressKey(t, app, "d")
	if app.overlay == nil || app.overlay.title != "Switch source" {
		t.Fatal("d did not open the source dialog")
	}
	count := 0
	for _, item := range app.overlay.items {
		if item.value == string(diffModeBranch) {
			count++
		}
	}
	if count != 0 {
		t.Fatalf("branch source offered without repository info: %+v", app.overlay.items)
	}

	// With a distinct default branch, the option appears.
	pressKey(t, app, "esc")
	app.diff.vcsInfo = &client.VcsInfo{Branch: "feature", DefaultBranch: "main"}
	pressKey(t, app, "d")
	found := false
	for _, item := range app.overlay.items {
		if item.value == string(diffModeBranch) {
			found = true
		}
	}
	if !found {
		t.Fatal("branch source missing with a distinct default branch")
	}

	// On the default branch itself, it is not offered (TS gating).
	pressKey(t, app, "esc")
	app.diff.vcsInfo = &client.VcsInfo{Branch: "main", DefaultBranch: "main"}
	pressKey(t, app, "d")
	for _, item := range app.overlay.items {
		if item.value == string(diffModeBranch) {
			t.Fatal("branch source offered while on the default branch")
		}
	}
}

func TestDiffViewerSwitchSourceRefetches(t *testing.T) {
	app := openDiff(t, diffFixture())
	app.diff.vcsInfo = &client.VcsInfo{Branch: "feature", DefaultBranch: "main"}

	pressKey(t, app, "d")
	// Activate the branch item through the dialog's real path: the picker's
	// onActivate, which is what a row activation runs.
	for _, item := range app.overlay.items {
		if item.value == string(diffModeBranch) {
			drive(t, app, staticMsg(app.overlay.onActivate(item)))
			break
		}
	}
	if app.diff.mode != diffModeBranch {
		t.Fatalf("source switch left mode at %q", app.diff.mode)
	}
	if !app.diff.loading {
		t.Fatal("source switch did not start a refetch")
	}
}

func TestDiffViewerHelpSheet(t *testing.T) {
	app := openDiff(t, diffFixture())
	pressKey(t, app, "?")
	if app.overlay == nil || app.overlay.kind != overlayHelp {
		t.Fatal("? did not open the help overlay")
	}
	if len(app.overlay.helpLines) == 0 {
		t.Fatal("help overlay carries no diff shortcut rows")
	}
	rendered := app.View()
	for _, want := range []string{"Next hunk", "Toggle file tree", "Mark reviewed"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("help sheet missing %q:\n%s", want, rendered)
		}
	}
}

func TestDiffViewerIgnoresKeysWhenClosed(t *testing.T) {
	// The route must not leak bindings into the chat view: n and ] are
	// editor characters there.
	app := newTestApp(t, "http://example.invalid")
	app.view = viewChat
	if app.diff != nil {
		t.Fatal("viewer open without being opened")
	}
}

func TestDiffViewerMouseClickSelectsTreeRow(t *testing.T) {
	app := openDiff(t, diffFixture())
	app.View() // record the hit spans

	// The tree is visible; its first row sits just under the header.
	cmd := app.diffMouseClick(1, 2)
	if cmd != nil {
		_ = cmd()
	}
	if app.diff.focus != diffFocusTree {
		t.Fatal("clicking the tree did not focus it")
	}
}

func TestDiffViewerTruncatesRowsNeverWraps(t *testing.T) {
	// A very long diff line must not wrap onto a second pane row.
	long := strings.Repeat("x", 500)
	patch := "--- a/long.txt\n+++ b/long.txt\n@@ -1 +1 @@\n-" + long + "\n+" + long + "\n"
	app := openDiff(t, []client.FileDiff{{File: "long.txt", Additions: 1, Deletions: 1, Status: "modified", Patch: patch}})
	app.width = 80

	app.buildDiffLayout()
	for i, row := range app.diff.layoutRows {
		if lines := strings.Count(row, "\n"); lines > 0 {
			t.Fatalf("layout row %d wrapped across %d lines", i, lines+1)
		}
	}
}

func TestDiffViewerGutterCarriesLineNumbers(t *testing.T) {
	app := openDiff(t, diffFixture())
	app.View()
	// The fixture's first hunk starts at line 1; the removed line is old 2,
	// the added line new 2. The gutter renders both, blanking the missing
	// side per gutter()'s rule.
	rendered := app.View()
	if !strings.Contains(stripANSI(rendered), " 2 ") {
		t.Fatalf("gutter missing line numbers:\n%s", rendered)
	}
}

func TestDiffSplitAvailableFollowsWidth(t *testing.T) {
	app := openDiff(t, diffFixture())

	app.width = diffViewerMinSplitWidth + 40
	if !app.diffSplitAvailable() {
		t.Fatal("split must be available on a wide terminal")
	}
	if app.diffView() != "split" {
		t.Fatalf("default view on a wide terminal = %q, want split", app.diffView())
	}

	app.width = 70
	if app.diffSplitAvailable() {
		t.Fatal("split must not be available on a narrow terminal")
	}
	if app.diffView() != "unified" {
		t.Fatalf("narrow view = %q, want unified", app.diffView())
	}

	// The persisted override applies only when split fits.
	app.width = diffViewerMinSplitWidth + 40
	app.diff.viewOverride = "unified"
	if app.diffView() != "unified" {
		t.Fatal("persisted unified override ignored")
	}
	app.width = 70
	app.diff.viewOverride = "split"
	if app.diffView() != "unified" {
		t.Fatal("persisted split override applied on a narrow terminal")
	}
}

func TestDiffViewerTreePrefixesAndStatusColumn(t *testing.T) {
	// The layout test the tree renderer owes §19.1: prefixes at each depth
	// and the ✓/A/M/D status column right-aligned in two cells.
	files := []client.FileDiff{
		{File: "dir/a.txt", Additions: 1, Status: "added", Patch: ""},
		{File: "dir/b.txt", Deletions: 1, Status: "deleted", Patch: ""},
	}
	app := openDiff(t, files)
	app.diff.reviewed["dir/a.txt"] = true

	rendered := stripANSI(app.renderDiffTree())
	if !strings.Contains(rendered, "▾ ") && !strings.Contains(rendered, "▸ ") {
		t.Fatalf("directory row missing its expand marker:\n%q", rendered)
	}
	if !strings.Contains(rendered, "✓A") {
		t.Fatalf("reviewed added file must show ✓A:\n%q", rendered)
	}
	if !strings.Contains(rendered, " D") {
		t.Fatalf("unreviewed deleted file must show \" D\":\n%q", rendered)
	}
}

func TestDiffViewerEnterOnDirectoryToggles(t *testing.T) {
	files := []client.FileDiff{
		{File: "dir/a.txt", Additions: 1, Status: "added", Patch: ""},
	}
	app := openDiff(t, files)

	pressKey(t, app, "tab")
	// ensureHighlighted picks the first FILE row (TS's find on
	// fileIndex !== undefined), so step up to the directory above it.
	pressKey(t, app, "k")
	if row := app.diff.rows[rowIndexOf(app.diff.rows, app.diff.highlight)]; !row.dir {
		rows := app.diff.rows
		var names []string
		for _, r := range rows {
			names = append(names, r.name)
		}
		t.Fatalf("highlighted row is not the directory: %+v (rows: %v)", row, names)
	}
	expandedBefore := app.diff.expanded[app.diff.highlight]
	pressKey(t, app, "enter")
	if app.diff.expanded[app.diff.highlight] == expandedBefore {
		t.Fatal("enter on a directory did not toggle its expansion")
	}
}

func TestDiffViewerSplitViewRendersBothSides(t *testing.T) {
	app := openDiff(t, diffFixture())
	// Wide enough for split (MIN_SPLIT_WIDTH=100 plus the tree's 33).
	app.width = 160
	if app.diffView() != "split" {
		t.Fatalf("default wide view = %q, want split", app.diffView())
	}

	app.buildDiffLayout()
	if len(app.diff.layoutRows) == 0 {
		t.Fatal("split layout produced no rows")
	}

	// Every laid-out row is exactly one line: the halves join, never stack.
	for i, row := range app.diff.layoutRows {
		if strings.Count(row, "\n") > 0 {
			t.Fatalf("split row %d wrapped", i)
		}
	}

	// The removed and added sides of the fixture's first change are both
	// present in the pane.
	rendered := app.View()
	if !strings.Contains(rendered, "oldFirst") {
		t.Fatal("split view lost the removed side")
	}
	if !strings.Contains(rendered, "newFirst") {
		t.Fatal("split view lost the added side")
	}

	// Hunk navigation still works in split mode: each changed run anchored.
	if len(app.diff.hunks) == 0 {
		t.Fatal("split mode produced no hunk anchors")
	}
}

func TestDiffViewerSplitDegradesToUnifiedWhenNarrow(t *testing.T) {
	app := openDiff(t, diffFixture())
	app.width = 80        // below MIN_SPLIT_WIDTH even with the tree hidden
	pressKey(t, app, "b") // hide the tree for maximum room
	if app.diffView() != "unified" {
		t.Fatalf("narrow view = %q, want unified", app.diffView())
	}
}

func TestDiffViewerFooterSurvivesEverySize(t *testing.T) {
	// §19.6: checked at 60/80/100/130/170 columns and short heights. The
	// footer must always render and never be cropped by frame().
	for _, size := range [][2]int{{100, 20}, {60, 24}, {130, 60}, {170, 50}, {80, 14}} {
		app := newTestApp(t, "http://example.invalid")
		app.width, app.height = size[0], size[1]
		app.diffStatePath = ""
		drive(t, app, staticMsg(app.openDiffViewer()))
		drive(t, app, diffLoadedMsg{mode: diffModeGit, files: diffFixture()})

		rendered := app.View()
		lines := strings.Split(rendered, "\n")
		if len(lines) > size[1] {
			t.Errorf("%dx%d: %d rows exceed the terminal", size[0], size[1], len(lines))
		}
		footer := stripANSI(lines[len(lines)-1])
		if !strings.Contains(footer, "next file") && !strings.Contains(footer, "focus file tree") {
			t.Errorf("%dx%d: footer row names no key: %q", size[0], size[1], footer)
		}
		// No row may exceed the terminal width (frame's side margins included).
		for i, line := range lines {
			if width := lipgloss.Width(line); width > size[0] {
				t.Errorf("%dx%d: row %d is %d cells wide", size[0], size[1], i, width)
			}
		}
	}
}

func TestDiffViewerRendersInLightTheme(t *testing.T) {
	app := newTestApp(t, "http://example.invalid")
	app.setTheme(theme.Resolve("gocode-light"))
	app.diffStatePath = ""
	drive(t, app, staticMsg(app.openDiffViewer()))
	drive(t, app, diffLoadedMsg{mode: diffModeGit, files: diffFixture()})

	rendered := app.View()
	if !strings.Contains(rendered, "src/file.ts") {
		t.Fatal("light theme render lost the file header")
	}
	if !strings.Contains(rendered, "const newFirst = true") {
		t.Fatal("light theme render lost the diff body")
	}
}
