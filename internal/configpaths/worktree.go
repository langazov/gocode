package configpaths

import (
	"path/filepath"

	"github.com/langazov/gocode-go/internal/fsutil"
)

// Worktree resolves the worktree boundary for project config discovery: the
// nearest ancestor containing .git, or the directory itself when there is no
// repository (matching ctx.worktree in the TypeScript config loader).
func Worktree(directory string) string {
	if hits := fsutil.FindUp(".git", directory); len(hits) > 0 {
		return filepath.Dir(hits[0])
	}
	if resolved, err := filepath.Abs(directory); err == nil {
		return resolved
	}
	return directory
}
