package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	goruntime "runtime" // this package has its own runtime type
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/gocoder"
	"github.com/langazov/gocode-go/internal/plugin"
	"github.com/langazov/gocode-go/internal/server"
	"github.com/langazov/gocode-go/internal/skill"
)

// ---- fake server: skill routes ----

func (s *fakeLibraryServer) nodeAt(p string) *fakeNode {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.nodes {
		if n.Path == p {
			return n
		}
	}
	return nil
}

// fakeDescription stands in for gocode-infra parsing SKILL.md frontmatter
// at upload time: the value of a single-line "description:" key.
func fakeDescription(p string, data []byte) string {
	if path.Base(p) != skillFileName {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "description:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// skillInfos groups the fake's .skills/ file nodes into skills, the way
// gocode-infra's BuildSkillInfos does. Caller holds s.mu.
func (s *fakeLibraryServer) skillInfos() []gocoder.LibrarySkill {
	byName := map[string]*gocoder.LibrarySkill{}
	for _, n := range s.nodes {
		rest, ok := strings.CutPrefix(n.Path, skillsRoot+"/")
		if !ok || n.Type != gocoder.LibraryTypeFile {
			continue
		}
		name, file, _ := strings.Cut(rest, "/")
		info := byName[name]
		if info == nil {
			info = &gocoder.LibrarySkill{Name: name, Status: gocoder.LibraryStatusReady}
			byName[name] = info
		}
		info.Files = append(info.Files, gocoder.LibrarySkillFile{Path: file, ID: n.ID, SizeBytes: n.SizeBytes, SHA256: n.SHA256, Status: n.Status})
		info.SizeBytes += n.SizeBytes
		if file == skillFileName {
			info.Description = n.Description
		}
		if n.Status != gocoder.LibraryStatusReady {
			info.Status = "indexing"
		}
	}
	out := make([]gocoder.LibrarySkill, 0, len(byName))
	for _, info := range byName {
		sort.Slice(info.Files, func(i, j int) bool { return info.Files[i].Path < info.Files[j].Path })
		info.FileCount = len(info.Files)
		info.ContentHash = gocoder.SkillContentHash(info.Files)
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *fakeLibraryServer) registerSkillRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /library/nodes/{id}/raw", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		s.mu.Lock()
		n, ok := s.nodes[r.PathValue("id")]
		s.raws++
		s.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(n.content))
	})

	mux.HandleFunc("DELETE /library/nodes/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		n, ok := s.nodes[r.PathValue("id")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		for id, other := range s.nodes {
			if id == n.ID || n.Type == gocoder.LibraryTypeFolder && strings.HasPrefix(other.Path, n.Path+"/") {
				delete(s.nodes, id)
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("GET /library/skills", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.skillLists++
		if s.skillsDown {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"skills": s.skillInfos()})
	})

	mux.HandleFunc("GET /library/skills/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, info := range s.skillInfos() {
			if info.Name == r.PathValue("name") {
				_ = json.NewEncoder(w).Encode(info)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"skill not found"}}`))
	})

	// Search: a skill (discover) or file (content) matches when it
	// contains the query as a substring — enough to test formatting and
	// filters, not ranking.
	mux.HandleFunc("GET /library/skills/search", func(w http.ResponseWriter, r *http.Request) {
		if !s.requireAuth(w, r) {
			return
		}
		q := r.URL.Query()
		s.mu.Lock()
		defer s.mu.Unlock()
		var results []gocoder.LibrarySkillHit
		for _, info := range s.skillInfos() {
			if want := q.Get("skill"); want != "" && want != info.Name {
				continue
			}
			hit := gocoder.LibrarySkillHit{Skill: info.Name, Description: info.Description, Score: 0.8}
			if q.Get("mode") != gocoder.SkillSearchContent {
				if strings.Contains(info.Name+" "+info.Description, q.Get("q")) {
					results = append(results, hit)
				}
				continue
			}
			for _, f := range info.Files {
				n := s.nodes[f.ID]
				role := skillRole(f.Path)
				if want := q.Get("role"); want != "" && want != role || !strings.Contains(n.content, q.Get("q")) {
					continue
				}
				hit.Chunks = append(hit.Chunks, gocoder.LibrarySkillChunk{
					File: f.Path, Path: n.Path, Role: role, NodeID: n.ID, StartLine: 1,
					EndLine: strings.Count(n.content, "\n") + 1, Content: n.content, Score: 0.8,
				})
			}
			if len(hit.Chunks) > 0 {
				results = append(results, hit)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"mode": q.Get("mode"), "results": results})
	})
}

// ---- fixtures ----

// skillEnv is a test runtime whose every filesystem root — data dir,
// global config, home, project — is a fresh temp dir, so tests never see
// (or write into) the machine's real skills.
type skillEnv struct {
	fake    *fakeLibraryServer
	rt      *runtime
	project string
	data    string
}

func newSkillEnv(t *testing.T) *skillEnv {
	t.Helper()
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	data := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("GOCODE_TEST_HOME", t.TempDir())
	t.Setenv(advertiseEnv, "")
	withAccount(t, data, srv.URL, "gk_test")
	project := t.TempDir()
	rt, err := newRuntime(runtimeOptions{Directory: project, SkillsAdvertise: true})
	if err != nil {
		t.Fatal(err)
	}
	advertised.invalidate()
	resetHostSkills(t)
	return &skillEnv{fake: fake, rt: rt, project: project, data: data}
}

const pdfSkillMD = "---\nname: pdf-tools\ndescription: Fill and merge PDF forms\n---\n# PDF tools\n\nSee [forms](references/forms.md) and run `scripts/fill.py`.\n"

// seedRemoteSkill puts a skill into the fake library the way
// library_skill_store would: folder nodes plus ready file nodes.
func (e *skillEnv) seedRemoteSkill(name string, files map[string]string) {
	e.fake.addNode(gocoder.LibraryNode{Path: skillsRoot, Name: skillsRoot, Type: gocoder.LibraryTypeFolder}, "")
	e.fake.addNode(gocoder.LibraryNode{Path: skillLibraryPath(name, ""), ParentPath: skillsRoot, Name: name, Type: gocoder.LibraryTypeFolder}, "")
	for file, content := range files {
		p := skillLibraryPath(name, file)
		parent, base := splitLibraryPath(p)
		e.fake.addNode(gocoder.LibraryNode{
			Path: p, ParentPath: parent, Name: base, Type: gocoder.LibraryTypeFile,
			SizeBytes: int64(len(content)), SHA256: sha256Hex([]byte(content)),
			Description: fakeDescription(p, []byte(content)), Status: gocoder.LibraryStatusReady,
		}, content)
	}
}

func writeSkillFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for file, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func pdfSkillFiles() map[string]string {
	return map[string]string{
		"SKILL.md":            pdfSkillMD,
		"references/forms.md": "# Forms\n\nAcroForm field names.\n",
		"scripts/fill.py":     "#!/usr/bin/env python3\nprint('fill')\n",
	}
}

// listFiles returns every regular file under dir, slash-relative.
func listFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// ---- use / show ----

func TestSkillUseReturnsInlineContentAndWritesNothing(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	dataBefore := listFiles(t, e.data)

	out, err := handleSkillUse(context.Background(), e.rt, "pdf-tools")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "# PDF tools") || strings.Contains(out, "description: Fill") {
		t.Fatalf("expected the SKILL.md body without frontmatter, got:\n%s", out)
	}
	if !strings.Contains(out, "references/forms.md") || !strings.Contains(out, "scripts/fill.py") || !strings.Contains(out, "library_skill_show") {
		t.Fatalf("expected a file index and a pointer to library_skill_show, got:\n%s", out)
	}
	if files := listFiles(t, e.project); len(files) != 0 {
		t.Fatalf("use wrote into the project: %v", files)
	}
	if after := listFiles(t, e.data); strings.Join(after, ",") != strings.Join(dataBefore, ",") {
		t.Fatalf("use wrote into the data dir: %v -> %v", dataBefore, after)
	}

	if _, err := handleSkillUse(context.Background(), e.rt, "missing"); err == nil || !strings.Contains(err.Error(), "library_skill_search") {
		t.Fatalf("missing skill error = %v", err)
	}
}

func TestSkillShowFetchesFileAndTree(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	ctx := context.Background()

	out, err := handleSkillShow(ctx, e.rt, "pdf-tools", "references/forms.md", false, false)
	if err != nil || !strings.Contains(out, "AcroForm field names.") || !strings.Contains(out, ".skills/pdf-tools/references/forms.md") {
		t.Fatalf("show file = %q, %v", out, err)
	}
	tree, err := handleSkillShow(ctx, e.rt, "pdf-tools", "", true, false)
	if err != nil || !strings.Contains(tree, "scripts/fill.py") || !strings.Contains(tree, "script") {
		t.Fatalf("tree = %q, %v", tree, err)
	}
	for _, bad := range []string{"../SKILL.md", "/etc/passwd", "references/../../x"} {
		if _, err := handleSkillShow(ctx, e.rt, "pdf-tools", bad, false, false); err == nil {
			t.Errorf("show %q: expected a path error", bad)
		}
	}
}

func TestSkillShowMaterializeWritesOneFileIntoHashKeyedCache(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	ctx := context.Background()

	// A stale cache dir from an older version of the skill is pruned.
	stale := filepath.Join(e.data, "gocode", "library-skills", "pdf-tools@0000000000000000")
	writeSkillFiles(t, stale, map[string]string{"scripts/fill.py": "old"})

	out, err := handleSkillShow(ctx, e.rt, "pdf-tools", "scripts/fill.py", false, true)
	if err != nil {
		t.Fatal(err)
	}
	remote, _ := e.rt.client.GetLibrarySkill(ctx, e.rt.bearer, "pdf-tools")
	want := filepath.Join(skillCacheDir("pdf-tools", remote.ContentHash), "scripts", "fill.py")
	if !strings.Contains(out, want) {
		t.Fatalf("output does not name %s:\n%s", want, out)
	}
	cacheRoot := filepath.Join(e.data, "gocode", "library-skills")
	files := listFiles(t, cacheRoot)
	if len(files) != 1 || !strings.HasPrefix(files[0], "pdf-tools@"+shortHash(remote.ContentHash)+"/scripts/fill.py") {
		t.Fatalf("cache holds %v, want exactly the one materialized file", files)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no execute bit (os.Chmod only toggles read-only), so the
	// mode check is Unix-only.
	if goruntime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("materialized script is not executable: %v", info.Mode())
	}
	if files := listFiles(t, e.project); len(files) != 0 {
		t.Fatalf("materialize wrote into the project: %v", files)
	}
}

// ---- load ----

func TestSkillLoadPreservesStructureInProjectScopeByDefault(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())

	out, err := handleSkillLoad(context.Background(), e.rt, "pdf-tools", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(e.project, ".gocode", "skills", "pdf-tools")
	got := listFiles(t, dest)
	want := []string{syncMarkerName, "SKILL.md", "references/forms.md", "scripts/fill.py"}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("loaded files = %v, want %v", got, want)
	}
	if !strings.Contains(out, "# PDF tools") || !strings.Contains(out, "project scope") {
		t.Fatalf("output = %q", out)
	}
	// The loaded skill is discoverable the way gocode's bootStack finds it.
	if _, ok := e.rt.localSkills()["pdf-tools"]; !ok {
		t.Fatal("loaded skill is not discovered from the project root")
	}
	if status := localStatus(mustRemote(t, e, "pdf-tools"), e.rt.localSkills()); status != "installed (project)" {
		t.Fatalf("status after load = %q", status)
	}
}

func TestSkillLoadGlobalScope(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	if _, err := handleSkillLoad(context.Background(), e.rt, "pdf-tools", "global", false, nil); err != nil {
		t.Fatal(err)
	}
	if l, ok := e.rt.localSkills()["pdf-tools"]; !ok || l.Scope != scopeGlobal {
		t.Fatalf("global load not discovered as global: %+v", l)
	}
}

func TestSkillLoadGatesOverwriteAndSupportsPartialFetch(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	ctx := context.Background()
	dest := filepath.Join(e.project, ".gocode", "skills", "pdf-tools")
	writeSkillFiles(t, dest, map[string]string{"references/forms.md": "my local edit\n"})

	if _, err := handleSkillLoad(ctx, e.rt, "pdf-tools", "", false, nil); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("expected an overwrite gate, got %v", err)
	}
	// Refused means nothing was written, not even the non-conflicting files.
	if files := listFiles(t, dest); len(files) != 1 {
		t.Fatalf("a refused load wrote files: %v", files)
	}

	if _, err := handleSkillLoad(ctx, e.rt, "pdf-tools", "", false, []string{"SKILL.md"}); err != nil {
		t.Fatalf("partial load of a non-conflicting file: %v", err)
	}
	if files := listFiles(t, dest); len(files) != 2 {
		t.Fatalf("partial load wrote %v, want SKILL.md next to the local edit", files)
	}

	if _, err := handleSkillLoad(ctx, e.rt, "pdf-tools", "", true, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dest, "references", "forms.md"))
	if string(data) != "# Forms\n\nAcroForm field names.\n" {
		t.Fatalf("overwrite did not replace the local edit: %q", data)
	}
}

func TestSkillLoadRejectsPathEscapes(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("evil", map[string]string{"SKILL.md": "---\nname: evil\ndescription: d\n---\n", "../../outside.sh": "rm -rf /"})

	if _, err := handleSkillLoad(context.Background(), e.rt, "evil", "", false, nil); err == nil {
		t.Fatal("expected a path-escape error")
	}
	if files := listFiles(t, e.project); len(files) != 0 {
		t.Fatalf("an escaping load wrote %v", files)
	}
}

func TestSkillLoadRefusesToWriteThroughSymlink(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	dest := filepath.Join(e.project, ".gocode", "skills", "pdf-tools")
	outside := t.TempDir()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "scripts")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := handleSkillLoad(context.Background(), e.rt, "pdf-tools", "", true, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected a symlink refusal, got %v", err)
	}
	if files := listFiles(t, outside); len(files) != 0 {
		t.Fatalf("wrote through the symlink: %v", files)
	}
}

func mustRemote(t *testing.T, e *skillEnv, name string) gocoder.LibrarySkill {
	t.Helper()
	s, err := e.rt.client.GetLibrarySkill(context.Background(), e.rt.bearer, name)
	if err != nil || s == nil {
		t.Fatalf("remote %s: %v", name, err)
	}
	return *s
}

// ---- store / diff ----

func TestSkillStoreUploadsValidatesLinksAndSkipsSecrets(t *testing.T) {
	e := newSkillEnv(t)
	dir := filepath.Join(e.project, ".gocode", "skills", "pdf-tools")
	files := pdfSkillFiles()
	files["SKILL.md"] += "Broken: [x](references/missing.md). Escaping: [y](../../notes.md). Anchor: [z](#pdf-tools).\n"
	files[".env"] = "SECRET=1\n"
	files["node_modules/x/index.js"] = "x"
	files["scripts/config.py"] = "API_KEY = 'abcdefghijklmnopqrstuvwxyz123456'\n"
	writeSkillFiles(t, dir, files)
	ctx := context.Background()

	dry, err := handleSkillStore(ctx, e.rt, "pdf-tools", "", false, true, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"would store", "4 upload(s)", "broken link \"references/missing.md\"", "points outside the skill folder", ".env files are never uploaded", "node_modules/", "scripts/config.py: looks like it contains a secret"} {
		if !strings.Contains(dry, want) {
			t.Errorf("dry run output lacks %q:\n%s", want, dry)
		}
	}
	if strings.Contains(dry, "#pdf-tools") {
		t.Errorf("a same-document anchor was reported as a link problem:\n%s", dry)
	}
	if s, _ := e.rt.client.GetLibrarySkill(ctx, e.rt.bearer, "pdf-tools"); s != nil {
		t.Fatal("dry run uploaded the skill")
	}

	if _, err := handleSkillStore(ctx, e.rt, "pdf-tools", "", false, false, true, 5); err != nil {
		t.Fatal(err)
	}
	remote := mustRemote(t, e, "pdf-tools")
	var paths []string
	for _, f := range remote.Files {
		paths = append(paths, f.Path)
	}
	if strings.Join(paths, ",") != "SKILL.md,references/forms.md,scripts/config.py,scripts/fill.py" {
		t.Fatalf("remote files = %v", paths)
	}
	if e.fake.nodeAt(".skills/pdf-tools/references") == nil {
		t.Fatal("ancestor folders were not created")
	}
	if status := localStatus(remote, e.rt.localSkills()); status != "installed (project)" {
		t.Fatalf("status after store = %q", status)
	}
}

func TestSkillStoreOverwriteGatingAndRemoteDeletion(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", map[string]string{
		"SKILL.md":            pdfSkillMD,
		"references/forms.md": "old forms\n",
		"references/gone.md":  "deleted locally\n",
	})
	dir := filepath.Join(e.project, ".gocode", "skills", "pdf-tools")
	writeSkillFiles(t, dir, pdfSkillFiles())
	ctx := context.Background()

	diff, err := handleSkillDiff(ctx, e.rt, "pdf-tools", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"added    scripts/fill.py", "changed  references/forms.md", "removed  references/gone.md", "1 unchanged"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff lacks %q:\n%s", want, diff)
		}
	}

	if _, err := handleSkillStore(ctx, e.rt, "pdf-tools", "", false, false, false, 0); err == nil || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("expected an overwrite gate, got %v", err)
	}
	if e.fake.nodeAt(".skills/pdf-tools/references/gone.md") == nil {
		t.Fatal("a refused store deleted a remote file")
	}

	if _, err := handleSkillStore(ctx, e.rt, "", ".gocode/skills/pdf-tools", true, false, false, 0); err != nil {
		t.Fatal(err)
	}
	if e.fake.nodeAt(".skills/pdf-tools/references/gone.md") != nil {
		t.Fatal("overwrite did not delete the file removed locally")
	}
	if e.fake.overwrites != 1 {
		t.Fatalf("overwrites = %d, want 1 (forms.md)", e.fake.overwrites)
	}
	diff, _ = handleSkillDiff(ctx, e.rt, "pdf-tools", "")
	if !strings.Contains(diff, "identical") {
		t.Fatalf("after overwrite, diff = %s", diff)
	}
}

func TestSkillStoreRequiresDescription(t *testing.T) {
	e := newSkillEnv(t)
	dir := filepath.Join(e.project, ".gocode", "skills", "bare")
	writeSkillFiles(t, dir, map[string]string{"SKILL.md": "---\nname: bare\n---\nbody\n"})
	if _, err := handleSkillStore(context.Background(), e.rt, "bare", "", false, true, false, 0); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("expected a missing-description error, got %v", err)
	}
}

// ---- list ----

func TestSkillListLocalStatus(t *testing.T) {
	e := newSkillEnv(t)
	ctx := context.Background()
	skillMD := func(name string) string {
		return "---\nname: " + name + "\ndescription: " + name + " skill\n---\n# " + name + "\n"
	}
	for _, name := range []string{"absent", "same", "outdated", "edited", "unknown"} {
		e.seedRemoteSkill(name, map[string]string{"SKILL.md": skillMD(name)})
	}
	for _, name := range []string{"same", "outdated", "edited"} {
		if _, err := handleSkillLoad(ctx, e.rt, name, "", false, nil); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(e.project, ".gocode", "skills")
	// outdated: the library changed after the load.
	e.fake.nodeAt(".skills/outdated/SKILL.md").SHA256 = "changed-remotely"
	// edited: the local copy changed after the load.
	writeSkillFiles(t, filepath.Join(root, "edited"), map[string]string{"notes.md": "local addition"})
	// unknown: differs, and there is no sync marker to tell which side moved.
	writeSkillFiles(t, filepath.Join(root, "unknown"), map[string]string{"SKILL.md": skillMD("unknown") + "local\n"})

	out, err := handleSkillList(ctx, e.rt)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"absent": statusNotInstalled, "same": "installed (project)", "outdated": statusOutdated,
		"edited": statusLocalChanges, "unknown": statusDiffers,
	}
	for _, line := range strings.Split(out, "\n")[1:] {
		name := strings.Fields(line)[0]
		if !strings.Contains(line, want[name]) {
			t.Errorf("%s: line %q lacks status %q", name, line, want[name])
		}
	}
}

// ---- search ----

func TestSkillSearchFormatsBothModes(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	ctx := context.Background()

	discover, err := handleSkillSearch(ctx, e.rt, "PDF", "", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(discover, "1. pdf-tools") || !strings.Contains(discover, "Fill and merge PDF forms") || !strings.Contains(discover, "library_skill_use") {
		t.Fatalf("discover =\n%s", discover)
	}

	content, err := handleSkillSearch(ctx, e.rt, "fill", "content", "", "script", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "## pdf-tools") || !strings.Contains(content, ".skills/pdf-tools/scripts/fill.py:1-3  [script]") || !strings.Contains(content, "print('fill')") {
		t.Fatalf("content =\n%s", content)
	}
	if strings.Contains(content, "SKILL.md:") {
		t.Fatalf("role filter not applied:\n%s", content)
	}

	if _, err := handleSkillLoad(ctx, e.rt, "pdf-tools", "", false, nil); err != nil {
		t.Fatal(err)
	}
	discover, _ = handleSkillSearch(ctx, e.rt, "PDF", "", "", "", 0)
	if !strings.Contains(discover, "[installed: project]") {
		t.Fatalf("installed skills are not marked:\n%s", discover)
	}
}

// ---- delete ----

func TestSkillDeleteRequiresConfirm(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	ctx := context.Background()

	if _, err := handleSkillDelete(ctx, e.rt, "pdf-tools", false); err == nil || !strings.Contains(err.Error(), "confirm=true") {
		t.Fatalf("expected a confirm gate, got %v", err)
	}
	if s, _ := e.rt.client.GetLibrarySkill(ctx, e.rt.bearer, "pdf-tools"); s == nil {
		t.Fatal("unconfirmed delete removed the skill")
	}
	if _, err := handleSkillDelete(ctx, e.rt, "pdf-tools", true); err != nil {
		t.Fatal(err)
	}
	if s, _ := e.rt.client.GetLibrarySkill(ctx, e.rt.bearer, "pdf-tools"); s != nil {
		t.Fatalf("skill survived delete: %+v", s)
	}
	if e.fake.nodeAt(".skills/pdf-tools") != nil {
		t.Fatal("skill folder survived delete")
	}
}

// ---- system.transform hook ----

func manySkills(n int) []gocoder.LibrarySkill {
	out := make([]gocoder.LibrarySkill, n)
	for i := range out {
		out[i] = gocoder.LibrarySkill{Name: "skill-" + string(rune('a'+i)), Description: strings.Repeat("d", 50)}
	}
	return out
}

func TestAdvertiseBlockLimitsAndOmitsInstalled(t *testing.T) {
	skills := manySkills(10)
	block := advertiseBlock(skills, map[string]bool{"skill-a": true}, 3, 10_000)
	if strings.Contains(block, "skill-a") {
		t.Fatal("installed skill advertised")
	}
	if got := strings.Count(block, "\n- "); got != 3 {
		t.Fatalf("advertised %d skills, want limit 3:\n%s", got, block)
	}
	capped := advertiseBlock(skills, nil, 100, 400)
	if len(capped) > 400 || strings.Count(capped, "\n- ") == 0 {
		t.Fatalf("char cap not honored (%d chars):\n%s", len(capped), capped)
	}
	if advertiseBlock(skills, nil, 100, 50) != "" {
		t.Fatal("a cap too small for any entry should contribute nothing")
	}
	if advertiseBlock(nil, nil, 5, 2000) != "" {
		t.Fatal("no skills should contribute nothing")
	}
}

func TestAdvertiseCacheHonorsTTLAndFailures(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	c := &advertiseCache{now: func() time.Time { return now }}
	calls := 0
	fail := false
	fetch := func(context.Context) ([]gocoder.LibrarySkill, error) {
		calls++
		if fail {
			return nil, context.DeadlineExceeded
		}
		return manySkills(2), nil
	}
	ttl := 5 * time.Minute

	if got := c.list(ttl, fetch); len(got) != 2 || calls != 1 {
		t.Fatalf("first list = %d skills, %d calls", len(got), calls)
	}
	now = now.Add(time.Minute)
	if got := c.list(ttl, fetch); len(got) != 2 || calls != 1 {
		t.Fatalf("within TTL: %d skills, %d calls; want cached", len(got), calls)
	}
	now = now.Add(ttl)
	fail = true
	if got := c.list(ttl, fetch); got != nil || calls != 2 {
		t.Fatalf("failed refresh = %v, %d calls; want nothing", got, calls)
	}
	now = now.Add(time.Minute)
	if got := c.list(ttl, fetch); got != nil || calls != 2 {
		t.Fatalf("after a failure within TTL: %d calls; want no retry", calls)
	}
	now = now.Add(ttl)
	fail = false
	if got := c.list(ttl, fetch); len(got) != 2 || calls != 3 {
		t.Fatalf("recovery = %d skills, %d calls", len(got), calls)
	}
}

func setPending(t *testing.T, opts runtimeOptions) {
	t.Helper()
	rtMu.Lock()
	prevRT, prevPending := rt, pending
	opts = resolveDefaults(opts)
	pending = &opts
	rt = nil
	rtMu.Unlock()
	t.Cleanup(func() {
		rtMu.Lock()
		rt, pending = prevRT, prevPending
		rtMu.Unlock()
		advertised.invalidate()
	})
	advertised.invalidate()
}

func transformed(t *testing.T) []any {
	t.Helper()
	out := handleSystemTransform(map[string]any{"system": []any{"base prompt"}})
	system, _ := out["system"].([]any)
	return system
}

func TestSystemTransformAdvertisesRemoteSkills(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	e.seedRemoteSkill("local-one", map[string]string{"SKILL.md": "---\nname: local-one\ndescription: already here\n---\n"})
	writeSkillFiles(t, filepath.Join(e.project, ".gocode", "skills", "local-one"), map[string]string{"SKILL.md": "---\nname: local-one\ndescription: already here\n---\n"})
	setPending(t, runtimeOptions{Directory: e.project, SkillsAdvertise: true})

	system := transformed(t)
	if len(system) != 2 {
		t.Fatalf("system = %v, want the base prompt plus one block", system)
	}
	block := system[1].(string)
	if !strings.Contains(block, "- pdf-tools: Fill and merge PDF forms") || strings.Contains(block, "local-one") {
		t.Fatalf("block =\n%s", block)
	}

	// Cached within the TTL: a second turn does not refetch.
	lists := e.fake.skillLists
	transformed(t)
	if e.fake.skillLists != lists {
		t.Fatal("second turn refetched within the TTL")
	}
}

func TestSystemTransformRespectsOptionAndEnvOverride(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())

	setPending(t, runtimeOptions{Directory: e.project, SkillsAdvertise: false})
	if system := transformed(t); len(system) != 1 {
		t.Fatalf("skillsAdvertise=false still advertised: %v", system)
	}
	t.Setenv(advertiseEnv, "1")
	if system := transformed(t); len(system) != 2 {
		t.Fatalf("env override 1 did not enable advertising: %v", system)
	}

	setPending(t, runtimeOptions{Directory: e.project, SkillsAdvertise: true})
	t.Setenv(advertiseEnv, "0")
	if system := transformed(t); len(system) != 1 {
		t.Fatalf("env override 0 did not disable advertising: %v", system)
	}
}

func TestSystemTransformContributesNothingOnFetchFailure(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	e.fake.skillsDown = true
	setPending(t, runtimeOptions{Directory: e.project, SkillsAdvertise: true})
	if system := transformed(t); len(system) != 1 {
		t.Fatalf("a failed fetch contributed %v", system)
	}

	// Signed out: no account means no runtime, and still no failure.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	setPending(t, runtimeOptions{Directory: e.project, SkillsAdvertise: true})
	if system := transformed(t); len(system) != 1 {
		t.Fatalf("signed-out session contributed %v", system)
	}
}

// ---- over JSON-RPC ----

func TestSkillToolsAndHookOverJSONRPC(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	dataHome := t.TempDir()
	withAccount(t, dataHome, srv.URL, "gk_test")
	e := &skillEnv{fake: fake}
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())

	dir := t.TempDir()
	instance, err := plugin.Spawn(context.Background(), "library-plugin", plugin.SpawnConfig{
		Command: []string{os.Args[0], "-test.run=TestHelperPlugin"},
		Dir:     dir,
		Env: []string{
			helperEnv + "=1", "XDG_DATA_HOME=" + dataHome,
			"XDG_CONFIG_HOME=" + t.TempDir(), "GOCODE_TEST_HOME=" + t.TempDir(),
		},
	}, plugin.Input{Directory: dir, Worktree: dir}, plugin.Options{"baseURL": srv.URL}, func(string) {})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	host := plugin.NewHost(func(string, string, error) {})
	host.Add(instance)
	t.Cleanup(func() { _ = host.Close(context.Background()) })

	use := findTool(t, instance, "library_skill_use")
	result, err := use.Execute(context.Background(), map[string]any{"name": "pdf-tools"}, plugin.ToolContext{SessionID: "s1", Directory: dir, Worktree: dir})
	if err != nil || !strings.Contains(result.Output, "# PDF tools") {
		t.Fatalf("library_skill_use = %q, %v", result.Output, err)
	}

	out := plugin.SystemTransformOutput{System: []string{"base"}}
	if err := plugin.Trigger(context.Background(), host, plugin.SystemTransform, plugin.SystemTransformInput{SessionID: "s1"}, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.System) != 2 || !strings.Contains(out.System[1], "pdf-tools") {
		t.Fatalf("system after hook = %v", out.System)
	}
}

// ---- host rescan (step 7) ----

func TestSkillLoadTriggersHostRescan(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())

	// The real host route, over a registry discovered from the same
	// project root the load writes into — as gocode's bootStack builds it.
	registry := skill.Discover(filepath.Join(e.project, ".gocode"))
	var gotHeader string
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Test")
		(&server.Server{Skills: registry}).Mux().ServeHTTP(w, r)
	}))
	defer host.Close()
	e.rt.opts.ServerURL = host.URL
	e.rt.opts.ServerHeaders = map[string]string{"X-Test": "yes"}

	if _, ok := registry.Get("pdf-tools"); ok {
		t.Fatal("skill present before load")
	}
	out, err := handleSkillLoad(context.Background(), e.rt, "pdf-tools", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "rescanned") {
		t.Fatalf("output does not report the rescan:\n%s", out)
	}
	if _, ok := registry.Get("pdf-tools"); !ok {
		t.Fatal("host registry did not pick up the loaded skill")
	}
	if gotHeader != "yes" {
		t.Fatalf("handshake headers not sent: %q", gotHeader)
	}
}

func TestSkillLoadFallsBackWithoutHost(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	for _, url := range []string{"", "http://127.0.0.1:1"} { // no server; unreachable server
		e.rt.opts.ServerURL = url
		out, err := handleSkillLoad(context.Background(), e.rt, "pdf-tools", "", true, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "from the next session") {
			t.Fatalf("serverURL %q: output lacks the fallback note:\n%s", url, out)
		}
	}
}

// ---- registering library skills with the host ----

func resetHostSkills(t *testing.T) {
	t.Helper()
	reset := func() {
		hostSkills.mu.Lock()
		hostSkills.registered = false
		hostSkills.content = map[string]cachedContent{}
		hostSkills.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// hostFor serves the real host skill routes over registry.
func hostFor(t *testing.T, registry *skill.Registry) *httptest.Server {
	t.Helper()
	host := httptest.NewServer((&server.Server{Skills: registry}).Mux())
	t.Cleanup(host.Close)
	return host
}

func TestSyncRegistersLibrarySkillsWithHost(t *testing.T) {
	e := newSkillEnv(t)
	e.seedRemoteSkill("pdf-tools", pdfSkillFiles())
	e.seedRemoteSkill("local-one", map[string]string{"SKILL.md": "---\nname: local-one\ndescription: remote copy\n---\nremote body\n"})
	writeSkillFiles(t, filepath.Join(e.project, ".gocode", "skills", "local-one"), map[string]string{"SKILL.md": "---\nname: local-one\ndescription: local copy\n---\nlocal body\n"})

	registry := skill.Discover(filepath.Join(e.project, ".gocode"))
	e.rt.opts.ServerURL = hostFor(t, registry).URL

	if err := syncHostSkills(context.Background(), e.rt); err != nil {
		t.Fatal(err)
	}
	info, ok := registry.Get("pdf-tools")
	if !ok || info.Source != hostSkillSource || !strings.Contains(info.Content, "# PDF tools") || !strings.Contains(info.Content, "library_skill_show") {
		t.Fatalf("pdf-tools = %+v, %v", info, ok)
	}
	if local, _ := registry.Get("local-one"); local.Description != "local copy" {
		t.Fatalf("the installed skill was shadowed by its library copy: %+v", local)
	}

	// Registered: the prompt hook defers to <available_skills>.
	setPending(t, runtimeOptions{Directory: e.project, SkillsAdvertise: true})
	if system := transformed(t); len(system) != 1 {
		t.Fatalf("hook still injected a block after registration: %v", system)
	}

	// A second sync reuses cached content: no further SKILL.md downloads.
	before := e.fake.raws
	if err := syncHostSkills(context.Background(), e.rt); err != nil {
		t.Fatal(err)
	}
	if e.fake.raws != before {
		t.Fatalf("resync downloaded %d unchanged SKILL.md files", e.fake.raws-before)
	}
}

func TestWaitForHostGivesUpOnUnreachableHost(t *testing.T) {
	rt := &runtime{opts: runtimeOptions{ServerURL: "http://127.0.0.1:1"}}
	start := time.Now()
	if waitForHost(context.Background(), rt, 300*time.Millisecond) {
		t.Fatal("unreachable host reported up")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("waitForHost overran its timeout")
	}
}

// TestPluginRegistersLibrarySkillsWithRealHost is the whole path: the real
// plugin process, handed a real host's serverURL at handshake, registers
// the library's skills into that host's skill registry by itself.
func TestPluginRegistersLibrarySkillsWithRealHost(t *testing.T) {
	fake := newFakeLibraryServer(1)
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	dataHome := t.TempDir()
	withAccount(t, dataHome, srv.URL, "gk_test")
	(&skillEnv{fake: fake}).seedRemoteSkill("pdf-tools", pdfSkillFiles())

	dir := t.TempDir()
	registry := skill.Discover(filepath.Join(dir, ".gocode"))
	host := hostFor(t, registry)

	instance, err := plugin.Spawn(context.Background(), "library-plugin", plugin.SpawnConfig{
		Command: []string{os.Args[0], "-test.run=TestHelperPlugin"},
		Dir:     dir,
		Env: []string{
			helperEnv + "=1", "XDG_DATA_HOME=" + dataHome,
			"XDG_CONFIG_HOME=" + t.TempDir(), "GOCODE_TEST_HOME=" + t.TempDir(),
		},
	}, plugin.Input{Directory: dir, Worktree: dir, ServerURL: host.URL}, plugin.Options{"baseURL": srv.URL}, func(string) {})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	h := plugin.NewHost(func(string, string, error) {})
	h.Add(instance)
	t.Cleanup(func() { _ = h.Close(context.Background()) })

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := registry.Get("pdf-tools"); ok {
			if info.Source != hostSkillSource {
				t.Fatalf("registered without its source: %+v", info)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the plugin never registered the library skill with the host")
}
