package tui

// diffviewer.go ports the TS diff viewer plugin
// (packages/tui/src/feature-plugins/system/diff-viewer.tsx and
// diff-viewer-file-tree.tsx) onto this port's rendering model: a full-screen
// route, not a dialog — it owns the keyboard while open, and View() renders
// one string through lipgloss the way every other route does.
//
// Layout mirrors the TS route: a header row, a body split between the file
// tree (fixed 32 columns, FILE_TREE_WIDTH) and the patch pane, and a footer
// hint row. Three states stay visually distinct (design principle 9):
// loading, failed, and genuinely empty.

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/langazov/gocode-go/internal/diff"
	"github.com/langazov/gocode-go/internal/tui/client"
	"github.com/langazov/gocode-go/internal/tui/theme"
)

// Geometry and behavior constants, cited from the TS source.
const (
	// diffViewerTreeWidth is FILE_TREE_WIDTH = 32.
	diffViewerTreeWidth = 32
	// diffViewerMinSplitWidth is MIN_SPLIT_WIDTH = 100: below this the
	// patch pane is too narrow for a two-column diff and the view forces
	// unified (TS `splitAvailable`).
	diffViewerMinSplitWidth = 100
	// diffViewerContextLines is VCS_DIFF_CONTEXT_LINES = 12, the context
	// window the TS viewer asks the server for.
	diffViewerContextLines = 12
	// diffViewerTreePage is how far the tree's page keys move: TS's
	// focusRunner moves the file selection by 8.
	diffViewerTreePage = 8
	// diffViewerStatusWidth is FILE_TREE_STATUS_WIDTH = 2: the ✓ + A/M/D
	// column right-aligned in the tree.
	diffViewerStatusWidth = 2
)

// diffFocus is the TS DiffViewerFocus: which pane owns j/k and the wheel.
type diffFocus int

const (
	diffFocusTree diffFocus = iota
	diffFocusPatches
)

// diffMode is the TS DiffMode: which diff source is loaded. "last-turn" is
// not ported (no snapshot system — see the package comment in diffstate.go
// and documentation/10-development.md's divergences table).
type diffMode string

const (
	diffModeGit    diffMode = "git"
	diffModeBranch diffMode = "branch"
)

// diffModeLabel ports diffSourceLabel.
func diffModeLabel(mode diffMode) string {
	if mode == diffModeBranch {
		return "main branch"
	}
	return "working tree"
}

// diffHunk is one hunk anchor for [ / ] navigation, porting the TS
// jumpRelativeHunk model: each hunk's absolute row in the pane's layout.
type diffHunk struct {
	fileIndex int
	hunkIndex int
	row       int // row in the pane's laid-out content
}

// diffViewer is the route's state. It lives on App for as long as the route
// is open (a.diff != nil) and is created by openDiffViewer.
type diffViewer struct {
	mode    diffMode
	loading bool
	err     string // distinguishes failed from empty
	files   []client.FileDiff
	parsed  [][]diff.File // per-file Parse cache, indexed like files
	vcsInfo *client.VcsInfo

	focus         diffFocus
	tree          diffFileTree
	rows          []diffTreeRow // flattened rows for the current expansion
	expanded      map[int]bool
	highlight     int // tree row id; -1 none
	lastHighlight int
	selected      int // file index; -1 none (TS selectedFileIndex)
	active        int // TS activePatchFileIndex
	reviewed      map[string]bool
	single        bool

	// scroll is the patch pane's first visible content row (TS scrollTop).
	scroll int
	// treeScroll is the file tree's first visible row.
	treeScroll int
	// hunks are the pane's hunk anchors, rebuilt with the layout.
	hunks        []diffHunk
	selectedHunk int // index into hunks; -1 none

	// layoutRows is the pane's laid-out rows (per visible file: header,
	// separator, diff rows). Render and navigation both read it, so a hunk
	// anchor can never drift from what is on screen.
	layoutRows []string
	// layoutRowFile maps each laid-out row to its file index (-1 for
	// separators and the filler).
	layoutRowFile []int
	// layoutRowHunk maps each laid-out row to its hunk index within the
	// file (-1 when the row is not a hunk header).
	layoutRowHunk []int
	// layoutKey is the signature of every input buildDiffLayout renders
	// from. Scrolling is deliberately not in it: the layout is content, the
	// scroll is a window over it, and rebuilding thousands of styled rows
	// on every wheel notch is what made the viewer feel like it was
	// swimming (each notch re-rendered every row of every file, twice).
	layoutKey string
	// layoutBuilds counts layout rebuilds; tests assert wheel bursts do not
	// multiply it.
	layoutBuilds int
	// fileOrder is every file's index in full tree order (TS
	// patchFileIndexes: orderedPatchFileIndexes(flattenFileTree(fileTree()))
	// — the whole tree, NOT the expansion-filtered rows, so collapsing a
	// directory never hides its files from the pane). Fixed when the diff
	// lands; the pane and n/p both walk it.
	fileOrder []int
	// reviewedVersion bumps on every reviewed toggle so the layout key can
	// cheaply include the flag set without hashing it.
	reviewedVersion int
	// loadSeq distinguishes successive diff payloads with identical
	// dimensions (a refetch that lands the same shape must still rebuild).
	loadSeq int
	// filler is the blank rows that keep the last file's bottom aligned
	// (TS patchFillerHeight).
	filler int

	// hits record the last render's clickable spans in absolute screen
	// cells, per §13: treeHits[i] is the file index (-1 for a directory)
	// tree row i selects; fileHeaderHits[row] is the file index whose
	// header that pane row belongs to.
	treeHits       []int
	fileHeaderHits map[int]int

	// prefs is the persisted preference state (diffstate.go), held here so
	// toggles can write through immediately.
	prefs     diffState
	prefsPath string
	// treeEnabled is the raw toggle; whether the pane shows is
	// showDiffViewerFileTree(treeEnabled, len(files)).
	treeEnabled bool
	// viewOverride is the persisted view choice ("split"/"unified"), "" =
	// follow the width-derived default (TS viewOverride/defaultView).
	viewOverride string
}

// diffLoadedMsg carries a fetched diff, keyed by mode so a stale response
// for a source the user has already switched away from is dropped.
type diffLoadedMsg struct {
	mode  diffMode
	files []client.FileDiff
	err   error
}

// diffVcsInfoMsg carries the repository info the source-switch dialog gates
// the "Main branch" option on.
type diffVcsInfoMsg struct {
	info *client.VcsInfo
	err  error
}

// openDiffViewer opens the route from the current view and starts the fetch.
// It opens synchronously in the loading state and refreshes in the
// background (the §9.5 rule), so the keypress is never waiting on HTTP.
func (a *App) openDiffViewer() tea.Cmd {
	state := loadDiffState(a.diffStatePath)
	d := &diffViewer{
		mode:          diffModeGit,
		loading:       true,
		focus:         diffFocusPatches,
		highlight:     -1,
		lastHighlight: -1,
		selected:      -1,
		active:        -1,
		reviewed:      map[string]bool{},
		selectedHunk:  -1,
		expanded:      map[int]bool{},
		treeEnabled:   state.showFileTreeDefault(),
		single:        state.SinglePatch,
		viewOverride:  func() string { v, _ := state.storedView(); return v }(),
		prefs:         state,
		prefsPath:     a.diffStatePath,
	}
	a.diff = d
	a.diffReturn = a.view
	// TS closes any open dialog before navigating (api.ui.dialog.clear()).
	a.closeOverlay()
	return tea.Batch(a.loadDiffCmd(d.mode), a.loadVcsInfoCmd())
}

// closeDiffViewer restores the view the route opened from (TS diff.close's
// returnRoute).
func (a *App) closeDiffViewer() {
	a.diff = nil
	a.view = a.diffReturn
}

// loadDiffCmd fetches the diff for a mode off the Elm loop.
func (a *App) loadDiffCmd(mode diffMode) tea.Cmd {
	c, ctx := a.client, a.ctx
	return func() tea.Msg {
		files, err := c.VcsDiff(ctx, string(mode), diffViewerContextLines)
		return diffLoadedMsg{mode: mode, files: files, err: err}
	}
}

// loadVcsInfoCmd fetches repository info for the source dialog's gating.
func (a *App) loadVcsInfoCmd() tea.Cmd {
	c, ctx := a.client, a.ctx
	return func() tea.Msg {
		info, err := c.Vcs(ctx)
		return diffVcsInfoMsg{info: info, err: err}
	}
}

// handleDiffResult folds a fetch into the viewer. A response for a mode the
// viewer has since left is dropped, exactly like the TS createResource
// keyed on diffInput.
func (a *App) handleDiffResult(msg diffLoadedMsg) tea.Cmd {
	d := a.diff
	if d == nil || msg.mode != d.mode {
		return nil
	}
	d.loading = false
	if msg.err != nil {
		d.err = msg.err.Error()
		return nil
	}
	d.err = ""
	d.files = msg.files
	d.loadSeq++
	d.parsed = make([][]diff.File, len(msg.files))
	items := make([]diffTreeItem, len(msg.files))
	for i, file := range msg.files {
		items[i] = diffTreeItem{File: file.File, Status: file.Status}
		d.parsed[i] = diff.Parse(file.Patch)
	}
	d.tree = buildDiffFileTree(items)
	// TS resets the whole selection state whenever the diff input changes.
	d.expanded = allExpandedDiffTreeDirs(d.tree)
	d.highlight = -1
	d.lastHighlight = -1
	d.active = -1
	d.selected = -1
	d.selectedHunk = -1
	d.reviewed = map[string]bool{}
	d.reviewedVersion = 0
	d.scroll = 0
	d.treeScroll = 0
	d.rows = flattenDiffFileTree(d.tree, d.expanded)
	// fileOrder is every file in full tree order (TS patchFileIndexes), the
	// pane's spine: expansion changes only the tree's rows, never this.
	d.fileOrder = orderedDiffPatchFileIndexes(flattenDiffFileTree(d.tree, nil))
	d.layoutKey = "" // force one rebuild against the new payload
	return nil
}

// handleDiffVcsInfo folds the repository info in; a failure leaves the
// branch source simply unavailable rather than erroring the viewer.
func (a *App) handleDiffVcsInfo(msg diffVcsInfoMsg) tea.Cmd {
	if a.diff == nil {
		return nil
	}
	if msg.err == nil {
		a.diff.vcsInfo = msg.info
	}
	return nil
}

// showTree reports whether the file tree pane renders (TS showFileTree).
func (d *diffViewer) showTree() bool {
	return showDiffViewerFileTree(d.treeEnabled, len(d.files))
}

// paneWidth is the patch pane's total width: the terminal minus the tree
// (33 columns with its separator) and the frame margins (TS patchPaneWidth).
func (a *App) diffPaneWidth() int {
	width := a.width
	if a.diff.showTree() {
		width -= diffViewerTreeWidth + 1
	}
	return width - 4
}

// paneInterior is the pane's content width: the total less the frame's side
// margins already accounted for, and the left border column when the tree
// provides a visual edge (TS patchLeftBorder).
func (a *App) diffPaneInterior() int {
	room := a.diffPaneWidth() - 2
	if !a.diff.showTree() {
		room = a.diffPaneWidth() - 4
	}
	return max(4, room)
}

// splitAvailable ports TS splitAvailable: the pane must be wide enough that
// two columns still read as code.
func (a *App) diffSplitAvailable() bool {
	return a.diffPaneWidth() >= diffViewerMinSplitWidth
}

// diffView is the effective view: the persisted override, else the width
// default (split when it fits, unified otherwise — TS defaultView).
func (a *App) diffView() string {
	if !a.diffSplitAvailable() {
		return "unified"
	}
	if a.diff.viewOverride != "" {
		return a.diff.viewOverride
	}
	return "split"
}

// ---------------------------------------------------------------------------
// Layout: one pass, shared by render and navigation
// ---------------------------------------------------------------------------

// buildDiffLayout lays out the pane's content rows for the visible files.
// Both the renderer and the hunk navigation read the result, so the anchors
// can never disagree with the screen (§13's same-pass rule).
func (a *App) buildDiffLayout() {
	d := a.diff

	// The layout is a pure function of these inputs. Anything else — the
	// scroll above all — is a window over the result, not an input, so a
	// wheel burst reuses one layout instead of re-rendering every row per
	// notch. (Review flags enter through reviewedVersion: counting toggles
	// is enough because any change to the set changes the count.)
	key := fmt.Sprintf("v:%s w:%d r:%d s:%v u:%d f:%d n:%d sq:%d",
		a.diffView(), a.diffPaneInterior(), d.reviewedVersion, d.single,
		d.selected, d.active, len(d.files), d.loadSeq)
	if key == d.layoutKey {
		return
	}
	d.layoutKey = key
	d.layoutBuilds++

	visible := d.visibleFiles()
	d.layoutRows = d.layoutRows[:0]
	d.layoutRowFile = d.layoutRowFile[:0]
	d.layoutRowHunk = d.layoutRowHunk[:0]
	d.hunks = d.hunks[:0]
	d.filler = 0
	d.fileHeaderHits = map[int]int{}

	if len(visible) == 0 {
		return
	}

	// The gutter is sized by the largest line number across every visible
	// file, so it does not jitter between files (gutterWidth's rule).
	largest := 0
	for _, files := range d.parsed {
		for _, file := range files {
			for _, line := range file.Lines {
				largest = max(largest, line.OldLine, line.NewLine)
			}
		}
	}
	width := len(integerDigits(largest))
	if width < 2 {
		width = 2
	}

	room := a.diffPaneInterior()
	styles := a.styles()
	first := true
	for _, entry := range visible {
		file := d.files[entry]
		parsedFiles := d.parsed[entry]
		var parsed diff.File
		if len(parsedFiles) > 0 {
			parsed = parsedFiles[0]
		}

		if !first {
			d.pushLayoutRow(styles.Muted.Render(strings.Repeat("─", room)), -1, -1)
		}
		first = false

		// File header: name left, +N -N right (TS patch header row). A
		// reviewed file mutes the whole row.
		reviewed := d.reviewed[file.File]
		nameStyle := a.onPanel(a.theme.Text, false)
		addStyle := a.onPanel(a.theme.Success, false)
		delStyle := a.onPanel(a.theme.Error, false)
		if reviewed {
			muted := a.onPanel(a.theme.TextMuted, false)
			nameStyle, addStyle, delStyle = muted, muted, muted
		}
		stats := addStyle.Render("+"+integerDigits(file.Additions)) + " " +
			delStyle.Render("-"+integerDigits(file.Deletions))
		name := truncateRunes(file.File, max(4, room-lipgloss.Width(stats)-1))
		headerRow := splitRow(room, nameStyle.Render(name), stats, 2)
		d.fileHeaderHits[len(d.layoutRows)] = entry
		d.pushLayoutRow(renderLines(nameStyle, headerRow), entry, -1)

		// Diff rows. An unparseable or capped patch renders the TS
		// fallback line instead of nothing.
		if len(parsed.Lines) == 0 {
			d.pushLayoutRow(a.onPanelMuted("No patch available for this file."), entry, -1)
			continue
		}
		if a.diffView() == "split" {
			// Split: pair old/new rows and render the two sides side by
			// side. Hunk anchors survive: the first row of each contiguous
			// changed run is a [ / ] target.
			previousChanged := false
			for _, pair := range diff.PairRows(parsed) {
				changed := pair.OldKind == diff.LineRemoved || pair.NewKind == diff.LineAdded
				hunk := -1
				if changed && !previousChanged {
					hunk = len(d.hunks)
					d.hunks = append(d.hunks, diffHunk{fileIndex: entry, hunkIndex: hunk, row: len(d.layoutRows)})
				}
				previousChanged = changed
				d.pushLayoutRow(a.diffPairRow(pair, width, room, reviewed), entry, hunk)
			}
			continue
		}
		panel := a.theme.BackgroundPanel
		for _, line := range parsed.Lines {
			row := a.diffRow(line, width, room, panel, reviewed)
			hunk := -1
			if line.Kind == diff.LineHunk {
				hunk = len(d.hunks)
				d.hunks = append(d.hunks, diffHunk{fileIndex: entry, hunkIndex: hunk, row: len(d.layoutRows)})
			}
			d.pushLayoutRow(row, entry, hunk)
		}
	}
}

// pushLayoutRow appends one content row and its file/hunk attribution.
func (d *diffViewer) pushLayoutRow(row string, fileIndex, hunkIndex int) {
	d.layoutRows = append(d.layoutRows, row)
	d.layoutRowFile = append(d.layoutRowFile, fileIndex)
	d.layoutRowHunk = append(d.layoutRowHunk, hunkIndex)
}

// diffRow renders one diff line with its gutter, porting editDiffBlock's
// row conventions: added Success, removed Error, context TextMuted, hunk
// headers muted, every row truncated to the pane's interior so a diff row
// never wraps (a wrapped "-" row reads as more removal, not a continuation).
func (a *App) diffRow(line diff.Line, numberWidth, room int, panel color.Color, reviewed bool) string {
	muted := a.onPanel(a.theme.TextMuted, false)
	switch line.Kind {
	case diff.LineHunk, diff.LineMeta:
		return muted.Render(ansi.Truncate(line.Content, room, "…"))
	case diff.LineAdded:
		style := lipgloss.NewStyle().Foreground(a.theme.Success).Background(panel)
		if reviewed {
			style = lipgloss.NewStyle().Foreground(a.theme.TextMuted).Background(panel)
		}
		return renderLines(style, ansi.Truncate(gutter(0, line.NewLine, numberWidth)+"+ "+line.Content, room, "…"))
	case diff.LineRemoved:
		style := lipgloss.NewStyle().Foreground(a.theme.Error).Background(panel)
		if reviewed {
			style = lipgloss.NewStyle().Foreground(a.theme.TextMuted).Background(panel)
		}
		return renderLines(style, ansi.Truncate(gutter(line.OldLine, 0, numberWidth)+"- "+line.Content, room, "…"))
	default:
		style := lipgloss.NewStyle().Foreground(a.theme.TextMuted).Background(panel)
		return renderLines(style, ansi.Truncate(gutter(line.OldLine, line.NewLine, numberWidth)+"  "+line.Content, room, "…"))
	}
}

// visibleFiles lists the file indexes the pane shows: every file, or just
// the single-patch selection (TS visiblePatchFiles). It reads fileOrder,
// which is fixed when the diff lands — NOT the expansion-filtered tree rows
// — so collapsing a directory never hides its files from the pane. The TS
// source computes patchFileIndexes from flattenFileTree(fileTree()), the
// whole tree; this port had wrongly fed it the filtered rows, which both
// diverged and made the layout depend on tree state.
func (d *diffViewer) visibleFiles() []int {
	if !d.single {
		return d.fileOrder
	}
	fileIndex := singleDiffPatchFileIndex(d.selected, d.active, d.currentFileIndex(), d.firstFileIndex())
	if fileIndex < 0 || fileIndex >= len(d.files) {
		return nil
	}
	return []int{fileIndex}
}

// currentFileIndex ports currentPatchFileIndex: the file whose content is
// at the top of the viewport.
func (d *diffViewer) currentFileIndex() int {
	viewportRow := d.scroll
	for i := len(d.layoutRowFile) - 1; i >= 0; i-- {
		if i <= viewportRow && d.layoutRowFile[i] >= 0 {
			return d.layoutRowFile[i]
		}
	}
	if len(d.layoutRowFile) > 0 {
		return d.layoutRowFile[0]
	}
	return -1
}

// firstFileIndex is the first file in tree order (TS firstPatchFileIndex).
func (d *diffViewer) firstFileIndex() int {
	if len(d.fileOrder) > 0 {
		return d.fileOrder[0]
	}
	return -1
}

// diffPairRow renders one split-view row: the old side's gutter and line,
// the new side's, joined side by side. Each half truncates to half the
// pane's interior so the row never wraps (the same rule as diffRow).
func (a *App) diffPairRow(pair diff.PairRow, numberWidth, room int, reviewed bool) string {
	half := max(8, (room-3)/2)
	panel := a.theme.BackgroundPanel

	oldStyle := lipgloss.NewStyle().Foreground(a.theme.TextMuted).Background(panel)
	if pair.OldKind == diff.LineRemoved {
		oldStyle = lipgloss.NewStyle().Foreground(a.theme.Error).Background(panel)
	}
	newStyle := lipgloss.NewStyle().Foreground(a.theme.TextMuted).Background(panel)
	if pair.NewKind == diff.LineAdded {
		newStyle = lipgloss.NewStyle().Foreground(a.theme.Success).Background(panel)
	}
	if reviewed {
		muted := lipgloss.NewStyle().Foreground(a.theme.TextMuted).Background(panel)
		oldStyle, newStyle = muted, muted
	}

	oldSign := "  "
	if pair.OldKind == diff.LineRemoved {
		oldSign = "- "
	}
	newSign := "  "
	if pair.NewKind == diff.LineAdded {
		newSign = "+ "
	}

	oldHalf := ansi.Truncate(
		gutter(pair.OldLine, 0, numberWidth)+oldSign+pair.Old, half, "…")
	newHalf := ansi.Truncate(
		gutter(0, pair.NewLine, numberWidth)+newSign+pair.New, half, "…")

	// Right-pad the old half so the new half starts on its own column and
	// the two sides never touch.
	oldHalf = oldHalf + strings.Repeat(" ", max(0, half-ansi.StringWidth(oldHalf)))
	return renderLines(oldStyle, oldHalf) + " " + renderLines(newStyle, ansi.Truncate(newHalf, half, "…"))
}

// ---------------------------------------------------------------------------
// Pane body: windowed render
// ---------------------------------------------------------------------------

// diffBodyHeight is the pane's row budget: the terminal less the header
// row, the footer hint row, and the frame's top/bottom margins.
func (a *App) diffBodyHeight() int {
	// The route's fixed chrome: the header row, the blank under it, the
	// blank above the footer, and the footer row itself — four rows the
	// body may not spend (§19.5: nothing may crop the hint row).
	return max(1, a.height-4)
}

// renderDiffPane renders the windowed patch pane: only the rows in
// [scroll, scroll+height) (§16.5), with the ↑/↓ indicator naming what is
// hidden when the content is taller than the window.
func (a *App) renderDiffPane() string {
	d := a.diff
	styles := a.styles()
	if d.loading {
		return styles.Muted.Render("Loading diff…")
	}
	if d.err != "" {
		return styles.Error.Render("Failed to load diff")
	}
	if len(d.files) == 0 {
		return styles.Muted.Render("No diff!")
	}

	a.buildDiffLayout()
	total := len(d.layoutRows)
	height := a.diffBodyHeight()
	if total <= height {
		// Short content keeps the pane the full height by padding below it,
		// so the tree and pane blocks stay vertically aligned. filler-1
		// blank lines join into filler rows; zero filler appends nothing.
		d.filler = height - total
		rows := append([]string{}, d.layoutRows...)
		if d.filler > 1 {
			rows = append(rows, strings.Repeat("\n", d.filler-1))
		}
		return strings.Join(rows, "\n")
	}

	// Clamp the scroll (end settles on the last windowful, like listBody).
	if d.scroll > total-height {
		d.scroll = total - height
	}
	if d.scroll < 0 {
		d.scroll = 0
	}
	hidden := height - 1 // one budget row for the indicator
	end := d.scroll + hidden
	if end > total {
		end = total
	}
	rows := append([]string{}, d.layoutRows[d.scroll:end]...)
	if d.scroll > 0 {
		rows = append(rows, styles.Muted.Render(
			fmt.Sprintf("  ↑ %d more lines", d.scroll)))
	} else {
		rows = append(rows, styles.Muted.Render(
			fmt.Sprintf("  ↓ %d more lines", total-end)))
	}
	return strings.Join(rows, "\n")
}

// ---------------------------------------------------------------------------
// File tree render
// ---------------------------------------------------------------------------

// renderDiffTree renders the file tree pane, porting
// diff-viewer-file-tree.tsx: box-drawing prefixes, the ✓/A/M/D status
// column, the highlight fill, and scroll-into-view.
func (a *App) renderDiffTree() string {
	d := a.diff
	if len(d.rows) == 0 {
		return a.onPanel(a.theme.Text, false).Render("No files")
	}

	height := a.diffBodyHeight()
	// scrollFileTreeRowIntoView: above the window jumps to the row, below
	// it lands the row on the last line.
	if index := rowIndexOf(d.rows, d.highlight); index != -1 {
		if index < d.treeScroll {
			d.treeScroll = index
		} else if index >= d.treeScroll+height {
			d.treeScroll = index - height + 1
		}
	}
	if maxScroll := len(d.rows) - height; d.treeScroll > maxScroll {
		d.treeScroll = max(maxScroll, 0)
	}
	if d.treeScroll < 0 {
		d.treeScroll = 0
	}

	end := min(len(d.rows), d.treeScroll+height)
	visible := d.rows[d.treeScroll:end]
	d.treeHits = make([]int, len(visible))
	for i := range d.treeHits {
		d.treeHits[i] = visible[i].fileIndex
	}

	// The tree's connector glyphs fade toward the panel they sit on
	// (TS: tint(text, background, 0.75)); a fade must name its surface.
	fadedPrefix := lipgloss.NewStyle().
		Foreground(theme.FadeColor(a.theme.BackgroundPanel, a.theme.TextMuted, 0.75)).
		Background(a.theme.BackgroundPanel)
	rows := make([]string, 0, len(visible))
	for i, row := range visible {
		highlighted := d.focus == diffFocusTree && d.highlight == row.id
		rowIndex := d.treeScroll + i

		prefix := diffTreeRowPrefix(d.rows, rowIndex, row, d.expanded)
		status := diffTreeRowStatus(row, d.files, d.reviewed)

		prefixStyle := fadedPrefix
		nameFg := a.theme.Text
		switch {
		case highlighted:
			prefixStyle = a.onPanel(a.theme.Background, false)
			nameFg = a.theme.Background
		case row.fileIndex >= 0 && row.fileIndex == d.selected:
			nameFg = a.theme.Primary
		case d.reviewed[row.name] || row.dir:
			nameFg = a.theme.TextMuted
		}
		nameStyle := a.onPanel(nameFg, false)
		statusStyle := a.onPanel(a.theme.TextMuted, false)
		if highlighted {
			nameStyle = a.onPanel(a.theme.Background, false)
			statusStyle = a.onPanel(a.theme.Background, false)
		}

		// Name truncates to what remains after the prefix and the status
		// column (TS Locale.truncate with the same budget).
		nameWidth := max(1, diffViewerTreeWidth-diffViewerStatusWidth-lipgloss.Width(prefix)-4)
		name := ansi.Truncate(row.name, nameWidth, "…")
		gap := diffViewerTreeWidth - 4 - lipgloss.Width(prefix) - lipgloss.Width(name) - diffViewerStatusWidth

		line := prefixStyle.Render(prefix) + nameStyle.Render(name) +
			strings.Repeat(" ", max(0, gap)) + statusStyle.Render(status)
		// The highlight belongs to the row box, edge to edge (§9.2).
		if highlighted {
			line = a.onPanel(a.theme.Primary, false).Render(ansi.Truncate(strings.Repeat(" ", 1)+line, diffViewerTreeWidth-2, ""))
		}
		rows = append(rows, line)
	}
	return strings.Join(rows, "\n")
}

// diffTreeRowPrefix ports fileTreeRowPrefix: the │ /   indentation, the
// ├─ / └─ branch, and the ▸ collapsed / ▾ expanded directory marker.
func diffTreeRowPrefix(rows []diffTreeRow, index int, row diffTreeRow, expanded map[int]bool) string {
	var indent strings.Builder
	for depth := 0; depth < row.depth; depth++ {
		if depth == 0 && !hasLaterSibling(rows, 0, 0) {
			indent.WriteString(" ")
			continue
		}
		if hasLaterSibling(rows, index, depth) {
			indent.WriteString("│  ")
		} else {
			indent.WriteString("   ")
		}
	}
	topRoot := index == 0 && row.depth == 0
	branch := "└─ "
	if topRoot {
		branch = " "
	} else if hasLaterSibling(rows, index, row.depth) {
		branch = "├─ "
	}
	marker := ""
	if row.dir {
		if expanded[row.id] {
			marker = "▾ "
		} else {
			marker = "▸ "
		}
	}
	return indent.String() + branch + marker
}

// hasLaterSibling ports hasLaterSibling: whether a later row shares this
// depth (so the connector draws │ rather than ending the run).
func hasLaterSibling(rows []diffTreeRow, index, depth int) bool {
	for _, row := range rows[index+1:] {
		if row.depth <= depth {
			return row.depth == depth
		}
	}
	return false
}

// diffTreeRowStatus ports fileTreeRowStatus: "✓M" when reviewed, " M"
// otherwise, right-aligned in the two-cell column.
func diffTreeRowStatus(row diffTreeRow, files []client.FileDiff, reviewed map[string]bool) string {
	if row.fileIndex < 0 || row.fileIndex >= len(files) {
		return ""
	}
	status := files[row.fileIndex].Status
	marker := "?"
	switch status {
	case "modified":
		marker = "M"
	case "added":
		marker = "A"
	case "deleted":
		marker = "D"
	}
	mark := " "
	if reviewed[files[row.fileIndex].File] {
		mark = "✓"
	}
	return mark + marker
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

// viewDiff renders the route: header, body (tree + pane), footer hints.
func (a *App) viewDiff() string {
	d := a.diff
	styles := a.styles()

	// Header: "Diff <source>" left, "N files" right (TS header row).
	title := styles.Text.Render("Diff ") + styles.Muted.Render(diffModeLabel(d.mode))
	count := fmt.Sprintf("%d %s", len(d.files), pluralFiles(len(d.files)))
	header := splitRow(a.width-4, title, styles.Muted.Render(count), 2)

	var body string
	if d.showTree() {
		tree := a.renderDiffTree()
		pane := a.renderDiffPane()
		treeBlock := a.diffTreePanel().Render(tree)
		body = lipgloss.JoinHorizontal(lipgloss.Top, treeBlock, pane)
	} else {
		body = a.renderDiffPane()
	}

	footer := a.diffFooter()
	return a.frame(strings.Join([]string{header, "", body, "", footer}, "\n"))
}

// diffTreePanel is the tree pane's chrome: the ┃ accent in Border and the
// panel fill, sized as a total border-box width (§1).
func (a *App) diffTreePanel() lipgloss.Style {
	return lipgloss.NewStyle().
		Border(splitBorder(), false, false, false, true).
		BorderForeground(a.theme.Border).
		Background(a.theme.BackgroundPanel).
		PaddingTop(0).
		PaddingBottom(0).
		PaddingLeft(1).
		Width(withLeftBorder(diffViewerTreeWidth))
}

// diffFooter renders the hint row: keys in Text, labels in TextMuted,
// dropped from the end when the row does not fit (§8.2's discipline).
func (a *App) diffFooter() string {
	d := a.diff
	pairs := []struct{ key, label string }{}
	if d.showTree() {
		pairs = append(pairs, struct{ key, label string }{"tab", "focus file tree"})
	}
	pairs = append(pairs,
		struct{ key, label string }{"n", "next file"},
		struct{ key, label string }{"]", "next hunk"},
		struct{ key, label string }{"[", "previous hunk"},
		struct{ key, label string }{"p", "previous file"},
		struct{ key, label string }{"d", "switch source"},
		struct{ key, label string }{"m", "mark reviewed"},
		struct{ key, label string }{"v", "view"},
		struct{ key, label string }{"s", "patches"},
		struct{ key, label string }{"b", "file tree"},
		struct{ key, label string }{"?", "all"},
	)
	styles := a.styles()
	segments := make([]string, 0, len(pairs)*2)
	for _, pair := range pairs {
		segments = append(segments,
			styles.Text.Render(pair.key)+" "+styles.Muted.Render(pair.label))
	}
	// Drop from the end until the joined row fits (footer discipline), but
	// keep at least one pair: an empty hint row names no way out, and on a
	// narrow terminal the first hint (which pairs kept first) is the one
	// that matters most.
	joined := func() string { return strings.Join(segments, "  ") }
	for len(segments) > 1 && lipgloss.Width(joined()) > a.width-4 {
		segments = segments[:len(segments)-1]
	}
	return joined()
}

func pluralFiles(count int) string {
	if count == 1 {
		return "file"
	}
	return "files"
}

// integerDigits renders a count or line number. Named rather than inlined
// because every numeric column in the viewer goes through it, and the one
// place it changes (zero-padding, say) should change once.
func integerDigits(value int) string {
	return strconv.Itoa(value)
}

// ---------------------------------------------------------------------------
// Navigation
// ---------------------------------------------------------------------------

// selectDiffFile ports selectPatchFile: reveal in the tree, set active and
// selected.
func (a *App) selectDiffFile(fileIndex int) {
	d := a.diff
	if selection, ok := diffFileSelectionFor(d.tree, fileIndex); ok {
		for node := range selection.expandedNodes {
			d.expanded[node] = true
		}
		d.highlight = selection.highlightedNode
		d.lastHighlight = selection.highlightedNode
		d.rows = flattenDiffFileTree(d.tree, d.expanded)
	}
	d.active = fileIndex
	d.selected = fileIndex
}

// jumpToDiffFile scrolls the pane so the file's header is at the top (TS
// scrollToFileIndex).
func (a *App) jumpToDiffFile(fileIndex int) {
	if fileIndex < 0 {
		return
	}
	a.selectDiffFile(fileIndex)
	a.buildDiffLayout()
	for row, mapped := range a.diff.layoutRowFile {
		if mapped == fileIndex {
			// The separator above a file's header belongs to the visual
			// block; land one row above the header when one exists.
			target := row
			if row > 0 && a.diff.layoutRowFile[row-1] == -1 {
				target = row - 1
			}
			a.diff.scroll = target
			return
		}
	}
}

// jumpRelativeDiffFile ports jumpRelativePatchFile: step through the files
// in tree order.
func (a *App) jumpRelativeDiffFile(offset int) {
	d := a.diff
	d.selectedHunk = -1
	indexes := d.fileOrder
	current := d.selected
	if current < 0 {
		current = d.active
	}
	next := moveDiffPatchFileIndex(indexes, current, offset)
	if next < 0 {
		return
	}
	if d.single {
		a.selectDiffFile(next)
		d.scroll = 0
		return
	}
	a.jumpToDiffFile(next)
}

// jumpRelativeDiffHunk ports jumpRelativeHunk: walk the hunk anchors built
// by the same layout the renderer drew, keeping the selection sticky (the
// TS test's ] ] [ sequence returns to the recorded scroll exactly).
func (a *App) jumpRelativeDiffHunk(offset int) {
	d := a.diff
	// The layout is the renderer's; navigation may run before any render
	// (a key arriving in the same frame the data landed), so make sure the
	// anchors exist before walking them.
	a.buildDiffLayout()
	if len(d.hunks) == 0 {
		return
	}
	selected := d.selectedHunk
	if selected >= 0 && selected < len(d.hunks) {
		next := selected + offset
		if next < 0 || next >= len(d.hunks) {
			return
		}
		d.selectedHunk = next
	} else {
		// No sticky selection: find the first hunk past the scroll in the
		// requested direction.
		var found = -1
		if offset > 0 {
			for i, hunk := range d.hunks {
				if hunk.row > d.scroll {
					found = i
					break
				}
			}
		} else {
			for i := len(d.hunks) - 1; i >= 0; i-- {
				if d.hunks[i].row < d.scroll {
					found = i
					break
				}
			}
		}
		if found == -1 {
			return
		}
		d.selectedHunk = found
	}
	hunk := d.hunks[d.selectedHunk]
	a.selectDiffFile(hunk.fileIndex)
	// TS scrolls to the hunk's content row exactly; the hunk header carries
	// its own line numbers, so nothing above it needs to stay visible.
	d.scroll = hunk.row
}

// toggleDiffReviewed ports toggleSelectedFileReviewed: the focused pane
// decides which file's flag flips.
func (a *App) toggleDiffReviewed() {
	d := a.diff
	fileIndex := d.selected
	if d.focus == diffFocusTree {
		if index := rowIndexOf(d.rows, d.highlight); index != -1 {
			fileIndex = d.rows[index].fileIndex
		}
	} else if fileIndex < 0 {
		fileIndex = d.active
		if fileIndex < 0 {
			fileIndex = d.currentFileIndex()
		}
	}
	if fileIndex < 0 || fileIndex >= len(d.files) {
		return
	}
	file := d.files[fileIndex].File
	if d.reviewed[file] {
		delete(d.reviewed, file)
	} else {
		d.reviewed[file] = true
	}
	// The toggle changes rendered colors, so the layout must rebuild; a
	// count is enough to key it (any change to the set changes the count).
	d.reviewedVersion++
}

// saveDiffPrefs writes the viewer's current toggles through to the state
// file (TS kv.set on each toggle).
func (d *diffViewer) saveDiffPrefs() {
	d.prefs.FileTree = &d.treeEnabled
	d.prefs.SinglePatch = d.single
	d.prefs.View = d.viewOverride
	saveDiffState(d.prefsPath, d.prefs)
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

// handleDiffKey owns the keyboard while the route is open, porting the TS
// command table. A dialog (the source picker, the help sheet) still wins:
// the overlay ladder runs before this, exactly like the TS viewer's dialogs.
func (a *App) handleDiffKey(msg tea.KeyMsg) tea.Cmd {
	d := a.diff
	key := msg.String()

	// q/esc close from anywhere in the route (diff.close). esc never
	// reaches the global interrupt arming here.
	if key == "q" || key == "esc" || key == "escape" {
		a.closeDiffViewer()
		return nil
	}

	switch key {
	case "j", "down":
		return a.diffMove(1)
	case "k", "up":
		return a.diffMove(-1)
	case "pagedown", "ctrl+f":
		return a.diffPage(1)
	case "pageup", "ctrl+b":
		return a.diffPage(-1)
	case "enter", " ":
		if d.focus == diffFocusTree {
			a.diffToggleTreeRow()
		}
		return nil
	case "right":
		if d.focus == diffFocusTree {
			a.diffExpandTreeRow()
		}
		return nil
	case "left":
		if d.focus == diffFocusTree {
			a.diffCollapseTreeRow()
		}
		return nil
	case "E":
		if d.focus == diffFocusTree {
			d.expanded = allExpandedDiffTreeDirs(d.tree)
			d.rows = flattenDiffFileTree(d.tree, d.expanded)
		}
		return nil
	case "]":
		a.jumpRelativeDiffHunk(1)
		return nil
	case "[":
		a.jumpRelativeDiffHunk(-1)
		return nil
	case "n":
		a.jumpRelativeDiffFile(1)
		return nil
	case "p":
		a.jumpRelativeDiffFile(-1)
		return nil
	case "m":
		a.toggleDiffReviewed()
		return nil
	case "tab":
		if !d.showTree() {
			return nil
		}
		if d.focus == diffFocusTree {
			d.focus = diffFocusPatches
		} else {
			a.diffEnsureHighlighted()
			d.focus = diffFocusTree
		}
		return nil
	case "b":
		d.treeEnabled = !d.treeEnabled
		if !d.treeEnabled {
			d.focus = diffFocusPatches
		}
		d.saveDiffPrefs()
		return nil
	case "s":
		return a.diffToggleSinglePatch()
	case "d":
		return a.diffSwitchSource()
	case "v":
		if !a.diffSplitAvailable() {
			return nil
		}
		d.selectedHunk = -1
		// Toggle against the EFFECTIVE view, not the stored override: with
		// nothing persisted the effective view is the width default, and
		// the first press must flip away from whatever is on screen.
		if a.diffView() == "split" {
			d.viewOverride = "unified"
		} else {
			d.viewOverride = "split"
		}
		d.saveDiffPrefs()
		return nil
	case "?":
		return a.diffHelp()
	case "g":
		d.scroll = 0
		return nil
	case "G":
		a.buildDiffLayout()
		if height := a.diffBodyHeight(); len(d.layoutRows) > height {
			d.scroll = len(d.layoutRows) - height
		}
		return nil
	}
	return nil
}

// diffMove ports diff.down/diff.up: the focused pane moves.
func (a *App) diffMove(offset int) tea.Cmd {
	d := a.diff
	if d.focus == diffFocusTree {
		d.highlight = moveDiffTreeSelection(d.rows, d.highlight, offset)
		d.lastHighlight = d.highlight
		return nil
	}
	// Patches: scrolling clears the sticky tree selection state, like the
	// TS focusRunner's clearFileTreePatchState.
	d.highlight = -1
	d.active = -1
	d.selectedHunk = -1
	a.diffScrollBy(offset)
	return nil
}

// diffPage ports diff.page.down/up.
func (a *App) diffPage(offset int) tea.Cmd {
	d := a.diff
	if d.focus == diffFocusTree {
		d.highlight = moveDiffTreeSelection(d.rows, d.highlight, offset*diffViewerTreePage)
		d.lastHighlight = d.highlight
		return nil
	}
	d.highlight = -1
	d.active = -1
	d.selectedHunk = -1
	a.diffScrollBy(offset * a.diffBodyHeight())
	return nil
}

// diffScrollBy moves the pane's window. Deliberately layout-free: the
// clamp happens against whatever layout the next render produces (the
// windowed branch clamps again anyway), because building the layout here is
// what made wheel scrolling queue a full re-render per notch.
func (a *App) diffScrollBy(delta int) {
	d := a.diff
	d.scroll += delta
	if d.scroll < 0 {
		d.scroll = 0
	}
}

// diffToggleTreeRow ports diff.toggle on a file row: jump to it.
func (a *App) diffToggleTreeRow() {
	d := a.diff
	index := rowIndexOf(d.rows, d.highlight)
	if index == -1 {
		return
	}
	row := d.rows[index]
	if row.fileIndex >= 0 {
		d.selectedHunk = -1
		a.jumpToDiffFile(row.fileIndex)
		return
	}
	d.expanded = toggleDiffTreeDir(d.tree, d.expanded, row.id)
	d.rows = flattenDiffFileTree(d.tree, d.expanded)
}

// diffExpandTreeRow ports diff.expand: expand a directory, or move into an
// already-expanded one.
func (a *App) diffExpandTreeRow() {
	d := a.diff
	index := rowIndexOf(d.rows, d.highlight)
	if index == -1 {
		return
	}
	row := d.rows[index]
	if row.dir && d.expanded[row.id] {
		d.highlight = moveDiffTreeSelectionToFirstChild(d.rows, row.id)
		d.lastHighlight = d.highlight
		return
	}
	d.expanded = setDiffTreeDirExpanded(d.tree, d.expanded, row.id, true)
	d.rows = flattenDiffFileTree(d.tree, d.expanded)
}

// diffCollapseTreeRow ports diff.collapse: collapse a directory, or move to
// the parent of a file/collapsed row.
func (a *App) diffCollapseTreeRow() {
	d := a.diff
	index := rowIndexOf(d.rows, d.highlight)
	if index == -1 {
		return
	}
	row := d.rows[index]
	if !row.dir || !d.expanded[row.id] {
		d.highlight = moveDiffTreeSelectionToParent(d.rows, row.id)
		d.lastHighlight = d.highlight
		return
	}
	d.expanded = setDiffTreeDirExpanded(d.tree, d.expanded, row.id, false)
	d.rows = flattenDiffFileTree(d.tree, d.expanded)
}

// diffEnsureHighlighted ports ensureHighlightedFileNode: restore the last
// highlight, else the first file row.
func (a *App) diffEnsureHighlighted() {
	d := a.diff
	if d.highlight != -1 && rowIndexOf(d.rows, d.highlight) != -1 {
		return
	}
	next := -1
	if d.lastHighlight != -1 && rowIndexOf(d.rows, d.lastHighlight) != -1 {
		next = d.lastHighlight
	} else {
		for _, row := range d.rows {
			if row.fileIndex >= 0 {
				next = row.id
				break
			}
		}
	}
	d.highlight = next
	if next != -1 {
		d.lastHighlight = next
	}
}

// diffToggleSinglePatch ports diff.single_patch.
func (a *App) diffToggleSinglePatch() tea.Cmd {
	d := a.diff
	d.selectedHunk = -1
	if !d.single {
		// Entering single-patch mode: make sure something is selected.
		if fileIndex := d.currentFileIndex(); fileIndex >= 0 {
			a.selectDiffFile(fileIndex)
		} else if fileIndex := d.firstFileIndex(); fileIndex >= 0 {
			a.selectDiffFile(fileIndex)
		}
		d.single = true
		d.scroll = 0
		d.saveDiffPrefs()
		return nil
	}
	// Leaving: keep the selected file visible in the full pane.
	fileIndex := d.currentFileIndex()
	d.single = false
	d.saveDiffPrefs()
	if fileIndex >= 0 {
		a.jumpToDiffFile(fileIndex)
	}
	return nil
}

// diffSwitchSource opens the source picker as an overlayList (§9.1: a
// picker is a list, not a new dialog kind), with the branch option gated on
// the repository info exactly like the TS switchDiffOptions memo.
func (a *App) diffSwitchSource() tea.Cmd {
	d := a.diff
	items := []overlayItem{
		{label: "Working tree", hint: "Show current git changes", value: string(diffModeGit)},
	}
	if info := d.vcsInfo; info != nil {
		if info.Branch != "" && info.DefaultBranch != "" && info.Branch != info.DefaultBranch {
			items = append(items, overlayItem{
				label: "Main branch", hint: "Show changes compared to main branch", value: string(diffModeBranch),
			})
		}
	}
	a.openList("Switch source", items)
	o := a.overlay
	o.hideFilter = true
	o.current = string(d.mode)
	// The picker navigates rather than picks-and-closes: onActivate leaves
	// the dialog open so the user can see each source, exactly like the TS
	// dialog's onSelect navigate.
	o.onActivate = func(item overlayItem) tea.Cmd {
		mode := diffMode(item.value)
		if a.diff == nil || mode == a.diff.mode {
			return nil
		}
		a.diff.mode = mode
		a.diff.loading = true
		a.diff.files = nil
		a.diff.parsed = nil
		a.diff.scroll = 0
		a.closeOverlay()
		return a.loadDiffCmd(mode)
	}
	return nil
}

// diffHelp opens the shortcut sheet through the help overlay, extended with
// the viewer's rows (an existing kind with content, not a ninth kind).
func (a *App) diffHelp() tea.Cmd {
	a.overlay = &overlay{kind: overlayHelp, title: "Diff shortcuts", size: dialogLarge}
	a.overlay.helpLines = a.diffHelpLines()
	return nil
}

// diffHelpLines ports DiffViewerHelpDialog's rows.
func (a *App) diffHelpLines() []string {
	return []string{
		"q          Close viewer       Quit the diff viewer",
		"tab        Focus file tree    Move focus between the file tree and patch pane",
		"]          Next hunk          Jump to the next diff hunk",
		"[          Previous hunk      Jump to the previous diff hunk",
		"n          Next file          Select the next changed file in file-tree order",
		"p          Previous file      Select the previous changed file in file-tree order",
		"b          Toggle file tree   Show or hide the file tree sidebar",
		"s          Toggle patches     Switch between one selected patch and all patches",
		"d          Switch source      Choose working tree or main-branch changes",
		"v          Toggle view        Switch between split and unified diff layout",
		"E          Expand all folders Open every folder in the file tree",
		"m          Mark reviewed      Toggle reviewed state for the selected file",
		"g / G      Top / bottom       Jump to the first or last row of the pane",
	}
}

// ---------------------------------------------------------------------------
// Mouse
// ---------------------------------------------------------------------------

// diffMouseClick routes a click on the viewer: tree rows select and reveal;
// a file's header jumps to it. Hits were recorded by the same render pass
// that drew them (§13).
func (a *App) diffMouseClick(x, y int) tea.Cmd {
	d := a.diff
	if d == nil {
		return nil
	}
	// Body starts after the header row and its blank separator (frame
	// offset included). The tree occupies its fixed width on the left.
	bodyTop := 2
	if y < bodyTop || y >= a.height-2 {
		return nil
	}
	if d.showTree() && x < diffViewerTreeWidth {
		row := y - bodyTop + d.treeScroll
		if row >= 0 && row < len(d.treeHits) {
			d.focus = diffFocusTree
			if index := rowIndexOf(d.rows, d.rows[min(row, len(d.rows)-1)].id); index != -1 {
				d.highlight = d.rows[index].id
				d.lastHighlight = d.highlight
				if d.rows[index].fileIndex >= 0 {
					d.selectedHunk = -1
					a.jumpToDiffFile(d.rows[index].fileIndex)
				}
			}
		}
		return nil
	}
	if row, ok := d.fileHeaderHits[y-bodyTop+d.scroll]; ok {
		d.selectedHunk = -1
		a.jumpToDiffFile(row)
	}
	return nil
}

// diffMouseWheel scrolls the focused pane.
func (a *App) diffMouseWheel(up bool) tea.Cmd {
	d := a.diff
	if d == nil {
		return nil
	}
	// scroll is the FIRST VISIBLE ROW, so wheel-up (earlier content)
	// decreases it and wheel-down increases it. This was initially
	// inverted — inherited from the timeline's scrollOffset, which counts
	// the opposite thing (lines kept off the bottom) — and scrolled the
	// pane backwards.
	delta := 1
	if up {
		delta = -1
	}
	if d.focus == diffFocusTree {
		d.treeScroll += delta
		if d.treeScroll < 0 {
			d.treeScroll = 0
		}
		return nil
	}
	a.diffScrollBy(delta)
	return nil
}
