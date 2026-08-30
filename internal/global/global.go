package global

import (
	"os"
	"path/filepath"

	"github.com/anomalyco/opencode-go/internal/flag"
)

const app = "opencode"

type Paths struct {
	Home   string
	Data   string
	Bin    string
	Log    string
	Repos  string
	Cache  string
	Config string
	State  string
	Tmp    string
}

func xdg(env, fallback string) string {
	if value := os.Getenv(env); value != "" {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return filepath.Join(home, fallback)
}

// Resolve computes the paths from the current environment. It reads env vars at
// call time so tests can override them; the package-level Path is a snapshot
// taken at load, mirroring the TypeScript module.
func Resolve() Paths {
	home := os.Getenv("OPENCODE_TEST_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			panic(err)
		}
	}
	data := filepath.Join(xdg("XDG_DATA_HOME", ".local/share"), app)
	cache := filepath.Join(xdg("XDG_CACHE_HOME", ".cache"), app)
	config := filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), app)
	state := filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), app)
	return Paths{
		Home:   home,
		Data:   data,
		Bin:    filepath.Join(cache, "bin"),
		Log:    filepath.Join(data, "log"),
		Repos:  filepath.Join(data, "repos"),
		Cache:  cache,
		Config: config,
		State:  state,
		Tmp:    filepath.Join(os.TempDir(), app),
	}
}

var Path = Resolve()

func Init() error {
	paths := Resolve()
	for _, dir := range []string{paths.Data, paths.Config, paths.State, paths.Tmp, paths.Log, paths.Bin, paths.Repos} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// Make returns the effective paths, applying overrides. The config directory
// respects OPENCODE_CONFIG_DIR, matching the TypeScript Global service.
func Make(overrides ...Paths) Paths {
	paths := Resolve()
	if dir := flag.ConfigDir(); dir != "" {
		paths.Config = dir
	}
	if len(overrides) > 0 {
		o := overrides[0]
		if o.Home != "" {
			paths.Home = o.Home
		}
		if o.Data != "" {
			paths.Data = o.Data
		}
		if o.Bin != "" {
			paths.Bin = o.Bin
		}
		if o.Log != "" {
			paths.Log = o.Log
		}
		if o.Repos != "" {
			paths.Repos = o.Repos
		}
		if o.Cache != "" {
			paths.Cache = o.Cache
		}
		if o.Config != "" {
			paths.Config = o.Config
		}
		if o.State != "" {
			paths.State = o.State
		}
		if o.Tmp != "" {
			paths.Tmp = o.Tmp
		}
	}
	return paths
}
