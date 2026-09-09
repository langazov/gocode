package tui

import (
	"reflect"
	"testing"
)

// treeFixture mirrors the TS suite's fixtures
// (tui/test/cli/tui/diff-viewer-file-tree.test.tsx): a nested set of
// changed paths exercising shared directories, chain collapse, and ordering.
func treeFixture() []diffTreeItem {
	return []diffTreeItem{
		{File: "src/main.go", Status: "modified"},
		{File: "src/util/path.go", Status: "modified"},
		{File: "src/util/other.go", Status: "added"},
		{File: "README.md", Status: "modified"},
		{File: "internal/deep/nested/file.go", Status: "modified"},
	}
}

func TestBuildDiffFileTreeSharedDirectories(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())

	// src and src/util are shared: two files under src/util produce one
	// "src" node and one "util" node, not four.
	dirs := map[string]int{}
	for _, node := range tree.nodes {
		if node.dir {
			dirs[node.name]++
		}
	}
	if dirs["src"] != 1 || dirs["util"] != 1 {
		t.Fatalf("shared directories duplicated: %v", dirs)
	}
	if dirs["internal"] != 1 || dirs["deep"] != 1 || dirs["nested"] != 1 {
		t.Fatalf("nested chain directories duplicated: %v", dirs)
	}
}

func TestBuildDiffFileTreeOrdersDirectoriesFirst(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	for i, root := range tree.roots {
		if tree.nodes[root].dir {
			continue
		}
		// Once a file root appears, no directory may follow it.
		for _, later := range tree.roots[i+1:] {
			if tree.nodes[later].dir {
				t.Fatalf("file %q sorts before directory root %q", tree.nodes[root].name, tree.nodes[later].name)
			}
		}
	}
}

func TestFlattenDiffFileTreeCollapsesSingleChildChains(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	rows := flattenDiffFileTree(tree, allExpandedDiffTreeDirs(tree))

	// internal/deep/nested is a single-child chain: one row naming it all.
	var chainRow *diffTreeRow
	for i := range rows {
		if rows[i].name == "internal/deep/nested" {
			chainRow = &rows[i]
		}
	}
	if chainRow == nil {
		var names []string
		for _, row := range rows {
			names = append(names, row.name)
		}
		t.Fatalf("collapsed chain row missing; rows = %v", names)
	}
	if !chainRow.dir {
		t.Fatalf("chain row is not a directory: %+v", chainRow)
	}

	// src has two children (util is its only child, but util has two), so
	// the chain stops at "src/util".
	found := false
	for _, row := range rows {
		if row.name == "src/util" {
			found = true
		}
		if row.name == "src" && row.dir {
			found = true
		}
	}
	if !found {
		t.Fatalf("src chain rows missing from %+v", rows)
	}
}

func TestFlattenDiffFileTreeRespectsExpansion(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())

	// Nothing expanded: only root rows render.
	rows := flattenDiffFileTree(tree, map[int]bool{})
	for _, row := range rows {
		if row.depth != 0 {
			t.Fatalf("collapsed tree rendered depth > 0: %+v", row)
		}
	}

	// Everything expanded: every file appears.
	rows = flattenDiffFileTree(tree, allExpandedDiffTreeDirs(tree))
	files := 0
	for _, row := range rows {
		if row.fileIndex >= 0 {
			files++
		}
	}
	if files != len(treeFixture()) {
		t.Fatalf("expanded tree shows %d files, want %d", files, len(treeFixture()))
	}
}

func TestMoveDiffTreeSelection(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	rows := flattenDiffFileTree(tree, allExpandedDiffTreeDirs(tree))

	if got := moveDiffTreeSelection(rows, -1, 1); got != rows[0].id {
		t.Fatalf("absent selection: %d, want first row %d", got, rows[0].id)
	}
	got := moveDiffTreeSelection(rows, rows[0].id, 1)
	if got != rows[1].id {
		t.Fatalf("step down: %d, want %d", got, rows[1].id)
	}
	if got := moveDiffTreeSelection(rows, rows[0].id, -1); got != rows[0].id {
		t.Fatalf("step up past the top must clamp: %d", got)
	}
	last := rows[len(rows)-1].id
	if got := moveDiffTreeSelection(rows, last, 1); got != last {
		t.Fatalf("step down past the bottom must clamp: %d", got)
	}
}

func TestMoveDiffTreeSelectionToChildAndParent(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	rows := flattenDiffFileTree(tree, allExpandedDiffTreeDirs(tree))

	// Find the collapsed "internal/deep/nested" chain row and step into it.
	// (src/util is NOT a collapsed chain: src has two children, so the chain
	// stops at src and util renders as its own row.)
	var chainRow = -1
	for i, row := range rows {
		if row.dir && row.name == "internal/deep/nested" {
			chainRow = i
			break
		}
	}
	if chainRow == -1 {
		t.Fatalf("internal/deep/nested chain row not found")
	}
	child := moveDiffTreeSelectionToFirstChild(rows, rows[chainRow].id)
	if child == rows[chainRow].id {
		t.Fatalf("first-child move did not move")
	}
	if rowIndexOf(rows, child) != chainRow+1 {
		t.Fatalf("first child is not the next row: %d vs %d", rowIndexOf(rows, child), chainRow+1)
	}

	// From that child, the parent move returns to the chain row.
	parent := moveDiffTreeSelectionToParent(rows, child)
	if parent != rows[chainRow].id {
		t.Fatalf("parent move = %d, want %d", parent, rows[chainRow].id)
	}
}

func TestMoveDiffTreeSelectionToFileClamps(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	rows := flattenDiffFileTree(tree, allExpandedDiffTreeDirs(tree))

	first := moveDiffTreeSelectionToFile(rows, -1, 1)
	last := moveDiffTreeSelectionToFile(rows, -1, -1)
	if first == last {
		t.Fatalf("forward and backward from absent selection landed on the same row")
	}
	if rowIndexOf(rows, first) > rowIndexOf(rows, last) {
		t.Fatalf("first file row (%d) is after last file row (%d)", rowIndexOf(rows, first), rowIndexOf(rows, last))
	}

	// TS's moveFileTreeSelectionToFile clamps at both ends rather than
	// wrapping (find with no match falls through to the boundary file).
	if got := moveDiffTreeSelectionToFile(rows, last, 1); got != last {
		t.Fatalf("clamp forward = %d, want %d", got, last)
	}
	if got := moveDiffTreeSelectionToFile(rows, first, -1); got != first {
		t.Fatalf("clamp backward = %d, want %d", got, first)
	}

	// Stepping from a directory row moves to the next file row below it.
	dirRow := -1
	for i, row := range rows {
		if row.dir {
			dirRow = i
			break
		}
	}
	next := moveDiffTreeSelectionToFile(rows, rows[dirRow].id, 1)
	if rowIndexOf(rows, next) <= dirRow {
		t.Fatalf("step from a directory did not advance: %d", rowIndexOf(rows, next))
	}
}

func TestDiffFileSelectionForExpandsAncestors(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	// fileIndex 4 is internal/deep/nested/file.go.
	sel, ok := diffFileSelectionFor(tree, 4)
	if !ok {
		t.Fatal("selection not found for fileIndex 4")
	}
	node := tree.nodes[sel.highlightedNode]
	if node.fileIndex != 4 {
		t.Fatalf("highlighted node maps to fileIndex %d", node.fileIndex)
	}
	if len(sel.expandedNodes) != 3 {
		t.Fatalf("ancestor expansion = %v, want 3 directories", sel.expandedNodes)
	}

	if _, ok := diffFileSelectionFor(tree, 99); ok {
		t.Fatal("selection found for a nonexistent fileIndex")
	}
}

func TestOrderedDiffPatchFileIndexesMatchRowOrder(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	rows := flattenDiffFileTree(tree, allExpandedDiffTreeDirs(tree))

	indexes := orderedDiffPatchFileIndexes(rows)
	if len(indexes) != len(treeFixture()) {
		t.Fatalf("ordered indexes = %v", indexes)
	}
	// Tree order sorts by directory then name, so util/other.go precedes
	// util/path.go regardless of input order.
	var names []string
	for _, index := range indexes {
		names = append(names, treeFixture()[index].File)
	}
	if names[0] != "internal/deep/nested/file.go" {
		t.Fatalf("tree order = %v", names)
	}
}

func TestMoveDiffPatchFileIndex(t *testing.T) {
	indexes := []int{3, 5, 7}
	if got := moveDiffPatchFileIndex(indexes, -1, 1); got != 3 {
		t.Fatalf("absent current = %d, want 3", got)
	}
	if got := moveDiffPatchFileIndex(indexes, 5, 1); got != 7 {
		t.Fatalf("step = %d, want 7", got)
	}
	if got := moveDiffPatchFileIndex(indexes, 7, 1); got != 7 {
		t.Fatalf("clamp at end = %d, want 7", got)
	}
	if got := moveDiffPatchFileIndex(indexes, 99, 1); got != 3 {
		t.Fatalf("unknown current = %d, want 3", got)
	}
	if got := moveDiffPatchFileIndex(nil, 3, 1); got != -1 {
		t.Fatalf("empty list = %d, want -1", got)
	}
}

func TestSingleDiffPatchFileIndexFallsBackInOrder(t *testing.T) {
	if got := singleDiffPatchFileIndex(-1, -1, -1, 4); got != 4 {
		t.Fatalf("first fallback = %d", got)
	}
	if got := singleDiffPatchFileIndex(-1, 2, 3, 4); got != 2 {
		t.Fatalf("active beats current: %d", got)
	}
	if got := singleDiffPatchFileIndex(1, 2, 3, 4); got != 1 {
		t.Fatalf("selected beats everything: %d", got)
	}
	if got := singleDiffPatchFileIndex(-1, -1, -1, -1); got != -1 {
		t.Fatalf("all unset = %d", got)
	}
}

func TestExpandCollapseHelpers(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	var dirID int
	for _, node := range tree.nodes {
		if node.dir {
			dirID = node.id
			break
		}
	}

	expanded := setDiffTreeDirExpanded(tree, map[int]bool{}, dirID, true)
	if !expanded[dirID] {
		t.Fatalf("expand did not set the directory")
	}
	expanded = toggleDiffTreeDir(tree, expanded, dirID)
	if expanded[dirID] {
		t.Fatalf("toggle did not clear the directory")
	}

	// A file selection is left alone by both helpers.
	fileID := -1
	for _, node := range tree.nodes {
		if !node.dir {
			fileID = node.id
			break
		}
	}
	if got := setDiffTreeDirExpanded(tree, map[int]bool{fileID: true}, fileID, false); !got[fileID] {
		t.Fatalf("file selection was mutated")
	}
}

func TestAllExpandedDiffTreeDirsCoversEveryDirectory(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	expanded := allExpandedDiffTreeDirs(tree)
	dirs := 0
	for _, node := range tree.nodes {
		if node.dir {
			dirs++
		}
	}
	if len(expanded) != dirs {
		t.Fatalf("expanded = %d entries, want %d directories", len(expanded), dirs)
	}
}

func TestShowDiffViewerFileTree(t *testing.T) {
	if !showDiffViewerFileTree(true, 1) {
		t.Fatal("enabled with files must show")
	}
	if showDiffViewerFileTree(true, 0) {
		t.Fatal("enabled with no files must hide")
	}
	if showDiffViewerFileTree(false, 10) {
		t.Fatal("disabled must hide")
	}
}

func TestFlattenEmptyTree(t *testing.T) {
	if rows := flattenDiffFileTree(buildDiffFileTree(nil), map[int]bool{}); len(rows) != 0 {
		t.Fatalf("empty tree flattened to %+v", rows)
	}
}

func TestBuildDiffFileTreeIgnoresEmptySegments(t *testing.T) {
	// Leading/trailing/duplicate slashes must not create phantom segments.
	tree := buildDiffFileTree([]diffTreeItem{{File: "a//b/", Status: "modified"}})
	var files int
	for _, node := range tree.nodes {
		if !node.dir {
			files++
			if node.name != "b" {
				t.Fatalf("file node named %q, want b", node.name)
			}
		}
	}
	if files != 1 {
		t.Fatalf("empty segments produced %d file nodes", files)
	}
}

func TestFlattenRowsHaveStableOrder(t *testing.T) {
	tree := buildDiffFileTree(treeFixture())
	first := flattenDiffFileTree(tree, allExpandedDiffTreeDirs(tree))
	second := flattenDiffFileTree(tree, allExpandedDiffTreeDirs(tree))
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("flatten is not deterministic:\n%v\n%v", first, second)
	}
}
