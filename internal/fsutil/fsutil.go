package fsutil

import (
	"os"
	"path/filepath"
)

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// FindUp walks from start toward the filesystem root, collecting every
// join(current, target) that exists. It stops once current equals stop or the
// root is reached.
func FindUp(target, start string, stop ...string) []string {
	var stopAt string
	if len(stop) > 0 {
		stopAt = stop[0]
	}
	var result []string
	current := start
	for {
		if exists(filepath.Join(current, target)) {
			result = append(result, filepath.Join(current, target))
		}
		if stopAt == current {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return result
}

// Up is FindUp over multiple targets. At each directory level the targets are
// checked in order.
func Up(targets []string, start string, stop ...string) []string {
	var stopAt string
	if len(stop) > 0 {
		stopAt = stop[0]
	}
	var result []string
	current := start
	for {
		for _, target := range targets {
			candidate := filepath.Join(current, target)
			if exists(candidate) {
				result = append(result, candidate)
			}
		}
		if stopAt == current {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return result
}

func IsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func IsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
