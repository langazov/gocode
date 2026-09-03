package configpaths

import (
	"path/filepath"

	"github.com/langazov/gocode-go/internal/flag"
	"github.com/langazov/gocode-go/internal/fsutil"
	"github.com/langazov/gocode-go/internal/global"
)

// Files searches upward from directory toward worktree for config files named
// `<name>.jsonc` and `<name>.json`, returning them reversed so the nearest file
// comes last (lowest precedence first), matching ConfigPaths.files.
func Files(name, directory string, worktree ...string) []string {
	var stop string
	if len(worktree) > 0 {
		stop = worktree[0]
	}
	found := fsutil.Up([]string{name + ".jsonc", name + ".json"}, directory, stop)
	return reversed(found)
}

// Directories returns the ordered set of config directories, matching
// ConfigPaths.directories.
func Directories(directory string, worktree ...string) []string {
	var stop string
	if len(worktree) > 0 {
		stop = worktree[0]
	}
	paths := global.Resolve()
	result := []string{paths.Config}
	if !flag.DisableProjectConfig() {
		result = append(result, fsutil.Up([]string{".gocode"}, directory, stop)...)
	}
	result = append(result, fsutil.Up([]string{".gocode"}, paths.Home, paths.Home)...)
	if dir := flag.ConfigDir(); dir != "" {
		result = append(result, dir)
	}
	return unique(result)
}

func FileInDirectory(dir, name string) []string {
	return []string{
		filepath.Join(dir, name+".json"),
		filepath.Join(dir, name+".jsonc"),
	}
}

func reversed(input []string) []string {
	out := make([]string, len(input))
	for i, v := range input {
		out[len(input)-1-i] = v
	}
	return out
}

func unique(input []string) []string {
	seen := make(map[string]bool, len(input))
	var out []string
	for _, v := range input {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
