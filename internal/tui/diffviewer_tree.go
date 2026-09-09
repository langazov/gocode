package tui

import (
	"sort"
	"strings"
)

// diffviewer_tree.go ports the pure tree logic of the TS diff viewer's file
// tree: packages/tui/src/feature-plugins/system/diff-viewer-file-tree-utils.ts.
// Every function here has a one-to-one TS counterpart, cited on it, so a
// behavior question is answered by reading the cited function first.

// diffTreeItem is one changed file, the tree's input
// (FileTreeItem: {file, status}).
type diffTreeItem struct {
	File   string
	Status string
}

// diffTreeNode is one node of the built tree (FileTreeNode). fileIndex is -1
// for directories, mirroring the TS `fileIndex?: number` optionality.
type diffTreeNode struct {
	id        int
	name      string
	parent    int // -1 at the root
	children  []int
	depth     int
	dir       bool
	fileIndex int
}

// diffFileTree is the built tree (FileTree: {roots, nodes}). Nodes are
// index-addressed by id, so a node reference is an int.
type diffFileTree struct {
	roots []int
	nodes []diffTreeNode
}

// diffTreeRow is one visible row after flattening (FileTreeRow). A collapsed
// directory chain renders as one row whose name joins the chain's segments.
type diffTreeRow struct {
	id        int
	depth     int
	dir       bool
	name      string
	fileIndex int // -1 for directories
}

// buildDiffFileTree ports buildFileTree: shared directory nodes per path
// prefix, files as leaves, roots and children sorted directories-first then
// by name then by insertion id (compareFileTreeNodes).
func buildDiffFileTree(items []diffTreeItem) diffFileTree {
	tree := diffFileTree{nodes: make([]diffTreeNode, 0, len(items))}
	dirByPath := map[string]int{}

	for fileIndex, item := range items {
		var segments []string
		for _, segment := range strings.Split(item.File, "/") {
			if segment != "" {
				segments = append(segments, segment)
			}
		}
		if len(segments) == 0 {
			continue
		}

		// Walk the parent directories, reusing a node when the path already
		// has one (TS's reduce over segments with directoryByPath).
		parent := -1
		depth := 0
		for _, segment := range segments[:len(segments)-1] {
			path := segment
			if depth > 0 {
				path = dirPathPrefix(segments[:depth+1])
			}
			if existing, ok := dirByPath[path]; ok {
				parent = existing
				depth++
				continue
			}
			id := tree.addNode(diffTreeNode{
				name: segment, parent: parent, depth: depth, dir: true, fileIndex: -1,
			})
			dirByPath[path] = id
			parent = id
			depth++
		}

		tree.addNode(diffTreeNode{
			name:      segments[len(segments)-1],
			parent:    parent,
			depth:     depth,
			dir:       false,
			fileIndex: fileIndex,
		})
	}

	sort.Slice(tree.roots, func(i, j int) bool { return tree.compareNodes(tree.roots[i], tree.roots[j]) < 0 })
	for i := range tree.nodes {
		children := tree.nodes[i].children
		sort.SliceStable(children, func(a, b int) bool {
			return tree.compareNodes(children[a], children[b]) < 0
		})
	}
	return tree
}

// dirPathPrefix joins already-collected segments with "/" — the TS version
// composes the same string inline in its reduce.
func dirPathPrefix(segments []string) string {
	return strings.Join(segments, "/")
}

// addNode appends a node, registering it under its parent or the roots
// (TS's addFileTreeNode). Returns the new id, which is simply the index.
func (t *diffFileTree) addNode(node diffTreeNode) int {
	node.id = len(t.nodes)
	node.children = nil
	t.nodes = append(t.nodes, node)
	if node.parent == -1 {
		t.roots = append(t.roots, node.id)
	} else {
		t.nodes[node.parent].children = append(t.nodes[node.parent].children, node.id)
	}
	return node.id
}

// compareNodes ports compareFileTreeNodes: directories before files, then
// name, then id (so equal names keep insertion order).
func (t *diffFileTree) compareNodes(left, right int) int {
	l, r := t.nodes[left], t.nodes[right]
	if l.dir != r.dir {
		if l.dir {
			return -1
		}
		return 1
	}
	if l.name < r.name {
		return -1
	}
	if l.name > r.name {
		return 1
	}
	return l.id - r.id
}

// flattenDiffFileTree ports flattenFileTree: depth-first rows, with a
// directory that has exactly one child directory collapsing into a single
// row naming the whole chain ("internal/tui" instead of two rows), expanded
// only when its set says so.
func flattenDiffFileTree(tree diffFileTree, expanded map[int]bool) []diffTreeRow {
	rows := make([]diffTreeRow, 0, len(tree.nodes))
	var visit func(id, depth int)
	visit = func(id, depth int) {
		node := tree.nodes[id]
		if !node.dir {
			rows = append(rows, diffTreeRow{id: node.id, depth: depth, dir: false, name: node.name, fileIndex: node.fileIndex})
			return
		}

		chain := collapsedDirChain(tree, node.id)
		last := chain[len(chain)-1]
		names := make([]string, 0, len(chain))
		for _, item := range chain {
			names = append(names, item.name)
		}
		rows = append(rows, diffTreeRow{id: node.id, depth: depth, dir: true, name: strings.Join(names, "/"), fileIndex: -1})
		if expanded[node.id] {
			for _, child := range last.children {
				visit(child, depth+1)
			}
		}
	}
	for _, root := range tree.roots {
		visit(root, 0)
	}
	return rows
}

// collapsedDirChain ports collapsedFileTreeDirectoryChain: a directory whose
// only child is also a directory merges with it. The chain's last node is
// the one whose children actually render.
func collapsedDirChain(tree diffFileTree, id int) []diffTreeNode {
	node := tree.nodes[id]
	if len(node.children) != 1 {
		return []diffTreeNode{node}
	}
	child := tree.nodes[node.children[0]]
	if !child.dir {
		return []diffTreeNode{node}
	}
	return append([]diffTreeNode{node}, collapsedDirChain(tree, child.id)...)
}

// rowIndexOf finds a row id's index, or -1 (TS's findIndex).
func rowIndexOf(rows []diffTreeRow, id int) int {
	for i, row := range rows {
		if row.id == id {
			return i
		}
	}
	return -1
}

// moveDiffTreeSelection ports moveFileTreeSelection: step by offset,
// clamped to the list; an absent selection lands on the first row.
func moveDiffTreeSelection(rows []diffTreeRow, selected int, offset int) int {
	if len(rows) == 0 {
		return -1
	}
	index := rowIndexOf(rows, selected)
	if index == -1 {
		return rows[0].id
	}
	return rows[max(0, min(len(rows)-1, index+offset))].id
}

// moveDiffTreeSelectionToFirstChild ports moveFileTreeSelectionToFirstChild:
// from a directory, move to the row right below it when that row is deeper
// (its first visible child).
func moveDiffTreeSelectionToFirstChild(rows []diffTreeRow, selected int) int {
	index := rowIndexOf(rows, selected)
	if index == -1 {
		return selected
	}
	row := rows[index]
	if !row.dir {
		return selected
	}
	if index+1 < len(rows) && rows[index+1].depth > row.depth {
		return rows[index+1].id
	}
	return selected
}

// moveDiffTreeSelectionToParent ports moveFileTreeSelectionToParent: the
// nearest earlier row with a smaller depth.
func moveDiffTreeSelectionToParent(rows []diffTreeRow, selected int) int {
	index := rowIndexOf(rows, selected)
	if index == -1 {
		return selected
	}
	row := rows[index]
	if row.depth == 0 {
		return selected
	}
	for i := index - 1; i >= 0; i-- {
		if rows[i].depth < row.depth {
			return rows[i].id
		}
	}
	return selected
}

// moveDiffTreeSelectionToFile ports moveFileTreeSelectionToFile: step to the
// next/previous file row (skipping directories), wrapping at the ends. A
// missing selection starts from the first (offset > 0) or last (offset < 0)
// file.
func moveDiffTreeSelectionToFile(rows []diffTreeRow, selected int, offset int) int {
	var fileRows []int // indexes into rows
	for i, row := range rows {
		if row.fileIndex >= 0 {
			fileRows = append(fileRows, i)
		}
	}
	if len(fileRows) == 0 {
		return -1
	}
	selectedIndex := rowIndexOf(rows, selected)
	if selectedIndex == -1 {
		if offset < 0 {
			return rows[fileRows[len(fileRows)-1]].id
		}
		return rows[fileRows[0]].id
	}
	var next int
	found := false
	if offset < 0 {
		for i := len(fileRows) - 1; i >= 0; i-- {
			if fileRows[i] < selectedIndex {
				next, found = fileRows[i], true
				break
			}
		}
	} else {
		for _, i := range fileRows {
			if i > selectedIndex {
				next, found = i, true
				break
			}
		}
	}
	if !found {
		if offset < 0 {
			next = fileRows[0]
		} else {
			next = fileRows[len(fileRows)-1]
		}
	}
	return rows[next].id
}

// diffFileSelection is the tree state that reveals a file: the row to
// highlight and the ancestors that must be expanded for it to be visible
// (TS's fileTreeFileSelection return).
type diffFileSelection struct {
	highlightedNode int
	expandedNodes   map[int]bool
}

// diffFileSelectionFor ports fileTreeFileSelection.
func diffFileSelectionFor(tree diffFileTree, fileIndex int) (diffFileSelection, bool) {
	for _, node := range tree.nodes {
		if node.dir || node.fileIndex != fileIndex {
			continue
		}
		expanded := map[int]bool{}
		for parent := node.parent; parent != -1; parent = tree.nodes[parent].parent {
			expanded[parent] = true
		}
		return diffFileSelection{highlightedNode: node.id, expandedNodes: expanded}, true
	}
	return diffFileSelection{}, false
}

// orderedDiffPatchFileIndexes ports orderedPatchFileIndexes: the file
// indexes in tree-row order — the order n/p walk.
func orderedDiffPatchFileIndexes(rows []diffTreeRow) []int {
	out := make([]int, 0, len(rows))
	for _, row := range rows {
		if row.fileIndex >= 0 {
			out = append(out, row.fileIndex)
		}
	}
	return out
}

// moveDiffPatchFileIndex ports movePatchFileIndex: step within the ordered
// file indexes, clamped. An absent current lands on the first.
func moveDiffPatchFileIndex(indexes []int, current int, offset int) int {
	if len(indexes) == 0 {
		return -1
	}
	index := -1
	for i, value := range indexes {
		if value == current {
			index = i
			break
		}
	}
	if index == -1 {
		return indexes[0]
	}
	return indexes[max(0, min(len(indexes)-1, index+offset))]
}

// singleDiffPatchFileIndex ports singlePatchFileIndex: selected ?? active ??
// current ?? first, expressed as -1 for "unset".
func singleDiffPatchFileIndex(selected, active, current, first int) int {
	for _, value := range []int{selected, active, current, first} {
		if value >= 0 {
			return value
		}
	}
	return -1
}

// allExpandedDiffTreeDirs ports allExpandedFileTreeDirectories: every
// directory id, the "expand all" target.
func allExpandedDiffTreeDirs(tree diffFileTree) map[int]bool {
	out := make(map[int]bool)
	for _, node := range tree.nodes {
		if node.dir {
			out[node.id] = true
		}
	}
	return out
}

// setDiffTreeDirExpanded ports setFileTreeDirectoryExpanded: force one
// directory's expanded flag, leaving a non-directory selection untouched.
func setDiffTreeDirExpanded(tree diffFileTree, expanded map[int]bool, selected int, value bool) map[int]bool {
	if selected < 0 || selected >= len(tree.nodes) || !tree.nodes[selected].dir {
		return expanded
	}
	next := copyDiffExpanded(expanded)
	if value {
		next[selected] = true
	} else {
		delete(next, selected)
	}
	return next
}

// toggleDiffTreeDir ports toggleFileTreeDirectory.
func toggleDiffTreeDir(tree diffFileTree, expanded map[int]bool, selected int) map[int]bool {
	if selected < 0 || selected >= len(tree.nodes) || !tree.nodes[selected].dir {
		return expanded
	}
	next := copyDiffExpanded(expanded)
	if next[selected] {
		delete(next, selected)
	} else {
		next[selected] = true
	}
	return next
}

func copyDiffExpanded(expanded map[int]bool) map[int]bool {
	next := make(map[int]bool, len(expanded)+1)
	for key, value := range expanded {
		next[key] = value
	}
	return next
}

// showDiffViewerFileTree ports showDiffViewerFileTree: the pane renders only
// when enabled AND there is at least one file to show.
func showDiffViewerFileTree(show bool, fileCount int) bool {
	return show && fileCount > 0
}
