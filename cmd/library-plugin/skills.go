package main

// skills.go exposes gocode agent skills stored in the gocoder.org Library
// (under the reserved ".skills/" prefix) as library_skill_* tools, plus the
// system.transform hook that advertises them. See CLOUD_SKILLS_PLAN.md.
//
// The remote tree mirrors a local skill folder exactly —
//
//	.skills/<name>/SKILL.md
//	.skills/<name>/references/*.md
//	.skills/<name>/scripts/*
//
// — so relative links inside SKILL.md stay valid wherever the skill is read
// from. Using a skill does not require downloading it: discover
// (library_skill_search) → read SKILL.md inline (library_skill_use) → fetch
// references and materialize scripts on demand (library_skill_show). A full
// download (library_skill_load) is for skills the user wants to keep.
//
// As with the rest of this plugin, indexing, embedding and vector search
// happen server-side in gocode-infra; this file is HTTP calls plus the
// local-filesystem half (walking, hashing, writing) that only the client
// can do.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/mddoc"
	"github.com/langazov/gocode-go/internal/skill"
)

const (
	// skillsRoot is the Library folder holding every skill
	// (website/backend/internal/library.SkillsRoot in gocode-infra).
	skillsRoot    = ".skills"
	skillFileName = "SKILL.md"

	// maxSkillFileBytes/maxSkillBytes mirror gocode-infra's
	// library.MaxSkillFileBytes/MaxSkillBytes, kept in sync by hand — the
	// same coupling-point caveat as maxUploadBytes.
	maxSkillFileBytes = 5 << 20
	maxSkillBytes     = 50 << 20

	// syncMarkerName records, inside a local skill folder, the skill
	// content hash it last matched the library at (written by load and
	// store). It is what lets library_skill_list tell "the library moved
	// on" (outdated) from "you edited it" (local changes). Never uploaded.
	syncMarkerName = ".library-sync.json"

	scopeProject = "project"
	scopeGlobal  = "global"

	// advertiseEnv overrides the skillsAdvertise option for one run.
	advertiseEnv = "GOCODE_LIBRARY_SKILLS_ADVERTISE"

	// advertiseFetchTimeout bounds the hook's metadata fetch: a turn must
	// never wait on gocoder.org for longer than this.
	advertiseFetchTimeout = 3 * time.Second
)

// Advertising defaults (plugin options skillsAdvertise*).
const (
	defaultAdvertiseLimit    = 20
	defaultAdvertiseMaxChars = 2000
	defaultAdvertiseTTL      = 300 // seconds
)

// ---- local layout ----

// projectDir is the directory project-scoped skills live under.
func (rt *runtime) projectDir() string {
	if rt.opts.Worktree != "" {
		return rt.opts.Worktree
	}
	return rt.opts.Directory
}

// globalSkillBase is the global root gocode's bootStack hands to
// skill.Discover (filepath.Join(global.Resolve().Config, "gocode")) — the
// place a globally loaded skill must land to be discovered next session.
func globalSkillBase() string {
	return filepath.Join(global.Resolve().Config, "gocode")
}

// skillRoot is the folder skills of scope are written into: <base>/skills,
// one of the layouts skill.Scan discovers.
func (rt *runtime) skillRoot(scope string) (string, error) {
	switch scope {
	case scopeProject:
		dir := rt.projectDir()
		if dir == "" {
			return "", fmt.Errorf("no project directory is known; use scope %q", scopeGlobal)
		}
		return filepath.Join(dir, ".gocode", "skills"), nil
	case scopeGlobal:
		return filepath.Join(globalSkillBase(), "skills"), nil
	default:
		return "", fmt.Errorf("scope must be %q or %q, got %q", scopeProject, scopeGlobal, scope)
	}
}

func (rt *runtime) scope(s string) string {
	if s != "" {
		return s
	}
	if rt.opts.SkillsDefaultScope != "" {
		return rt.opts.SkillsDefaultScope
	}
	return scopeProject
}

// localSkill is one skill found on disk.
type localSkill struct {
	Info  skill.Info
	Scope string // scopeProject | scopeGlobal
}

// localSkills indexes on-disk skills by name, using the same roots and
// precedence as gocode's bootStack: project roots first, so a project
// skill shadows a global one.
func (rt *runtime) localSkills() map[string]localSkill {
	type root struct{ dir, scope string }
	var roots []root
	if dir := rt.projectDir(); dir != "" {
		roots = append(roots, root{filepath.Join(dir, ".gocode"), scopeProject}, root{filepath.Join(dir, ".agents"), scopeProject})
	}
	roots = append(roots, root{globalSkillBase(), scopeGlobal}, root{filepath.Join(global.Resolve().Home, ".agents"), scopeGlobal})

	out := map[string]localSkill{}
	for _, r := range roots {
		for _, info := range skill.Scan(r.dir) {
			if _, seen := out[info.Name]; !seen {
				out[info.Name] = localSkill{Info: info, Scope: r.scope}
			}
		}
	}
	return out
}

// ---- local files ----

// localFile is one file of a local skill folder.
type localFile struct {
	Rel    string // slash-separated, relative to the skill root
	Abs    string
	Size   int64
	SHA256 string
}

// skillWalk is a walked local skill folder: the files that belong to the
// skill, and what was left out and why.
type skillWalk struct {
	Files    []localFile
	Skipped  []string
	Warnings []string
	Size     int64
}

var (
	skippedDirs = map[string]bool{".git": true, "node_modules": true, "__pycache__": true, ".venv": true}
	binaryExts  = map[string]bool{".exe": true, ".dll": true, ".so": true, ".dylib": true, ".o": true, ".a": true, ".pyc": true, ".class": true}
	// secretPatterns flag content that looks like a credential. A match is
	// a warning, not a refusal: the patterns are heuristics.
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\b(sk|gk|rk)_(live_|test_)?[A-Za-z0-9]{20,}\b`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
		regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|passwd|token)\b\s*[:=]\s*["']?[A-Za-z0-9/+_\-]{16,}`),
	}
)

// isCompiledBinary reports executables and object files, which never
// belong in a skill (a skill ships source scripts, not build output).
// Images and other assets are fine.
func isCompiledBinary(name string, head []byte) bool {
	if binaryExts[strings.ToLower(filepath.Ext(name))] {
		return true
	}
	for _, magic := range [][]byte{{0x7f, 'E', 'L', 'F'}, {0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0xca, 0xfe, 0xba, 0xbe}, {'M', 'Z'}} {
		if bytes.HasPrefix(head, magic) {
			return true
		}
	}
	return false
}

// isEnvFile matches .env, .env.local, .envrc — every .env* file.
func isEnvFile(name string) bool { return strings.HasPrefix(name, ".env") }

// walkLocalSkill walks dir and returns the files that make up the skill,
// hashed. It skips .git/node_modules and friends, .env* files, symlinks,
// compiled binaries, oversized files, and the sync marker, reporting each
// skip; a skill over the total size cap is an error.
func walkLocalSkill(dir string) (*skillWalk, error) {
	w := &skillWalk{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if skippedDirs[name] {
				w.Skipped = append(w.Skipped, rel+"/ (ignored directory)")
				return fs.SkipDir
			}
			return nil
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			w.Skipped = append(w.Skipped, rel+" (symlink)")
			return nil
		case !d.Type().IsRegular():
			w.Skipped = append(w.Skipped, rel+" (not a regular file)")
			return nil
		case name == syncMarkerName || name == ".DS_Store":
			return nil
		case isEnvFile(name):
			w.Skipped = append(w.Skipped, rel+" (.env files are never uploaded)")
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if info.Size() > maxSkillFileBytes {
			w.Skipped = append(w.Skipped, fmt.Sprintf("%s (%s exceeds the %s per-file limit)", rel, formatBytes(info.Size()), formatBytes(maxSkillFileBytes)))
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if isCompiledBinary(name, data) {
			w.Skipped = append(w.Skipped, rel+" (compiled binary)")
			return nil
		}
		if utf8.Valid(data) {
			for _, re := range secretPatterns {
				if re.Match(data) {
					w.Warnings = append(w.Warnings, rel+": looks like it contains a secret — check it before sharing")
					break
				}
			}
		}
		w.Files = append(w.Files, localFile{Rel: rel, Abs: p, Size: int64(len(data)), SHA256: sha256Hex(data)})
		w.Size += int64(len(data))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if w.Size > maxSkillBytes {
		return nil, fmt.Errorf("skill folder %s is %s, over the %s per-skill limit", dir, formatBytes(w.Size), formatBytes(maxSkillBytes))
	}
	sort.Slice(w.Files, func(i, j int) bool { return w.Files[i].Rel < w.Files[j].Rel })
	return w, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// localContentHash is gocoder.SkillContentHash over local files — equal
// to the remote skill's ContentHash exactly when both hold the same files.
func localContentHash(files []localFile) string {
	asRemote := make([]gocoder.LibrarySkillFile, len(files))
	for i, f := range files {
		asRemote[i] = gocoder.LibrarySkillFile{Path: f.Rel, SHA256: f.SHA256}
	}
	return gocoder.SkillContentHash(asRemote)
}

// syncMarker is the content of syncMarkerName.
type syncMarker struct {
	ContentHash string    `json:"contentHash"`
	SyncedAt    time.Time `json:"syncedAt"`
}

func readSyncMarker(dir string) *syncMarker {
	data, err := os.ReadFile(filepath.Join(dir, syncMarkerName))
	if err != nil {
		return nil
	}
	var m syncMarker
	if json.Unmarshal(data, &m) != nil || m.ContentHash == "" {
		return nil
	}
	return &m
}

func writeSyncMarker(dir, contentHash string) error {
	data, err := json.MarshalIndent(syncMarker{ContentHash: contentHash, SyncedAt: time.Now().UTC()}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, syncMarkerName), append(data, '\n'), 0o644)
}

// ---- path safety ----

// validateSkillName mirrors gocode-infra's library.ValidateSkillName, and
// doubles as the local-path guard: a name is used as a folder name.
func validateSkillName(name string) error {
	if name == "" || len(name) > 64 || name[0] == '.' {
		return fmt.Errorf("invalid skill name %q", name)
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return fmt.Errorf("invalid skill name %q (use letters, digits, '-', '_' or '.')", name)
		}
	}
	return nil
}

// cleanSkillFile validates a path relative to a skill root, as received
// from the library or a tool argument: slash-separated, relative, no ".."
// or empty segments. Anything else is rejected rather than cleaned — a
// path that needs cleaning did not come from a well-formed skill.
func cleanSkillFile(file string) (string, error) {
	f := strings.ReplaceAll(strings.TrimSpace(file), "\\", "/")
	if f == "" {
		return "", fmt.Errorf("file is required")
	}
	if strings.HasPrefix(f, "/") || filepath.IsAbs(file) || (len(f) > 1 && f[1] == ':') {
		return "", fmt.Errorf("file %q must be relative to the skill folder", file)
	}
	for _, seg := range strings.Split(f, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", fmt.Errorf("file %q is not a plain path inside the skill folder", file)
		}
	}
	return f, nil
}

// safeJoin joins a validated skill-relative file onto root and refuses
// the result if any existing component between root and the target is a
// symlink — writing through one could land outside root.
func safeJoin(root, file string) (string, error) {
	f, err := cleanSkillFile(file)
	if err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(f))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file %q escapes %s", file, root)
	}
	cur := root
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, seg)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing to write through symlink %s", cur)
		}
	}
	return target, nil
}

// writeFileAtomic writes data to target via a temp file and rename, so an
// interrupted load never leaves a half-written script behind.
func writeFileAtomic(target string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".tmp-"+filepath.Base(target)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// fileMode makes scripts executable: anything under scripts/ or starting
// with a shebang.
func fileMode(file string, data []byte) fs.FileMode {
	if strings.HasPrefix(file, "scripts/") || bytes.HasPrefix(data, []byte("#!")) {
		return 0o755
	}
	return 0o644
}

// ---- remote helpers ----

func skillLibraryPath(name, file string) string {
	if file == "" {
		return skillsRoot + "/" + name
	}
	return skillsRoot + "/" + name + "/" + file
}

// requireRemoteSkill fetches a skill's metadata, turning "no such skill"
// into an error that points at discovery.
func requireRemoteSkill(ctx context.Context, rt *runtime, name string) (*gocoder.LibrarySkill, error) {
	if err := validateSkillName(name); err != nil {
		return nil, err
	}
	s, err := rt.client.GetLibrarySkill(ctx, rt.bearer, name)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("no skill named %q in the library; find one with library_skill_search or library_skill_list", name)
	}
	return s, nil
}

func findRemoteFile(s *gocoder.LibrarySkill, file string) (gocoder.LibrarySkillFile, bool) {
	for _, f := range s.Files {
		if f.Path == file {
			return f, true
		}
	}
	return gocoder.LibrarySkillFile{}, false
}

// downloadVerified downloads one remote file and checks it against the
// sha256 the library reported, so a truncated or swapped transfer never
// reaches disk.
func downloadVerified(ctx context.Context, rt *runtime, f gocoder.LibrarySkillFile) ([]byte, error) {
	data, err := rt.client.DownloadLibraryRaw(ctx, rt.bearer, f.ID)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", f.Path, err)
	}
	if f.SHA256 != "" && sha256Hex(data) != f.SHA256 {
		return nil, fmt.Errorf("download %s: content does not match the library's sha256", f.Path)
	}
	return data, nil
}

// skillRole mirrors gocode-infra's library.SkillRole closely enough for
// display: SKILL.md, references, scripts, assets.
func skillRole(file string) string {
	ext := strings.ToLower(path.Ext(file))
	switch {
	case file == skillFileName:
		return "skill_md"
	case strings.HasPrefix(file, "references/"), ext == ".md", ext == ".markdown", ext == ".txt":
		return "reference"
	case strings.HasPrefix(file, "scripts/"):
		return "script"
	default:
		return "asset"
	}
}

// stripFrontmatter drops a leading YAML frontmatter block.
func stripFrontmatter(content string) string {
	doc := mddoc.Parse(content)
	span := doc.Frontmatter.Span()
	if span.End <= span.Start {
		return content
	}
	lines := strings.SplitAfter(content, "\n")
	if span.End >= len(lines) {
		return ""
	}
	return strings.TrimLeft(strings.Join(lines[span.End:], ""), "\n")
}

func formatFileIndex(files []gocoder.LibrarySkillFile) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	for _, f := range files {
		fmt.Fprintf(w, "  %s\t%s\t%s\n", f.Path, skillRole(f.Path), formatBytes(f.SizeBytes))
	}
	w.Flush()
	return b.String()
}

// ---- library_skill_search ----

func handleSkillSearch(ctx context.Context, rt *runtime, query, mode, skillName, role string, k int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("library_skill_search: query is required")
	}
	if mode == "" {
		mode = gocoder.SkillSearchDiscover
	}
	if mode != gocoder.SkillSearchDiscover && mode != gocoder.SkillSearchContent {
		return "", fmt.Errorf("library_skill_search: mode must be %q or %q", gocoder.SkillSearchDiscover, gocoder.SkillSearchContent)
	}
	if k <= 0 {
		k = 5
	}
	hits, err := rt.client.SearchLibrarySkills(ctx, rt.bearer, gocoder.SkillSearchRequest{
		Query: query, Mode: mode, Skill: skillName, Role: role, K: k, PerSkill: 3,
	})
	if err != nil {
		return "", err
	}
	return formatSkillHits(mode, hits, rt.localSkills()), nil
}

func formatSkillHits(mode string, hits []gocoder.LibrarySkillHit, local map[string]localSkill) string {
	if len(hits) == 0 {
		return "no matching skills"
	}
	var b strings.Builder
	for i, hit := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		installed := ""
		if l, ok := local[hit.Skill]; ok {
			installed = fmt.Sprintf("  [installed: %s]", l.Scope)
		}
		if mode == gocoder.SkillSearchDiscover {
			fmt.Fprintf(&b, "%d. %s  (score %.3f)%s", i+1, hit.Skill, hit.Score, installed)
			if hit.Description != "" {
				fmt.Fprintf(&b, "\n   %s", hit.Description)
			}
			continue
		}
		fmt.Fprintf(&b, "## %s  (score %.3f)%s", hit.Skill, hit.Score, installed)
		if hit.Description != "" {
			fmt.Fprintf(&b, "\n%s", hit.Description)
		}
		for _, c := range hit.Chunks {
			fmt.Fprintf(&b, "\n\n%s:%d-%d  [%s] (score %.3f)", skillLibraryPath(hit.Skill, c.File), c.StartLine, c.EndLine, c.Role, c.Score)
			if len(c.HeadingPath) > 0 {
				fmt.Fprintf(&b, "  [%s]", strings.Join(c.HeadingPath, " > "))
			}
			b.WriteString("\n")
			b.WriteString(c.Content)
		}
	}
	if mode == gocoder.SkillSearchDiscover {
		b.WriteString("\n\nRead one with library_skill_use (no install needed).")
	}
	return b.String()
}

// ---- library_skill_use ----

func handleSkillUse(ctx context.Context, rt *runtime, name string) (string, error) {
	s, err := requireRemoteSkill(ctx, rt, name)
	if err != nil {
		return "", err
	}
	md, ok := findRemoteFile(s, skillFileName)
	if !ok {
		return "", fmt.Errorf("skill %q has no %s in the library", name, skillFileName)
	}
	data, err := downloadVerified(ctx, rt, md)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "<skill_content name=%q source=\"library\">\n", s.Name)
	fmt.Fprintf(&b, "# Skill: %s\n\n", s.Name)
	b.WriteString(strings.TrimSpace(stripFrontmatter(string(data))))
	b.WriteString("\n\n")
	if len(s.Files) > 1 {
		b.WriteString("Skill files (relative links in the skill point at these):\n")
		var others []gocoder.LibrarySkillFile
		for _, f := range s.Files {
			if f.Path != skillFileName {
				others = append(others, f)
			}
		}
		b.WriteString(formatFileIndex(others))
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "This skill was read from the user's gocoder.org Library, not installed. Follow it for the current task. "+
		"Fetch a referenced file with library_skill_show name=%q file=\"<path>\"; to run a script, library_skill_show with materialize=true returns a local path. "+
		"Use library_skill_load only if the user wants the skill kept locally.\n", s.Name)
	b.WriteString("</skill_content>")
	return b.String(), nil
}

// ---- library_skill_show ----

// skillCacheDir is where materialized files live:
// <data dir>/library-skills/<name>@<hash>/<file>. Keying by the skill's
// content hash means an in-place remote update can never run a stale
// cached script.
func skillCacheDir(name, contentHash string) string {
	return filepath.Join(global.Resolve().Data, "library-skills", name+"@"+shortHash(contentHash))
}

func shortHash(h string) string {
	if len(h) > 16 {
		return h[:16]
	}
	if h == "" {
		return "unknown"
	}
	return h
}

// pruneSkillCache removes every cached <name>@<hash> directory except keep.
func pruneSkillCache(name, keep string) {
	base := filepath.Dir(keep)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join(base, e.Name())
		if e.IsDir() && strings.HasPrefix(e.Name(), name+"@") && full != keep {
			_ = os.RemoveAll(full)
		}
	}
}

func handleSkillShow(ctx context.Context, rt *runtime, name, file string, tree, materialize bool) (string, error) {
	s, err := requireRemoteSkill(ctx, rt, name)
	if err != nil {
		return "", err
	}
	if tree || file == "" {
		var b strings.Builder
		fmt.Fprintf(&b, "%s — %d files, %s, updated %s, status %s\n", s.Name, s.FileCount, formatBytes(s.SizeBytes), s.UpdatedAt.Format(time.RFC3339), s.Status)
		b.WriteString(formatFileIndex(s.Files))
		return strings.TrimRight(b.String(), "\n"), nil
	}
	file, err = cleanSkillFile(file)
	if err != nil {
		return "", fmt.Errorf("library_skill_show: %w", err)
	}
	f, ok := findRemoteFile(s, file)
	if !ok {
		return "", fmt.Errorf("skill %q has no file %q; list its files with library_skill_show tree=true", name, file)
	}
	data, err := downloadVerified(ctx, rt, f)
	if err != nil {
		return "", err
	}

	if materialize {
		dir := skillCacheDir(s.Name, s.ContentHash)
		target, err := safeJoin(dir, file)
		if err != nil {
			return "", err
		}
		if err := writeFileAtomic(target, data, fileMode(file, data)); err != nil {
			return "", fmt.Errorf("materialize %s: %w", file, err)
		}
		pruneSkillCache(s.Name, dir)
		return fmt.Sprintf("materialized %s (%s) at:\n%s\n\nThe cache is keyed by the skill's content hash; materialize sibling files the same way to place them next to it.",
			skillLibraryPath(s.Name, file), formatBytes(int64(len(data))), target), nil
	}

	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return fmt.Sprintf("%s is a binary file (%s); pass materialize=true to get a local path.", skillLibraryPath(s.Name, file), formatBytes(int64(len(data)))), nil
	}
	return fmt.Sprintf("%s\n\n%s", skillLibraryPath(s.Name, file), string(data)), nil
}

// ---- library_skill_list ----

// Local status values for library_skill_list.
const (
	statusNotInstalled = "not installed"
	statusOutdated     = "outdated"
	statusLocalChanges = "local changes"
	statusDiverged     = "local changes, outdated"
	statusDiffers      = "differs"
)

// localStatus compares a remote skill with its local copy, if any:
// installed and identical, or — via the sync marker — whether the library
// moved on (outdated), the local copy was edited (local changes), or both.
// Without a marker the direction is unknown and it only "differs".
func localStatus(remote gocoder.LibrarySkill, local map[string]localSkill) string {
	l, ok := local[remote.Name]
	if !ok || l.Info.Location == "" {
		return statusNotInstalled
	}
	dir := l.Info.Dir()
	walk, err := walkLocalSkill(dir)
	if err != nil {
		return fmt.Sprintf("installed (%s), unreadable: %v", l.Scope, err)
	}
	localHash := localContentHash(walk.Files)
	if localHash == remote.ContentHash {
		return fmt.Sprintf("installed (%s)", l.Scope)
	}
	marker := readSyncMarker(dir)
	switch {
	case marker == nil:
		return statusDiffers
	case marker.ContentHash == localHash:
		return statusOutdated
	case marker.ContentHash == remote.ContentHash:
		return statusLocalChanges
	default:
		return statusDiverged
	}
}

func handleSkillList(ctx context.Context, rt *runtime) (string, error) {
	skills, err := rt.client.ListLibrarySkills(ctx, rt.bearer)
	if err != nil {
		return "", err
	}
	if len(skills) == 0 {
		return "no skills in the library; store one with library_skill_store", nil
	}
	local := rt.localSkills()
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tFILES\tSIZE\tUPDATED\tLOCAL\tDESCRIPTION")
	for _, s := range skills {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\n", s.Name, s.FileCount, formatBytes(s.SizeBytes),
			s.UpdatedAt.Format("2006-01-02 15:04"), localStatus(s, local), truncate(s.Description, 80))
	}
	w.Flush()
	return strings.TrimRight(b.String(), "\n"), nil
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// ---- library_skill_load ----

func handleSkillLoad(ctx context.Context, rt *runtime, name, scope string, overwrite bool, files []string) (string, error) {
	s, err := requireRemoteSkill(ctx, rt, name)
	if err != nil {
		return "", err
	}
	scope = rt.scope(scope)
	root, err := rt.skillRoot(scope)
	if err != nil {
		return "", fmt.Errorf("library_skill_load: %w", err)
	}
	dest := filepath.Join(root, s.Name)

	selected := s.Files
	if len(files) > 0 {
		selected = nil
		for _, want := range files {
			clean, err := cleanSkillFile(want)
			if err != nil {
				return "", fmt.Errorf("library_skill_load: %w", err)
			}
			f, ok := findRemoteFile(s, clean)
			if !ok {
				return "", fmt.Errorf("library_skill_load: skill %q has no file %q", s.Name, want)
			}
			selected = append(selected, f)
		}
	}

	// Plan every write before making any: a refused overwrite must leave
	// the folder exactly as it was.
	type write struct {
		file   gocoder.LibrarySkillFile
		target string
		exists bool
	}
	var writes []write
	var conflicts []string
	unchanged := 0
	for _, f := range selected {
		target, err := safeJoin(dest, f.Path)
		if err != nil {
			return "", fmt.Errorf("library_skill_load: %w", err)
		}
		existing, rerr := os.ReadFile(target)
		switch {
		case rerr == nil && f.SHA256 != "" && sha256Hex(existing) == f.SHA256:
			unchanged++
			continue
		case rerr == nil:
			conflicts = append(conflicts, f.Path)
			writes = append(writes, write{f, target, true})
		case errors.Is(rerr, fs.ErrNotExist):
			writes = append(writes, write{f, target, false})
		default:
			return "", rerr
		}
	}
	if len(conflicts) > 0 && !overwrite {
		return "", fmt.Errorf("library_skill_load: %d local file(s) in %s differ from the library: %s — pass overwrite=true to replace them (check library_skill_diff first)",
			len(conflicts), dest, strings.Join(conflicts, ", "))
	}

	for _, wr := range writes {
		data, err := downloadVerified(ctx, rt, wr.file)
		if err != nil {
			return "", err
		}
		if err := writeFileAtomic(wr.target, data, fileMode(wr.file.Path, data)); err != nil {
			return "", fmt.Errorf("write %s: %w", wr.target, err)
		}
	}
	if len(files) == 0 {
		if err := writeSyncMarker(dest, s.ContentHash); err != nil {
			return "", err
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "loaded %s into %s (%s scope): %d written", s.Name, dest, scope, len(writes)-len(conflicts))
	if len(conflicts) > 0 {
		fmt.Fprintf(&b, ", %d overwritten", len(conflicts))
	}
	fmt.Fprintf(&b, ", %d already up to date.\n", unchanged)
	if extra := localOnlyFiles(dest, s); len(extra) > 0 && len(files) == 0 {
		fmt.Fprintf(&b, "Left alone (local only, not in the library): %s\n", strings.Join(extra, ", "))
	}
	b.WriteString(rescanNote(ctx, rt, s.Name))
	if body, err := os.ReadFile(filepath.Join(dest, skillFileName)); err == nil {
		fmt.Fprintf(&b, "\n\n<skill_content name=%q>\n%s\n</skill_content>", s.Name, strings.TrimSpace(stripFrontmatter(string(body))))
	}
	return b.String(), nil
}

// rescanTimeout bounds the host rescan request; a slow or absent host
// only costs the "next session" fallback, never a failed load.
const rescanTimeout = 5 * time.Second

// requestHostRescan asks the host to re-run skill discovery
// (POST /api/skill/rescan), so a skill just written to disk joins
// <available_skills> without a restart. ok is false when the host has no
// HTTP API or the request failed.
func requestHostRescan(ctx context.Context, rt *runtime) (added []string, ok bool) {
	if rt.opts.ServerURL == "" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, rescanTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(rt.opts.ServerURL, "/")+"/api/skill/rescan", nil)
	if err != nil {
		return nil, false
	}
	for k, v := range rt.opts.ServerHeaders {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var body struct {
		Added []string `json:"added"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil, false
	}
	return body.Added, true
}

// rescanNote triggers the host rescan and says what it achieved.
func rescanNote(ctx context.Context, rt *runtime, name string) string {
	if _, ok := requestHostRescan(ctx, rt); ok {
		return fmt.Sprintf("gocode rescanned its skills: %s is in <available_skills> from the next turn (loadable with the skill tool now).", name)
	}
	return "It appears in <available_skills> from the next session."
}

func localOnlyFiles(dir string, s *gocoder.LibrarySkill) []string {
	walk, err := walkLocalSkill(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, f := range walk.Files {
		if _, ok := findRemoteFile(s, f.Rel); !ok {
			out = append(out, f.Rel)
		}
	}
	return out
}

// ---- resolving a local skill (store, diff) ----

// resolvedLocal is a local skill folder ready to compare or upload.
type resolvedLocal struct {
	Name        string
	Description string
	Dir         string
	Walk        *skillWalk
}

// resolveLocalSkill finds a local skill by directory (dir) or by name
// (against the local skill roots) and validates its SKILL.md frontmatter:
// name and description are both required.
func resolveLocalSkill(rt *runtime, name, dir string) (*resolvedLocal, error) {
	var skillDir string
	switch {
	case dir != "":
		skillDir = rt.resolveLocalPath(dir)
		if strings.EqualFold(filepath.Base(skillDir), skillFileName) {
			skillDir = filepath.Dir(skillDir)
		}
	case name != "":
		l, ok := rt.localSkills()[name]
		if !ok {
			return nil, fmt.Errorf("no local skill named %q (looked in .gocode/ and .agents/ skill folders, project and global); pass dir instead", name)
		}
		skillDir = l.Info.Dir()
	default:
		return nil, fmt.Errorf("name or dir is required")
	}

	skillMD := filepath.Join(skillDir, skillFileName)
	if _, err := os.Stat(skillMD); err != nil {
		return nil, fmt.Errorf("%s has no %s — only folder skills can be stored", skillDir, skillFileName)
	}
	info, err := skill.Load(skillMD)
	if err != nil {
		return nil, fmt.Errorf("invalid frontmatter: %w", err)
	}
	if strings.TrimSpace(info.Description) == "" {
		return nil, fmt.Errorf("%s: frontmatter needs a description — it is how the skill is found", skillMD)
	}
	if err := validateSkillName(info.Name); err != nil {
		return nil, fmt.Errorf("%s: %w", skillMD, err)
	}
	if name != "" && dir != "" && name != info.Name {
		return nil, fmt.Errorf("%s declares name %q, not %q", skillMD, info.Name, name)
	}
	walk, err := walkLocalSkill(skillDir)
	if err != nil {
		return nil, err
	}
	return &resolvedLocal{Name: info.Name, Description: info.Description, Dir: skillDir, Walk: walk}, nil
}

// checkSkillLinks resolves every relative link in the skill's markdown
// files and reports those that point at no file of the skill, or out of
// the skill folder entirely — a link that works locally but not from the
// library copy.
func checkSkillLinks(walk *skillWalk) []string {
	present := map[string]bool{}
	dirs := map[string]bool{}
	for _, f := range walk.Files {
		present[f.Rel] = true
		for d := path.Dir(f.Rel); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	var problems []string
	for _, f := range walk.Files {
		ext := strings.ToLower(path.Ext(f.Rel))
		if ext != ".md" && ext != ".markdown" {
			continue
		}
		data, err := os.ReadFile(f.Abs)
		if err != nil {
			continue
		}
		doc := mddoc.Parse(string(data))
		fileDir := path.Dir(f.Rel)
		if fileDir == "." {
			fileDir = ""
		}
		for _, link := range doc.Links {
			if link.Kind != mddoc.LinkFile && link.Kind != mddoc.LinkWiki {
				continue
			}
			line := doc.LineOfByte(link.StartByte) + 1
			dest := mddoc.UnescapeDestination(link.Destination)
			if link.Kind == mddoc.LinkFile && strings.HasPrefix(dest, "/") {
				problems = append(problems, fmt.Sprintf("%s:%d: absolute link %q will not resolve from the library", f.Rel, line, link.Destination))
				continue
			}
			res := link.Resolve(fileDir, nil)
			target := res.File
			if unescaped, err := url.PathUnescape(target); err == nil {
				target = unescaped
			}
			target = path.Clean(target)
			switch {
			case res.File == "":
				continue // same-document anchor
			case target == ".." || strings.HasPrefix(target, "../"):
				problems = append(problems, fmt.Sprintf("%s:%d: link %q points outside the skill folder", f.Rel, line, link.Destination))
			case !present[target] && !dirs[strings.TrimSuffix(target, "/")]:
				problems = append(problems, fmt.Sprintf("%s:%d: broken link %q (no %s in the skill)", f.Rel, line, link.Destination, target))
			}
		}
	}
	return problems
}

// ---- library_skill_diff ----

// skillDiff is a file-by-file comparison of a local skill with its remote
// copy.
type skillDiff struct {
	Added     []localFile                // local only
	Changed   []localFile                // both, different content
	Removed   []gocoder.LibrarySkillFile // remote only
	Unchanged int
}

func diffSkill(local *resolvedLocal, remote *gocoder.LibrarySkill) skillDiff {
	var d skillDiff
	remoteByPath := map[string]gocoder.LibrarySkillFile{}
	if remote != nil {
		for _, f := range remote.Files {
			remoteByPath[f.Path] = f
		}
	}
	for _, f := range local.Walk.Files {
		r, ok := remoteByPath[f.Rel]
		switch {
		case !ok:
			d.Added = append(d.Added, f)
		case r.SHA256 != f.SHA256: // an empty remote hash (pre-sha256 upload) counts as changed
			d.Changed = append(d.Changed, f)
		default:
			d.Unchanged++
		}
		delete(remoteByPath, f.Rel)
	}
	for _, r := range remoteByPath {
		d.Removed = append(d.Removed, r)
	}
	sort.Slice(d.Removed, func(i, j int) bool { return d.Removed[i].Path < d.Removed[j].Path })
	return d
}

func (d skillDiff) empty() bool { return len(d.Added)+len(d.Changed)+len(d.Removed) == 0 }

func (d skillDiff) format() string {
	var b strings.Builder
	for _, f := range d.Added {
		fmt.Fprintf(&b, "  added    %s  (%s, local only)\n", f.Rel, formatBytes(f.Size))
	}
	for _, f := range d.Changed {
		fmt.Fprintf(&b, "  changed  %s\n", f.Rel)
	}
	for _, f := range d.Removed {
		fmt.Fprintf(&b, "  removed  %s  (library only)\n", f.Path)
	}
	fmt.Fprintf(&b, "  %d unchanged", d.Unchanged)
	return b.String()
}

func handleSkillDiff(ctx context.Context, rt *runtime, name, dir string) (string, error) {
	local, err := resolveLocalSkill(rt, name, dir)
	if err != nil {
		return "", fmt.Errorf("library_skill_diff: %w", err)
	}
	remote, err := rt.client.GetLibrarySkill(ctx, rt.bearer, local.Name)
	if err != nil {
		return "", err
	}
	if remote == nil {
		return fmt.Sprintf("%s (%s) is not in the library; every one of its %d files would be uploaded by library_skill_store.", local.Name, local.Dir, len(local.Walk.Files)), nil
	}
	d := diffSkill(local, remote)
	if d.empty() {
		return fmt.Sprintf("%s: local (%s) and library copies are identical (%d files).", local.Name, local.Dir, d.Unchanged), nil
	}
	return fmt.Sprintf("%s: local %s vs library %s\n%s", local.Name, local.Dir, skillLibraryPath(local.Name, ""), d.format()), nil
}

// ---- library_skill_store ----

func handleSkillStore(ctx context.Context, rt *runtime, name, dir string, overwrite, dryRun, wait bool, timeoutSeconds int) (string, error) {
	local, err := resolveLocalSkill(rt, name, dir)
	if err != nil {
		return "", fmt.Errorf("library_skill_store: %w", err)
	}
	remote, err := rt.client.GetLibrarySkill(ctx, rt.bearer, local.Name)
	if err != nil {
		return "", err
	}
	d := diffSkill(local, remote)
	linkProblems := checkSkillLinks(local.Walk)

	var b strings.Builder
	verb := "stored"
	if dryRun {
		verb = "would store"
	}
	fmt.Fprintf(&b, "%s %s from %s to %s\n", verb, local.Name, local.Dir, skillLibraryPath(local.Name, ""))
	fmt.Fprintf(&b, "%d upload(s), %d replacement(s), %d deletion(s), %d unchanged\n", len(d.Added), len(d.Changed), len(d.Removed), d.Unchanged)
	if !d.empty() {
		b.WriteString(d.format())
		b.WriteString("\n")
	}
	appendList(&b, "Skipped", local.Walk.Skipped)
	appendList(&b, "Warnings", local.Walk.Warnings)
	appendList(&b, "Link problems", linkProblems)

	needsOverwrite := len(d.Changed) > 0 || len(d.Removed) > 0
	if needsOverwrite && !overwrite {
		if dryRun {
			b.WriteString("Replacing and deleting library files requires overwrite=true.")
			return strings.TrimRight(b.String(), "\n"), nil
		}
		return "", fmt.Errorf("library_skill_store: %s already exists in the library and differs (%d changed, %d library-only file(s)); pass overwrite=true to make the library copy match exactly — library-only files are deleted. Review with library_skill_diff",
			local.Name, len(d.Changed), len(d.Removed))
	}
	if dryRun {
		return strings.TrimRight(b.String(), "\n"), nil
	}
	if d.empty() {
		_ = writeSyncMarker(local.Dir, localContentHash(local.Walk.Files))
		return fmt.Sprintf("%s is already identical in the library (%d files); nothing to upload.", local.Name, d.Unchanged), nil
	}

	// SKILL.md goes first, so its node (and the description it carries)
	// exists before the other files are indexed and look it up.
	uploads := append(append([]localFile(nil), d.Added...), d.Changed...)
	sort.SliceStable(uploads, func(i, j int) bool { return uploads[i].Rel == skillFileName && uploads[j].Rel != skillFileName })
	changed := map[string]bool{}
	for _, f := range d.Changed {
		changed[f.Rel] = true
	}
	for _, f := range uploads {
		data, err := os.ReadFile(f.Abs)
		if err != nil {
			return "", err
		}
		if sha256Hex(data) != f.SHA256 {
			return "", fmt.Errorf("library_skill_store: %s changed while storing; run it again", f.Rel)
		}
		libPath := skillLibraryPath(local.Name, f.Rel)
		if err := ensureAncestorFolders(ctx, rt, libPath); err != nil {
			return "", fmt.Errorf("library_skill_store: %w", err)
		}
		if _, err := rt.client.UploadLibraryFileWith(ctx, rt.bearer, libPath, path.Base(f.Rel), data, gocoder.UploadOptions{Overwrite: changed[f.Rel]}); err != nil {
			return "", fmt.Errorf("library_skill_store: upload %s: %w", f.Rel, err)
		}
	}
	for _, f := range d.Removed {
		if err := rt.client.DeleteLibraryNode(ctx, rt.bearer, f.ID); err != nil {
			return "", fmt.Errorf("library_skill_store: delete %s: %w", f.Path, err)
		}
	}
	if err := writeSyncMarker(local.Dir, localContentHash(local.Walk.Files)); err != nil {
		return "", err
	}
	advertised.invalidate()

	if wait {
		if timeoutSeconds <= 0 {
			timeoutSeconds = rt.opts.UploadTimeout
		}
		status, err := waitSkillIndexed(ctx, rt, local.Name, time.Duration(timeoutSeconds)*time.Second)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "index status: %s", status)
	} else {
		b.WriteString("indexing runs in the background; check with library_skill_list.")
	}
	return b.String(), nil
}

func appendList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, item := range items {
		fmt.Fprintf(b, "  %s\n", item)
	}
}

// waitSkillIndexed polls the skill's aggregate status until it is ready or
// failed, or the timeout elapses.
func waitSkillIndexed(ctx context.Context, rt *runtime, name string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		s, err := rt.client.GetLibrarySkill(ctx, rt.bearer, name)
		if err != nil {
			return "", err
		}
		if s != nil && (s.Status == gocoder.LibraryStatusReady || s.Status == gocoder.LibraryStatusFailed) {
			return s.Status, nil
		}
		if time.Now().After(deadline) {
			return "still indexing after the wait timeout", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(uploadPollInterval):
		}
	}
}

// ---- library_skill_delete ----

func handleSkillDelete(ctx context.Context, rt *runtime, name string, confirm bool) (string, error) {
	s, err := requireRemoteSkill(ctx, rt, name)
	if err != nil {
		return "", err
	}
	if !confirm {
		return "", fmt.Errorf("library_skill_delete: deleting %s removes %d file(s) (%s) from the library permanently; ask the user, then call again with confirm=true",
			s.Name, s.FileCount, formatBytes(s.SizeBytes))
	}
	// Deleting the skill's folder node removes everything under it in one
	// call. Files whose folder node was never created are deleted one by
	// one — whatever the folder delete left behind.
	children, err := rt.client.ListLibraryNodes(ctx, rt.bearer, skillsRoot)
	if err != nil {
		return "", err
	}
	for _, n := range children {
		if n.Name == s.Name && n.Type == gocoder.LibraryTypeFolder {
			if err := rt.client.DeleteLibraryNode(ctx, rt.bearer, n.ID); err != nil {
				return "", err
			}
		}
	}
	remaining, err := rt.client.ListLibraryTree(ctx, rt.bearer, skillLibraryPath(s.Name, ""))
	if err != nil {
		return "", err
	}
	for _, n := range remaining {
		if n.Type == gocoder.LibraryTypeFile {
			if err := rt.client.DeleteLibraryNode(ctx, rt.bearer, n.ID); err != nil {
				return "", err
			}
		}
	}
	advertised.invalidate()
	return fmt.Sprintf("deleted %s from the library (%d file(s)). Local copies, if any, are untouched.", s.Name, s.FileCount), nil
}

// ---- system.transform: advertising remote skills ----

// advertiseEnabled resolves skillsAdvertise, with the env override winning
// for a single run.
func advertiseEnabled(opts runtimeOptions) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(advertiseEnv))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return opts.SkillsAdvertise
}

// advertiseCache holds the last good remote skill list between turns.
type advertiseCache struct {
	mu        sync.Mutex
	skills    []gocoder.LibrarySkill
	fetchedAt time.Time
	failedAt  time.Time
	now       func() time.Time
}

var advertised = &advertiseCache{now: time.Now}

func (c *advertiseCache) invalidate() {
	c.mu.Lock()
	c.fetchedAt, c.failedAt = time.Time{}, time.Time{}
	c.mu.Unlock()
}

// list returns the remote skills to advertise. A fresh list (younger than
// ttl) is served from cache; a stale one is refetched with a short
// timeout. A failed fetch contributes nothing and is not retried until ttl
// passes, so an offline or signed-out session never pays the timeout on
// every turn.
func (c *advertiseCache) list(ttl time.Duration, fetch func(context.Context) ([]gocoder.LibrarySkill, error)) []gocoder.LibrarySkill {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < ttl {
		return c.skills
	}
	if !c.failedAt.IsZero() && now.Sub(c.failedAt) < ttl {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), advertiseFetchTimeout)
	defer cancel()
	skills, err := fetch(ctx)
	if err != nil {
		c.failedAt = now
		c.skills, c.fetchedAt = nil, time.Time{}
		return nil
	}
	c.skills, c.fetchedAt, c.failedAt = skills, now, time.Time{}
	return skills
}

// advertiseBlock renders the remote-skills block appended to the system
// prompt: skills not installed locally, at most limit of them, the whole
// block within maxChars. Empty when there is nothing to advertise.
func advertiseBlock(skills []gocoder.LibrarySkill, installed map[string]bool, limit, maxChars int) string {
	const header = "<library_skills>\n" +
		"Skills in the user's gocoder.org Library, not installed locally. When one fits the task, read it with library_skill_use (no install needed) and follow it.\n"
	const footer = "</library_skills>"
	var b strings.Builder
	b.WriteString(header)
	count := 0
	for _, s := range skills {
		if count >= limit {
			break
		}
		if installed[s.Name] || s.Description == "" {
			continue
		}
		line := fmt.Sprintf("- %s: %s\n", s.Name, truncate(s.Description, 200))
		if b.Len()+len(line)+len(footer) > maxChars {
			break
		}
		b.WriteString(line)
		count++
	}
	if count == 0 {
		return ""
	}
	b.WriteString(footer)
	return b.String()
}

// installedSkillNames is every skill the host already lists in
// <available_skills>: on disk in any root, or compiled into gocode.
func (rt *runtime) installedSkillNames() map[string]bool {
	out := map[string]bool{}
	for name := range rt.localSkills() {
		out[name] = true
	}
	for _, info := range skill.Builtins() {
		out[info.Name] = true
	}
	return out
}

// handleSystemTransform appends the remote-skills block to the system
// prompt. It never fails a turn: any problem (no account, offline, a bad
// response) means the block is simply left out.
func handleSystemTransform(output map[string]any) map[string]any {
	opts := pendingOptions()
	if opts == nil || !advertiseEnabled(*opts) {
		return output
	}
	rt, err := ensureRuntime()
	if err != nil {
		return output
	}
	ttl := time.Duration(rt.opts.SkillsAdvertiseTTL) * time.Second
	skills := advertised.list(ttl, func(ctx context.Context) ([]gocoder.LibrarySkill, error) {
		return rt.client.ListLibrarySkills(ctx, rt.bearer)
	})
	block := advertiseBlock(skills, rt.installedSkillNames(), rt.opts.SkillsAdvertiseLimit, rt.opts.SkillsAdvertiseMaxChars)
	if block == "" {
		return output
	}
	system, _ := output["system"].([]any)
	output["system"] = append(system, block)
	return output
}

func pendingOptions() *runtimeOptions {
	rtMu.Lock()
	defer rtMu.Unlock()
	return pending
}

// ---- tool manifest ----

func skillToolSchemas() []map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	nameParam := str("Skill name, as shown by library_skill_search or library_skill_list.")
	return []map[string]any{
		{
			"name": "library_skill_search",
			"description": "Search skills stored in the user's gocoder.org Library. Use this when no skill in <available_skills> fits the task: " +
				"mode=discover (default) answers \"which skill fits this task\"; mode=content searches inside skill files and returns the best-matching chunks grouped per skill, cited as .skills/<name>/<file>:L-L. " +
				"Next step: library_skill_use to read the skill — no install needed.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": str("The task or topic, in natural language."),
					"mode":  map[string]any{"type": "string", "enum": []string{"discover", "content"}, "description": "discover (default): rank skills. content: rank chunks inside skill files."},
					"skill": str("content mode: only search this skill."),
					"role":  map[string]any{"type": "string", "enum": []string{"skill_md", "reference", "script", "asset"}, "description": "content mode: only search files of this role."},
					"k":     integer("Max skills returned. Default 5."),
				},
				"required": []string{"query"},
			},
		},
		{
			"name": "library_skill_use",
			"description": "Read a skill from the user's gocoder.org Library and use it in the current turn: returns its SKILL.md inline plus an index of its files. Writes nothing to disk. " +
				"Follow the skill's instructions; fetch files it links to with library_skill_show, and scripts you need to run with library_skill_show materialize=true.",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"name": nameParam},
				"required":   []string{"name"},
			},
		},
		{
			"name": "library_skill_show",
			"description": "Fetch one file of a library skill (e.g. a reference a SKILL.md links to), or its file listing with tree=true. " +
				"With materialize=true the file is written to a local cache keyed by the skill's content hash and its local path is returned — how to run a skill's script without installing the whole skill.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        nameParam,
					"file":        str("Path relative to the skill folder, e.g. \"references/forms.md\" or \"scripts/fill.py\"."),
					"tree":        boolean("List the skill's files instead of fetching one."),
					"materialize": boolean("Write the file to the local cache and return its path instead of its content. Use for scripts and binary assets."),
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "library_skill_list",
			"description": "List the skills in the user's gocoder.org Library with description, file count, size, last update, and local status: not installed, installed (project/global), outdated, local changes. Read-only.",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name": "library_skill_load",
			"description": "Download a library skill into a local skill folder so it is kept and appears in <available_skills> from the next session; returns its SKILL.md inline for use now. " +
				"Only when the user wants the skill kept — to just use it, call library_skill_use. Refuses to replace differing local files unless overwrite=true.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":      nameParam,
					"scope":     map[string]any{"type": "string", "enum": []string{"project", "global"}, "description": "project: .gocode/skills/ in this project (default). global: the user's global gocode skills folder."},
					"overwrite": boolean("Replace local files that differ from the library copy. Ask the user first."),
					"files":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only fetch these files (paths relative to the skill folder)."},
				},
				"required": []string{"name"},
			},
		},
		{
			"name": "library_skill_store",
			"description": "Upload a local skill folder (SKILL.md with name and description frontmatter, plus references/, scripts/ and other files) to the user's gocoder.org Library under .skills/<name>/. " +
				"Checks that relative links resolve inside the skill, skips .git/node_modules/.env*/binaries, and warns on anything that looks like a secret. " +
				"Updating an existing library skill needs overwrite=true, which also deletes library files no longer present locally. Preview with dryRun.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":      str("Local skill name (looked up in the project and global skill folders)."),
					"dir":       str("Skill folder path instead of a name, relative to the project or absolute."),
					"overwrite": boolean("Replace changed library files and delete library files missing locally, so the library copy matches exactly."),
					"dryRun":    boolean("Report uploads, replacements, deletions and link problems without changing anything."),
					"wait":      boolean("Wait for indexing to finish before replying. Default true."),
					"timeout":   integer("With wait, how long to wait in seconds. Default 120."),
				},
			},
		},
		{
			"name":        "library_skill_diff",
			"description": "Compare a local skill folder with its library copy file by file: added (local only), removed (library only), changed. Read-only.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": str("Local skill name."),
					"dir":  str("Skill folder path instead of a name."),
				},
			},
		},
		{
			"name":        "library_skill_delete",
			"description": "Permanently delete a skill from the user's gocoder.org Library. IRREVERSIBLE. Local copies are not touched. Requires confirm=true — ask the user first.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":    nameParam,
					"confirm": boolean("Must be true to delete. Ask the user first."),
				},
				"required": []string{"name", "confirm"},
			},
		},
	}
}

// dispatchSkillTool runs a library_skill_* tool; handled is false for any
// other tool name.
func dispatchSkillTool(ctx context.Context, rt *runtime, name string, args map[string]any) (output string, handled bool, err error) {
	switch name {
	case "library_skill_search":
		output, err = handleSkillSearch(ctx, rt, stringOpt(args, "query", ""), stringOpt(args, "mode", ""), stringOpt(args, "skill", ""), stringOpt(args, "role", ""), intOpt(args, "k", 0))
	case "library_skill_use":
		output, err = handleSkillUse(ctx, rt, stringOpt(args, "name", ""))
	case "library_skill_show":
		output, err = handleSkillShow(ctx, rt, stringOpt(args, "name", ""), stringOpt(args, "file", ""), boolOpt(args, "tree", false), boolOpt(args, "materialize", false))
	case "library_skill_list":
		output, err = handleSkillList(ctx, rt)
	case "library_skill_load":
		output, err = handleSkillLoad(ctx, rt, stringOpt(args, "name", ""), stringOpt(args, "scope", ""), boolOpt(args, "overwrite", false), stringsOpt(args, "files"))
	case "library_skill_store":
		output, err = handleSkillStore(ctx, rt, stringOpt(args, "name", ""), stringOpt(args, "dir", ""), boolOpt(args, "overwrite", false), boolOpt(args, "dryRun", false), boolOpt(args, "wait", true), intOpt(args, "timeout", 0))
	case "library_skill_diff":
		output, err = handleSkillDiff(ctx, rt, stringOpt(args, "name", ""), stringOpt(args, "dir", ""))
	case "library_skill_delete":
		output, err = handleSkillDelete(ctx, rt, stringOpt(args, "name", ""), boolOpt(args, "confirm", false))
	default:
		return "", false, nil
	}
	return output, true, err
}

func stringsOpt(m map[string]any, key string) []string {
	raw, _ := m[key].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
