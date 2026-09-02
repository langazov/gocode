package lsp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Server describes how to find and start one language server.
type Server struct {
	ID string
	// Extensions the server handles. Empty means every file.
	Extensions []string
	// Command is the argv. The first element is looked up on PATH.
	Command []string
	// Env adds to the server process environment.
	Env map[string]string
	// Initialization is passed as initializationOptions.
	Initialization map[string]any
	// RootMarkers name the files whose nearest ancestor directory is the
	// project root.
	RootMarkers []string
	// StrictRoot requires a marker: without one the server is not started at
	// all, rather than falling back to the working directory. Ports
	// StrictNearestRoot, used where running a server on an unrelated tree is
	// worse than not running it.
	StrictRoot bool
	// Disabled excludes a server that config turned off.
	Disabled bool
}

// builtinServers are the servers this port knows how to start.
//
// Scope note: the TypeScript registry is ~2000 lines for 38 servers, most of
// that being installers — npm install, go install, GitHub release downloads,
// archive extraction. None of that is LSP, and a static Go binary is a poor
// place for it, so this port starts only servers already on PATH. Anything
// missing is reachable through the `lsp` config section, which takes an
// explicit command and covers servers this list does not name at all.
var builtinServers = []Server{
	{
		ID:          "gopls",
		Extensions:  []string{".go"},
		Command:     []string{"gopls"},
		RootMarkers: []string{"go.work", "go.mod"},
	},
	{
		ID:          "typescript",
		Extensions:  []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"},
		Command:     []string{"typescript-language-server", "--stdio"},
		RootMarkers: []string{"tsconfig.json", "jsconfig.json", "package.json"},
	},
	{
		ID:          "rust",
		Extensions:  []string{".rs"},
		Command:     []string{"rust-analyzer"},
		RootMarkers: []string{"Cargo.toml"},
	},
	{
		ID:          "pyright",
		Extensions:  []string{".py", ".pyi"},
		Command:     []string{"pyright-langserver", "--stdio"},
		RootMarkers: []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile"},
	},
	{
		ID:          "ruff",
		Extensions:  []string{".py", ".pyi"},
		Command:     []string{"ruff", "server"},
		RootMarkers: []string{"pyproject.toml", "ruff.toml", ".ruff.toml"},
		StrictRoot:  true,
	},
	{
		ID:          "clangd",
		Extensions:  []string{".c", ".cpp", ".cc", ".cxx", ".h", ".hpp", ".hxx", ".objc", ".m", ".mm"},
		Command:     []string{"clangd"},
		RootMarkers: []string{"compile_commands.json", "compile_flags.txt", "CMakeLists.txt", "Makefile"},
	},
	{
		ID:          "zls",
		Extensions:  []string{".zig", ".zon"},
		Command:     []string{"zls"},
		RootMarkers: []string{"build.zig"},
	},
	{
		ID:          "lua-ls",
		Extensions:  []string{".lua"},
		Command:     []string{"lua-language-server"},
		RootMarkers: []string{".luarc.json", ".luarc.jsonc", "stylua.toml", ".stylua.toml"},
	},
	{
		ID:          "bash",
		Extensions:  []string{".sh", ".bash", ".zsh"},
		Command:     []string{"bash-language-server", "start"},
		RootMarkers: []string{},
	},
	{
		ID:          "terraform",
		Extensions:  []string{".tf", ".tfvars"},
		Command:     []string{"terraform-ls", "serve"},
		RootMarkers: []string{".terraform", "main.tf"},
	},
	{
		ID:          "dart",
		Extensions:  []string{".dart"},
		Command:     []string{"dart", "language-server", "--protocol=lsp"},
		RootMarkers: []string{"pubspec.yaml"},
	},
	{
		ID:          "ocaml-lsp",
		Extensions:  []string{".ml", ".mli"},
		Command:     []string{"ocamllsp"},
		RootMarkers: []string{"dune-project", "dune-workspace"},
	},
	{
		ID:          "gleam",
		Extensions:  []string{".gleam"},
		Command:     []string{"gleam", "lsp"},
		RootMarkers: []string{"gleam.toml"},
	},
	{
		ID:          "nixd",
		Extensions:  []string{".nix"},
		Command:     []string{"nixd"},
		RootMarkers: []string{"flake.nix", "default.nix", "shell.nix"},
	},
	{
		ID:          "clojure-lsp",
		Extensions:  []string{".clj", ".cljs", ".cljc", ".edn"},
		Command:     []string{"clojure-lsp"},
		RootMarkers: []string{"project.clj", "deps.edn", "build.boot"},
	},
	{
		ID:          "elixir-ls",
		Extensions:  []string{".ex", ".exs"},
		Command:     []string{"elixir-ls"},
		RootMarkers: []string{"mix.exs"},
	},
	{
		ID:          "haskell-language-server",
		Extensions:  []string{".hs", ".lhs"},
		Command:     []string{"haskell-language-server-wrapper", "--lsp"},
		RootMarkers: []string{"stack.yaml", "cabal.project", "*.cabal"},
	},
	{
		ID:          "yaml-ls",
		Extensions:  []string{".yaml", ".yml"},
		Command:     []string{"yaml-language-server", "--stdio"},
		RootMarkers: []string{},
	},
	{
		ID:          "json-ls",
		Extensions:  []string{".json", ".jsonc"},
		Command:     []string{"vscode-json-language-server", "--stdio"},
		RootMarkers: []string{},
	},
	{
		ID:          "texlab",
		Extensions:  []string{".tex", ".bib"},
		Command:     []string{"texlab"},
		RootMarkers: []string{},
	},
	{
		ID:          "svelte",
		Extensions:  []string{".svelte"},
		Command:     []string{"svelteserver", "--stdio"},
		RootMarkers: []string{"svelte.config.js", "package.json"},
	},
	{
		ID:          "astro",
		Extensions:  []string{".astro"},
		Command:     []string{"astro-ls", "--stdio"},
		RootMarkers: []string{"astro.config.mjs", "package.json"},
	},
	{
		ID:          "prisma",
		Extensions:  []string{".prisma"},
		Command:     []string{"prisma-language-server", "--stdio"},
		RootMarkers: []string{},
	},
	{
		ID:          "dockerfile",
		Extensions:  []string{".dockerfile", "Dockerfile"},
		Command:     []string{"docker-langserver", "--stdio"},
		RootMarkers: []string{},
	},
	{
		ID:          "csharp",
		Extensions:  []string{".cs"},
		Command:     []string{"omnisharp", "-lsp"},
		RootMarkers: []string{"*.sln", "*.csproj"},
	},
	{
		ID:          "kotlin-ls",
		Extensions:  []string{".kt", ".kts"},
		Command:     []string{"kotlin-language-server"},
		RootMarkers: []string{"build.gradle.kts", "build.gradle", "pom.xml"},
	},
	{
		ID:          "sourcekit-lsp",
		Extensions:  []string{".swift"},
		Command:     []string{"sourcekit-lsp"},
		RootMarkers: []string{"Package.swift"},
	},
}

// Available reports whether the server's binary is on PATH.
func (s Server) Available() bool {
	if len(s.Command) == 0 {
		return false
	}
	_, err := exec.LookPath(s.Command[0])
	return err == nil
}

// Handles reports whether this server claims a file.
func (s Server) Handles(path string) bool {
	if len(s.Extensions) == 0 {
		return true
	}
	extension := filepath.Ext(path)
	base := filepath.Base(path)
	for _, candidate := range s.Extensions {
		if extension != "" && strings.EqualFold(candidate, extension) {
			return true
		}
		// An entry with no dot names a whole filename (Dockerfile).
		if !strings.HasPrefix(candidate, ".") && strings.EqualFold(candidate, base) {
			return true
		}
	}
	return false
}

// Root finds the project root for a file, porting NearestRoot and
// StrictNearestRoot: walk up from the file looking for a marker, stopping at
// the working directory. Without a marker a lenient server falls back to the
// working directory and a strict one declines to run.
func (s Server) Root(file, directory string) (string, bool) {
	dir := filepath.Dir(normalizePath(file))
	stop := normalizePath(directory)

	for {
		for _, marker := range s.RootMarkers {
			if matchesMarker(dir, marker) {
				return dir, true
			}
		}
		if dir == stop || dir == filepath.Dir(dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if s.StrictRoot {
		return "", false
	}
	return stop, true
}

// matchesMarker reports whether a directory holds a marker, supporting the
// `*.ext` form the registry uses for project files whose name varies.
func matchesMarker(dir, marker string) bool {
	if strings.ContainsAny(marker, "*?[") {
		matches, err := filepath.Glob(filepath.Join(dir, marker))
		return err == nil && len(matches) > 0
	}
	_, err := os.Stat(filepath.Join(dir, marker))
	return err == nil
}

// languageID maps an extension to an LSP language identifier.
func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".py", ".pyi":
		return "python"
	case ".rs":
		return "rust"
	case ".c":
		return "c"
	case ".cpp", ".cc", ".cxx", ".hpp", ".hxx":
		return "cpp"
	case ".h":
		return "c"
	case ".zig", ".zon":
		return "zig"
	case ".lua":
		return "lua"
	case ".sh", ".bash", ".zsh":
		return "shellscript"
	case ".rb":
		return "ruby"
	case ".java":
		return "java"
	case ".kt", ".kts":
		return "kotlin"
	case ".swift":
		return "swift"
	case ".cs":
		return "csharp"
	case ".php":
		return "php"
	case ".dart":
		return "dart"
	case ".ex", ".exs":
		return "elixir"
	case ".hs", ".lhs":
		return "haskell"
	case ".ml", ".mli":
		return "ocaml"
	case ".gleam":
		return "gleam"
	case ".nix":
		return "nix"
	case ".clj", ".cljs", ".cljc", ".edn":
		return "clojure"
	case ".json", ".jsonc":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".tf", ".tfvars":
		return "terraform"
	case ".svelte":
		return "svelte"
	case ".astro":
		return "astro"
	case ".tex":
		return "latex"
	case ".prisma":
		return "prisma"
	}
	if strings.EqualFold(filepath.Base(path), "Dockerfile") {
		return "dockerfile"
	}
	return "plaintext"
}
