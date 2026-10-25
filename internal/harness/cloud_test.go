package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/langazov/gocode-go/internal/gocoder"
)

// fakeLibrary is an in-memory gocoder.org Library: files by full path.
type fakeLibrary struct {
	mu      sync.Mutex
	files   map[string][]byte
	folders map[string]bool
	down    bool // every call fails, as offline
	uploads int
}

func newFakeLibrary() *fakeLibrary {
	return &fakeLibrary{files: map[string][]byte{}, folders: map[string]bool{}}
}

var errOffline = errors.New("dial tcp: network is unreachable")

func (f *fakeLibrary) ListLibraryTree(_ context.Context, _, parent string) ([]gocoder.LibraryNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errOffline
	}
	var out []gocoder.LibraryNode
	for p := range f.files {
		if strings.HasPrefix(p, parent+"/") {
			out = append(out, gocoder.LibraryNode{ID: p, Path: p, Type: gocoder.LibraryTypeFile})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (f *fakeLibrary) UploadLibraryFileWith(_ context.Context, _, path, _ string, data []byte, opts gocoder.UploadOptions) (*gocoder.LibraryNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errOffline
	}
	parent := path[:strings.LastIndex(path, "/")]
	if !f.folders[parent] {
		return nil, fmt.Errorf("no folder %s", parent)
	}
	if _, exists := f.files[path]; exists && !opts.Overwrite {
		return nil, &gocoder.APIError{Status: http.StatusConflict, Message: "exists"}
	}
	f.files[path] = append([]byte(nil), data...)
	f.uploads++
	return &gocoder.LibraryNode{ID: path, Path: path}, nil
}

func (f *fakeLibrary) CreateLibraryFolder(_ context.Context, _, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errOffline
	}
	f.folders[path] = true
	return nil
}

func (f *fakeLibrary) DownloadLibraryRaw(_ context.Context, _, id string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[id]
	if !ok {
		return nil, fmt.Errorf("no node %s", id)
	}
	return data, nil
}

func (f *fakeLibrary) DeleteLibraryNode(_ context.Context, _, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.files, id)
	return nil
}

func (f *fakeLibrary) skills() map[string][]gocoder.LibrarySkillFile {
	out := map[string][]gocoder.LibrarySkillFile{}
	for p, data := range f.files {
		rest, ok := strings.CutPrefix(p, skillsPrefix+"/")
		if !ok {
			continue
		}
		name, rel, _ := strings.Cut(rest, "/")
		sum := sha256.Sum256(data)
		out[name] = append(out[name], gocoder.LibrarySkillFile{Path: rel, ID: p, SHA256: hex.EncodeToString(sum[:])})
	}
	return out
}

func (f *fakeLibrary) ListLibrarySkills(context.Context, string) ([]gocoder.LibrarySkill, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gocoder.LibrarySkill
	for name, files := range f.skills() {
		out = append(out, gocoder.LibrarySkill{Name: name, Files: files, ContentHash: gocoder.SkillContentHash(files)})
	}
	return out, nil
}

func (f *fakeLibrary) GetLibrarySkill(_ context.Context, _, name string) (*gocoder.LibrarySkill, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	files, ok := f.skills()[name]
	if !ok {
		return nil, nil
	}
	return &gocoder.LibrarySkill{Name: name, Files: files, ContentHash: gocoder.SkillContentHash(files)}, nil
}

func (f *fakeLibrary) SearchLibrarySkills(context.Context, string, gocoder.SkillSearchRequest) ([]gocoder.LibrarySkillHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gocoder.LibrarySkillHit
	for name := range f.skills() {
		out = append(out, gocoder.LibrarySkillHit{Skill: name})
	}
	return out, nil
}

func (f *fakeLibrary) SearchLibrary(_ context.Context, _, _, prefix string, _ int) ([]gocoder.LibrarySearchHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gocoder.LibrarySearchHit
	for p := range f.files {
		if strings.HasPrefix(p, prefix) && strings.HasSuffix(p, "EXPERIENCE.md") {
			out = append(out, gocoder.LibrarySearchHit{Node: &gocoder.LibraryNode{ID: p, Path: p}})
		}
	}
	return out, nil
}

func (f *fakeLibrary) put(path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = []byte(content)
}

func TestDetect(t *testing.T) {
	ctx := context.Background()
	ready := DetectInput{PluginLoaded: true, PluginTools: []string{"library_skill_search", "library_skill_store", "x"}, HasKey: true,
		CheckKey: func(context.Context) error { return nil }}
	cases := []struct {
		name    string
		in      DetectInput
		setting string
		want    string
		reason  string
		fails   bool
	}{
		{"auto ready", ready, "", BackendSplit, "", false},
		{"cloud ready", ready, "cloud", BackendCloud, "", false},
		{"local forced", ready, "local", BackendLocal, "is local", false},
		{"no plugin", DetectInput{}, "auto", BackendLocal, "not loaded", false},
		{"no tools", DetectInput{PluginLoaded: true}, "auto", BackendLocal, "does not offer", false},
		{"no key", DetectInput{PluginLoaded: true, PluginTools: ready.PluginTools}, "auto", BackendLocal, "not signed in", false},
		{"bad key", DetectInput{PluginLoaded: true, PluginTools: ready.PluginTools, HasKey: true,
			CheckKey: func(context.Context) error { return errors.New("401") }}, "auto", BackendLocal, "rejected", false},
		{"cloud unavailable", DetectInput{}, "cloud", "", "", true},
	}
	for _, tc := range cases {
		in := tc.in
		in.Setting = tc.setting
		got, err := Detect(ctx, in)
		if tc.fails {
			if err == nil {
				t.Errorf("%s: want an error", tc.name)
			}
			continue
		}
		if err != nil || got.Backend != tc.want || !strings.Contains(got.Reason, tc.reason) {
			t.Errorf("%s: got %+v, %v; want %s (%q)", tc.name, got, err, tc.want, tc.reason)
		}
	}
	if _, err := Detect(ctx, DetectInput{Setting: "sometimes"}); err == nil {
		t.Error("an unknown setting was accepted")
	}
}

func TestPublishSkills(t *testing.T) {
	ctx := context.Background()
	lib := newFakeLibrary()
	cloud := &Cloud{Client: lib, Bearer: "gk_test"}
	tree := Tree{
		"skills/doc_review/SKILL.md":          skillDoc,
		"skills/doc_review/scripts/check.py":  "print(1)\n",
		"skills/other/SKILL.md":               "---\nname: other\ndescription: d\n---\n",
		FileSystemPrompt:                      "load doc_review and other",
		"scripts/not_a_skill_file.py":         "x",
		"skills/doc_review/references/ref.md": "ref",
	}
	report, err := cloud.PublishSkills(ctx, tree, false)
	if err != nil || len(report.New) != 2 {
		t.Fatalf("first publish: %+v, %v", report, err)
	}
	if string(lib.files[".skills/doc_review/scripts/check.py"]) != "print(1)\n" || lib.files[".skills/doc_review/SKILL.md"] == nil {
		t.Fatalf("files not uploaded: %v", lib.files)
	}
	report, _ = cloud.PublishSkills(ctx, tree, false)
	if len(report.Unchanged) != 2 || len(report.New) != 0 {
		t.Fatalf("republish should be a no-op: %+v", report)
	}
	tree["skills/other/SKILL.md"] += "changed\n"
	report, _ = cloud.PublishSkills(ctx, tree, false)
	if len(report.Blocked) != 1 || report.Blocked[0] != "other" {
		t.Fatalf("a changed skill must need force: %+v", report)
	}
	report, err = cloud.PublishSkills(ctx, tree, true)
	if err != nil || len(report.Changed) != 1 || !strings.HasSuffix(string(lib.files[".skills/other/SKILL.md"]), "changed\n") {
		t.Fatalf("forced publish: %+v, %v", report, err)
	}
}

func TestCloudStoreSpoolAndSync(t *testing.T) {
	ctx := context.Background()
	lib := newFakeLibrary()
	store := &CloudStore{Cloud: &Cloud{Client: lib}, Cache: &LocalStore{Root: t.TempDir()}}
	entry := Entry{Domain: "answers", Task: "t1", Label: "agg1", Why: "complete",
		Lesson: Experience{Cue: "c", Lesson: "l", Evidence: "e"}, Tree: Tree{FileSystemPrompt: "x", "scripts/a.py": "print()"}}

	lib.down = true
	if err := store.Add(ctx, entry); err != nil {
		t.Fatalf("an upload failure must not fail Add: %v", err)
	}
	infos, _ := store.Cache.List(ctx, "answers")
	if len(infos) != 1 || !infos[0].Pending {
		t.Fatalf("entry should be pending: %+v", infos)
	}
	if dir, _ := store.ContextDir(ctx, "answers"); dir == "" {
		t.Fatal("an offline store must still serve the cache")
	}

	lib.down = false
	pushed, err := store.Sync(ctx)
	if err != nil || pushed != 1 {
		t.Fatalf("sync: %d, %v", pushed, err)
	}
	if lib.files[".harness/answers/harnesses/t1/harness/scripts/a.py"] == nil || lib.files[".harness/answers/INDEX.md"] == nil {
		t.Fatalf("not uploaded: %v", keys(lib.files))
	}
	infos, _ = store.Cache.List(ctx, "answers")
	if infos[0].Pending {
		t.Fatal("still pending after sync")
	}

	// Another machine's entry is pulled into the cache.
	lib.put(harnessPrefix+"/answers/harnesses/t2/EXPERIENCE.md", "# t2\n\n- adopted: `c1`\n\n## Transferable lesson\n- **cue**: remote cue\n- **lesson**: l\n- **evidence**: e\n")
	lib.put(harnessPrefix+"/answers/harnesses/t2/harness/systemprompt.md", "remote")
	infos, _ = store.List(ctx, "answers")
	if len(infos) != 2 || infos[1].Cue != "remote cue" {
		t.Fatalf("remote entry not pulled: %+v", infos)
	}

	if err := store.Forget(ctx, "answers", "t2"); err != nil {
		t.Fatal(err)
	}
	if lib.files[harnessPrefix+"/answers/harnesses/t2/EXPERIENCE.md"] != nil {
		t.Fatal("forget left the remote entry")
	}
	if _, err := os.Stat(store.Cache.entryDir("answers", "t2")); err == nil {
		t.Fatal("forget left the cached entry")
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestSkillEvidenceAndRelated(t *testing.T) {
	ctx := context.Background()
	lib := newFakeLibrary()
	lib.put(".skills/go_tests/SKILL.md", "---\nname: go_tests\n---\n")
	lib.put(".skills/go_tests/scripts/run.sh", "go test ./...")
	lib.put(".harness/other/harnesses/x/EXPERIENCE.md", "lesson from elsewhere")
	lib.put(".harness/answers/harnesses/y/EXPERIENCE.md", "same domain")
	cloud := &Cloud{Client: lib}
	task := Task{ID: "t", Domain: "answers", Prompt: "fix tests"}

	skills, err := cloud.SkillEvidence(ctx, task)
	if err != nil || skills["go_tests/SKILL.md"] == "" || skills["go_tests/scripts/run.sh"] == "" {
		t.Fatalf("skill evidence = %v, %v", skills, err)
	}
	related, err := cloud.RelatedLessons(ctx, task)
	if err != nil || len(related) != 1 || related["other__harnesses__x.md"] != "lesson from elsewhere" {
		t.Fatalf("related = %v, %v", related, err)
	}
}

func TestEngineRecordsSkillDriftAndExtraEvidence(t *testing.T) {
	exec, improver := &fakeExec{}, &fakeImprover{}
	engine, task := newEngineFixture(t, exec, improver, nil)
	var calls int
	engine.Environment = func(context.Context) (map[string]string, error) {
		calls++
		return map[string]string{"go_tests": fmt.Sprint("hash", calls)}, nil
	}
	engine.Extra = func(context.Context, Task) ([]ExtraEvidence, error) {
		return []ExtraEvidence{{Dir: "cloud-skills", Description: "Library skills", Files: Tree{"go_tests/SKILL.md": "x"}}}, nil
	}
	summary, err := engine.Run(context.Background(), task, RunOptions{K: 1, Mode: ModeParallel})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.SkillsDrifted || summary.SkillsAtStart["go_tests"] != "hash1" {
		t.Fatalf("drift not recorded: %+v %+v", summary.SkillsAtStart, summary.SkillsAtEnd)
	}
	evidence := filepath.Join(engine.Dir(task.ID, 1), ModeParallel, "context", "inv1", "cloud-skills", "go_tests", "SKILL.md")
	if _, err := os.Stat(evidence); err != nil {
		t.Fatalf("extra evidence missing: %v", err)
	}
}
