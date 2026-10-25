package harness

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/diff"
)

// CopyDir copies src into dst (created if missing), preserving file modes
// and symlinks. A missing src leaves dst empty: a task without a workspace
// starts from nothing.
//
// Copies are copy-on-write where the filesystem allows: a fresh dst is
// cloned whole (clonefile on APFS), otherwise each file is reflinked when
// it can be and copied when it cannot. A rollout of a large repository then
// costs disk only for what it changes.
func CopyDir(src, dst string) error {
	if src != "" {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			if info, err := os.Stat(src); err == nil && info.IsDir() {
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					return err
				}
				if cloneTree(src, dst) == nil {
					return nil
				}
			}
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	if src == "" {
		return nil
	}
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case entry.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(path, target, info.Mode().Perm())
		}
		return nil // sockets, devices: not part of a workspace
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if _, err := os.Lstat(dst); os.IsNotExist(err) && cloneFile(src, dst) == nil {
		return os.Chmod(dst, mode)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// skipDiffDir names directories whose contents are machinery, not work: the
// VCS store and gocode's own state.
func skipDiffDir(name string) bool {
	return name == ".git" || name == "node_modules" || name == "__pycache__"
}

// maxDiffFile caps one file's rendered diff; maxDiffTotal caps the whole.
const (
	maxDiffFile  = 60_000
	maxDiffTotal = 400_000
)

// WorkspaceDiff renders what a rollout changed: a unified diff of every text
// file added, modified or removed between the pristine source and the
// rollout's workspace, plus one line per changed binary file. It is the
// deliverable of a coding task — the change itself, not the agent's account
// of it.
func WorkspaceDiff(source, workspace string) (string, []string, error) {
	before, err := snapshot(source)
	if err != nil {
		return "", nil, err
	}
	after, err := snapshot(workspace)
	if err != nil {
		return "", nil, err
	}
	paths := map[string]bool{}
	for rel := range before {
		paths[rel] = true
	}
	for rel := range after {
		paths[rel] = true
	}
	sorted := make([]string, 0, len(paths))
	for rel := range paths {
		sorted = append(sorted, rel)
	}
	sort.Strings(sorted)

	var out strings.Builder
	var changed []string
	for _, rel := range sorted {
		old, hadOld := before[rel]
		cur, hasNew := after[rel]
		if hadOld && hasNew && bytes.Equal(old, cur) {
			continue
		}
		changed = append(changed, rel)
		if out.Len() > maxDiffTotal {
			continue
		}
		if isBinary(old) || isBinary(cur) {
			fmt.Fprintf(&out, "Binary file %s %s\n", rel, binaryVerb(hadOld, hasNew))
			continue
		}
		oldLabel, newLabel := "a/"+rel, "b/"+rel
		if !hadOld {
			oldLabel = "/dev/null"
		}
		if !hasNew {
			newLabel = "/dev/null"
		}
		unified := diff.Unified(oldLabel, newLabel, string(old), string(cur))
		if len(unified) > maxDiffFile {
			unified = unified[:maxDiffFile] + fmt.Sprintf("\n…(+%d chars — render truncation)\n", len(unified)-maxDiffFile)
		}
		out.WriteString(unified)
		if !strings.HasSuffix(unified, "\n") {
			out.WriteByte('\n')
		}
	}
	if out.Len() > maxDiffTotal {
		fmt.Fprintf(&out, "…(diff truncated; %d files changed in all — render truncation)\n", len(changed))
	}
	return out.String(), changed, nil
}

func binaryVerb(hadOld, hasNew bool) string {
	switch {
	case !hadOld:
		return "added"
	case !hasNew:
		return "removed"
	}
	return "changed"
}

func isBinary(data []byte) bool {
	n := len(data)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}

func snapshot(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	if root == "" {
		return out, nil
	}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && skipDiffDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	return out, err
}
