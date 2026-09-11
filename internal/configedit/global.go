package configedit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/global"
)

// Global returns the global config file the edits in this package would
// target, and whether any global config exists at all. When none does, the
// path is where DefaultName would be created.
func Global() (string, bool, error) {
	dir := global.Resolve().Config
	for _, name := range candidates {
		path := filepath.Join(dir, name)
		_, err := os.Stat(path)
		if err == nil {
			return path, true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false, err
		}
	}
	return filepath.Join(dir, DefaultName), false, nil
}

// CreateGlobal writes an empty global config when none exists yet, and is a
// no-op otherwise. It returns the global config path either way.
func CreateGlobal() (string, error) {
	path, exists, err := Global()
	if err != nil || exists {
		return path, err
	}
	return path, write(path, []byte("{}\n"))
}
