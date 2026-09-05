// Package chunk walks a project directory and splits its text files into
// overlapping line-window chunks for embedding.
//
// Splitting is deliberately language-agnostic: a fixed-size sliding window
// over lines, not a tree-sitter-aware split. That keeps this package
// dependency-free and correct for any text format (code, markdown, config),
// at the cost of occasionally cutting a chunk mid-function. The overlap
// exists precisely to make that acceptable — a boundary a reader would find
// mid-sentence still has the surrounding lines in the neighboring chunk.
package chunk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/langazov/gocode-go/internal/lsp"
)

// SymbolResolver is the subset of *lsp.Service a syntax-aware chunker needs:
// one file's outline. Depending on this narrow interface instead of *lsp.Service
// directly keeps this package testable with a hand-written fake instead of a
// spawned language server, and keeps the dependency honest about how little
// of the LSP client this code actually uses.
//
// *lsp.Service satisfies this. A nil *lsp.Service (LSP disabled, or never
// configured) already returns (nil, nil) from its own DocumentSymbols, which
// this package treats as "fall back to the sliding window" — the same
// outcome as leaving Options.LSP nil altogether.
type SymbolResolver interface {
	DocumentSymbols(ctx context.Context, file string) ([]lsp.DocumentSymbol, error)
}

// Options controls the walk and the split.
type Options struct {
	// Include is a set of glob patterns (doublestar syntax, `**` supported).
	// A file must match at least one to be indexed. Empty means "match the
	// built-in allowlist of known code, documentation, and small config file
	// extensions" (see textExtensions) rather than literally everything —
	// setting Include explicitly opts back out of that default and trusts
	// the caller's own patterns instead. Either way, Exclude and the
	// binary-file checks still apply on top.
	Include []string
	// Exclude is a set of glob patterns; a file matching any of these is
	// skipped even if it matches Include.
	Exclude []string
	// Lines is the chunk size in source lines. Defaults to 60.
	Lines int
	// Overlap is how many trailing lines of one chunk repeat as the leading
	// lines of the next. Defaults to 10. Must be less than Lines.
	Overlap int
	// MaxFileBytes skips files larger than this, so one huge generated file
	// (a lockfile, a bundled asset) cannot dominate an index. Defaults to 1MB.
	MaxFileBytes int64
	// LSP, when set, is asked for each file's symbol outline first; a file
	// whose language it recognizes is split at real function/class/method
	// boundaries instead of the fixed-line sliding window. A file with no
	// resolver, no matching server, or no symbols returned falls back to the
	// sliding window unchanged — this is strictly additive to the plain
	// Options{} behavior every existing caller already gets.
	LSP SymbolResolver
	// PathPrefix is prepended to every chunk's Path when root is a
	// subdirectory of the project rather than the project root itself (a
	// scoped reindex of one subtree). Include/Exclude still match against the
	// root-relative path, unprefixed; only the resulting Chunk.Path (and thus
	// its ID, which is derived from it) gains the prefix, so a chunk produced
	// by indexing "sub/" alone gets the same ID and Path a whole-project walk
	// would have given it. Empty (whole-project indexing) is a no-op.
	PathPrefix string
	// DisableGitignore turns off .gitignore/.ignore handling entirely,
	// restoring pre-existing behavior where only Include/Exclude and the
	// built-in binary/size checks decide what's walked.
	DisableGitignore bool
	// IgnoreBase is the project root to load ancestor .gitignore/.ignore
	// files from, down through root, when root is a subdirectory being
	// walked on its own — otherwise a scoped reindex would miss exclusions
	// declared above it (a repo's top-level .gitignore, say). Empty means
	// "just root," the right default for a whole-project walk where root
	// already is the project root.
	IgnoreBase string
}

func (o Options) withDefaults() Options {
	if o.Lines <= 0 {
		o.Lines = 60
	}
	if o.Overlap < 0 || o.Overlap >= o.Lines {
		o.Overlap = 10
	}
	if o.MaxFileBytes <= 0 {
		o.MaxFileBytes = 1 << 20
	}
	return o
}

// Chunk is one indexable slice of a file.
type Chunk struct {
	// ID is content-addressed on (path, start, end): sha256 hex, stable across
	// reindexes as long as the chunk boundaries don't move.
	ID string
	// Path is project-root-relative, slash-separated.
	Path        string
	StartLine   int // 1-based, inclusive
	EndLine     int // 1-based, inclusive
	Content     string
	ContentHash string // sha256 hex of Content, for incremental reindex
}

// Walk splits every matching text file under root into chunks. Paths in the
// returned chunks are root-relative and slash-separated regardless of OS.
func Walk(ctx context.Context, root string, opts Options) ([]Chunk, error) {
	opts = opts.withDefaults()
	root = filepath.Clean(root)
	prefix := strings.Trim(filepath.ToSlash(opts.PathPrefix), "/")

	var ignores *ignoreStack
	if !opts.DisableGitignore {
		ignores = newIgnoreStack(root, opts.IgnoreBase)
	}

	var chunks []Chunk
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" && path != root {
				return fs.SkipDir
			}
			if ignores != nil && path != root {
				if ignores.descend(path, true) {
					return fs.SkipDir
				}
				ignores.push(path)
			}
			return nil
		}
		if ignores != nil {
			if ignores.descend(path, false) {
				return nil
			}
			// The ignore files themselves are project metadata, not content
			// worth searching; index everything else while skipping these.
			if name := entry.Name(); name == ".gitignore" || name == ".ignore" {
				return nil
			}
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if !matches(relative, opts.Include, opts.Exclude) {
			return nil
		}
		if isBinaryExt(relative) {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.Size() > opts.MaxFileBytes || info.Size() == 0 {
			return nil
		}
		if max, capped := smallDataExtMaxBytes[strings.ToLower(filepath.Ext(relative))]; capped && info.Size() > max {
			return nil
		}
		relPath := relative
		if prefix != "" {
			relPath = prefix + "/" + relative
		}
		fileChunks, readErr := chunkFile(ctx, path, relPath, opts)
		if readErr != nil {
			// Unreadable or binary: skip, don't fail the whole walk.
			return nil
		}
		chunks = append(chunks, fileChunks...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].Path != chunks[j].Path {
			return chunks[i].Path < chunks[j].Path
		}
		return chunks[i].StartLine < chunks[j].StartLine
	})
	return chunks, nil
}

func matches(relative string, include, exclude []string) bool {
	for _, pattern := range exclude {
		if ok, _ := doublestar.Match(pattern, relative); ok {
			return false
		}
	}
	if len(include) == 0 {
		return isDefaultTextCandidate(relative)
	}
	for _, pattern := range include {
		if ok, _ := doublestar.Match(pattern, relative); ok {
			return true
		}
	}
	return false
}

// textExtensions is the default allowlist of file extensions considered
// meaningful for semantic code/doc search, consulted only when Options.
// Include is empty. A blocklist of binary formats (see isBinaryExt) can
// never enumerate every non-code text format — a multi-megabyte SVG sprite
// or a JSON API-response fixture is perfectly valid UTF-8 and would sail
// through a NUL-byte sniff — so the default is inverted instead: nothing
// gets indexed unless it's recognizably code, docs, or small structured
// config. An explicit Include list bypasses this entirely.
var textExtensions = map[string]bool{
	// Code
	".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".py": true, ".rb": true, ".java": true, ".kt": true, ".kts": true, ".scala": true,
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".cxx": true, ".hpp": true, ".hh": true,
	".cs": true, ".fs": true, ".fsx": true, ".swift": true, ".m": true, ".mm": true,
	".rs": true, ".php": true, ".pl": true, ".pm": true, ".lua": true, ".r": true, ".jl": true,
	".ex": true, ".exs": true, ".erl": true, ".hrl": true,
	".clj": true, ".cljs": true, ".cljc": true, ".hs": true, ".ml": true, ".mli": true,
	".nim": true, ".zig": true, ".dart": true, ".groovy": true, ".gradle": true, ".vb": true,
	".sql": true, ".sh": true, ".bash": true, ".zsh": true, ".fish": true, ".ps1": true,
	".bat": true, ".cmd": true,
	".vue": true, ".svelte": true, ".astro": true,
	".html": true, ".htm": true, ".css": true, ".scss": true, ".sass": true, ".less": true, ".styl": true,
	".graphql": true, ".gql": true, ".proto": true, ".thrift": true,
	".tf": true, ".tfvars": true, ".hcl": true, ".cue": true, ".nix": true,

	// Documentation
	".md": true, ".mdx": true, ".markdown": true, ".rst": true, ".adoc": true, ".asciidoc": true,
	".txt": true, ".org": true,

	// Small structured config. .json gets its own tighter size cap (see
	// smallDataExtMaxBytes) since the extension covers both hand-written
	// config (small) and generated data dumps (often huge, never worth
	// searching).
	".json": true, ".jsonc": true, ".yaml": true, ".yml": true, ".toml": true,
	".ini": true, ".cfg": true, ".conf": true, ".properties": true, ".xml": true,
	".editorconfig": true, ".env": true,
}

// textFilenames extends textExtensions to well-known extensionless files
// that would otherwise never match any extension-based rule.
var textFilenames = map[string]bool{
	"Makefile": true, "makefile": true, "GNUmakefile": true,
	"Dockerfile": true, "Containerfile": true,
	"Rakefile": true, "Gemfile": true, "Procfile": true, "Vagrantfile": true,
	"Justfile": true, "justfile": true,
	"README": true, "CHANGELOG": true, "LICENSE": true, "AUTHORS": true,
	"CONTRIBUTING": true, "NOTICE": true,
}

// smallDataExtMaxBytes caps a few structured-data extensions well below
// Options.MaxFileBytes: they're valid text and pass every other filter, but
// a large instance (a dependency lockfile serialized as JSON, a recorded API
// fixture with an embedded base64 blob) is generated data, not something
// worth searching — and exactly the pathological input that blows past the
// embeddings endpoint's per-input token limit (see embed.Client). Files
// under this cap are unaffected; a normal package.json or tsconfig.json
// never comes close.
var smallDataExtMaxBytes = map[string]int64{
	".json": 64 << 10, // 64KB
}

// isDefaultTextCandidate reports whether relative names a file Walk should
// consider by default (Options.Include empty): a recognized code/doc
// extension, or a well-known extensionless filename.
func isDefaultTextCandidate(relative string) bool {
	if textFilenames[filepath.Base(relative)] {
		return true
	}
	return textExtensions[strings.ToLower(filepath.Ext(relative))]
}

func chunkFile(ctx context.Context, absPath, relPath string, opts Options) ([]Chunk, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	if looksBinary(data) {
		return nil, fmt.Errorf("chunk: %s looks binary", relPath)
	}
	lines := splitLines(string(data))
	if len(lines) == 0 {
		return nil, nil
	}

	if opts.LSP != nil {
		if symbols, err := opts.LSP.DocumentSymbols(ctx, absPath); err == nil && len(symbols) > 0 {
			return chunksFromSymbols(relPath, lines, symbols, opts), nil
		}
		// Any failure (no server for this language, request error, timeout)
		// falls straight through to the sliding window below — the resolver
		// is a best-effort enhancement, never a hard dependency.
	}

	return slidingWindow(relPath, lines, opts), nil
}

// slidingWindow is the language-agnostic fallback: a fixed-size overlapping
// line window, used when no LSP resolver is configured, no server handles
// this file's language, or the server returned no symbols.
func slidingWindow(relPath string, lines []string, opts Options) []Chunk {
	step := opts.Lines - opts.Overlap
	var out []Chunk
	for start := 0; start < len(lines); start += step {
		end := min(start+opts.Lines, len(lines))
		out = append(out, buildChunk(relPath, lines, start, end))
		if end == len(lines) {
			break
		}
	}
	return out
}

// buildChunk turns a 0-indexed, half-open [start, end) line range into a
// Chunk. Shared by both the sliding-window and syntax-aware splitters so a
// chunk's identity (ID/ContentHash derivation) has exactly one definition.
func buildChunk(relPath string, lines []string, start, end int) Chunk {
	content := strings.Join(lines[start:end], "\n")
	return Chunk{
		ID:          chunkID(relPath, start+1, end),
		Path:        relPath,
		StartLine:   start + 1,
		EndLine:     end,
		Content:     content,
		ContentHash: hashString(content),
	}
}

// splitLines splits on "\n" without dropping a trailing empty element for a
// file that doesn't end in a newline, and without producing a spurious final
// empty chunk for one that does.
func splitLines(text string) []string {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// binaryExtensions are file types skipped without ever reading their
// content: images, archives, executables, fonts, media, and other formats
// that are never meaningful to embed as text, plus a few (docx/xlsx/pptx,
// jar) whose zip-based bytes would otherwise pass the NUL-byte sniff in
// looksBinary only after wastefully reading a possibly-large file first.
// looksBinary remains the backstop for anything not on this list — a
// mislabeled file, or a binary format with no recognized extension.
var binaryExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".bmp": true,
	".ico": true, ".webp": true, ".tiff": true, ".tif": true, ".heic": true,
	".avif": true, ".icns": true,
	".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".bz2": true,
	".xz": true, ".7z": true, ".rar": true, ".jar": true, ".war": true, ".ear": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true,
	".o": true, ".obj": true, ".class": true, ".wasm": true, ".bin": true,
	".ttf": true, ".otf": true, ".woff": true, ".woff2": true, ".eot": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".mkv": true,
	".wav": true, ".flac": true, ".ogg": true, ".webm": true, ".m4a": true,
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true,
	".db": true, ".sqlite": true, ".sqlite3": true,
	".pyc": true, ".pyo": true,
}

// isBinaryExt reports whether relative's extension marks it as a known
// binary format, checked case-insensitively.
func isBinaryExt(relative string) bool {
	return binaryExtensions[strings.ToLower(filepath.Ext(relative))]
}

// looksBinary is the same heuristic git and most editors use: a NUL byte
// anywhere in the first few KB means "not text."
func looksBinary(data []byte) bool {
	probe := data
	if len(probe) > 8000 {
		probe = probe[:8000]
	}
	return slices.Contains(probe, 0)
}

func chunkID(path string, start, end int) string {
	return hashString(fmt.Sprintf("%s|%d|%d", path, start, end))
}

func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
