package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/langazov/gocode-go/internal/gocoder"
)

// gocoder.org Library integration (documentation/HARNESS2_PLAN.md §7). When
// library-plugin is loaded and signed in, Harness² uses the Library tightly:
// cloud skills are part of every rollout's environment (snapshotted so drift
// is visible), matching ones are shown to the editor, adopted skills can be
// published to `.skills/`, and — on the full cloud backend — the experience
// library lives under `.harness/` with a local cache. Without the plugin,
// everything runs on LocalStore exactly as before.

// LibraryClient is the subset of *gocoder.Client the integration uses, so
// tests can fake gocoder.org.
type LibraryClient interface {
	ListLibraryTree(ctx context.Context, bearer, parentPath string) ([]gocoder.LibraryNode, error)
	UploadLibraryFileWith(ctx context.Context, bearer, path, filename string, data []byte, opts gocoder.UploadOptions) (*gocoder.LibraryNode, error)
	CreateLibraryFolder(ctx context.Context, bearer, path string) error
	DownloadLibraryRaw(ctx context.Context, bearer, id string) ([]byte, error)
	DeleteLibraryNode(ctx context.Context, bearer, id string) error
	ListLibrarySkills(ctx context.Context, bearer string) ([]gocoder.LibrarySkill, error)
	GetLibrarySkill(ctx context.Context, bearer, name string) (*gocoder.LibrarySkill, error)
	SearchLibrarySkills(ctx context.Context, bearer string, req gocoder.SkillSearchRequest) ([]gocoder.LibrarySkillHit, error)
	SearchLibrary(ctx context.Context, bearer, query, pathPrefix string, k int) ([]gocoder.LibrarySearchHit, error)
}

// Cloud is an authenticated handle on the gocoder.org Library.
type Cloud struct {
	Client LibraryClient
	Bearer string
}

// Library prefixes. `.skills/` is library-plugin's; `.harness/` holds the
// experience library and needs gocode-infra to accept the files a harness
// tree carries (scripts, hooks.json) the way it does under `.skills/`.
const (
	skillsPrefix  = ".skills"
	harnessPrefix = ".harness"
)

// Library backend names.
const (
	BackendLocal = "local"
	BackendSplit = "split"
	BackendCloud = "cloud"
)

// DetectInput is what backend detection looks at.
type DetectInput struct {
	// Setting is the config's harness.library: auto (or empty), local, cloud.
	Setting string
	// PluginLoaded and PluginTools describe the library-plugin instance
	// the plugin host reports, if any.
	PluginLoaded bool
	PluginTools  []string
	// HasKey reports a stored gocoder.org API key; CheckKey validates it.
	HasKey   bool
	CheckKey func(context.Context) error
	// Features reports the reserved roots the Library service supports; on
	// auto, "harness" among them selects the full cloud backend.
	Features func(context.Context) ([]string, error)
}

// Detection is the backend a run uses and why.
type Detection struct {
	Backend string `json:"backend"`
	Reason  string `json:"reason"`
}

// requiredPluginTools are the tools that show library-plugin can serve skills.
var requiredPluginTools = []string{"library_skill_search", "library_skill_store"}

// Detect picks the backend once per run (§7.1). auto falls back to local
// with the failed check named; cloud fails instead. auto resolves to cloud
// only when the service says it stores `.harness/`; against an older
// deployment it resolves to split — the Library for skills, the local
// filesystem for experience.
func Detect(ctx context.Context, in DetectInput) (Detection, error) {
	setting := strings.ToLower(strings.TrimSpace(in.Setting))
	switch setting {
	case "", "auto", BackendLocal, BackendCloud:
	default:
		return Detection{}, fmt.Errorf(`harness.library must be "auto", "local" or "cloud", got %q`, in.Setting)
	}
	if setting == BackendLocal {
		return Detection{Backend: BackendLocal, Reason: "harness.library is local"}, nil
	}
	reason := ""
	switch {
	case !in.PluginLoaded:
		reason = "library-plugin not loaded"
	case !containsAll(in.PluginTools, requiredPluginTools):
		reason = "library-plugin does not offer " + strings.Join(requiredPluginTools, " and ")
	case !in.HasKey:
		reason = "not signed in to gocoder.org"
	case in.CheckKey != nil:
		if err := in.CheckKey(ctx); err != nil {
			reason = "gocoder.org rejected the stored key: " + err.Error()
		}
	}
	if reason != "" {
		if setting == BackendCloud {
			return Detection{}, fmt.Errorf("harness.library is cloud, but %s", reason)
		}
		return Detection{Backend: BackendLocal, Reason: reason}, nil
	}
	if setting == BackendCloud {
		return Detection{Backend: BackendCloud, Reason: "library-plugin loaded and signed in"}, nil
	}
	if in.Features != nil {
		if features, err := in.Features(ctx); err == nil && containsAll(features, []string{"harness"}) {
			return Detection{Backend: BackendCloud, Reason: "library-plugin loaded and signed in; gocoder.org stores .harness/"}, nil
		}
	}
	return Detection{Backend: BackendSplit, Reason: "library-plugin loaded and signed in; experience stays local until gocoder.org accepts .harness/"}, nil
}

func containsAll(have, want []string) bool {
	set := map[string]bool{}
	for _, h := range have {
		set[h] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

// SkillSnapshot maps every Library skill to its content hash, so a run can
// record the environment its rollouts saw and flag drift.
func (c *Cloud) SkillSnapshot(ctx context.Context) (map[string]string, error) {
	skills, err := c.Client.ListLibrarySkills(ctx, c.Bearer)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(skills))
	for _, s := range skills {
		hash := s.ContentHash
		if hash == "" {
			hash = gocoder.SkillContentHash(s.Files)
		}
		out[s.Name] = hash
	}
	return out, nil
}

// maxEvidenceSkills bounds the Library skills shown to an editor.
const maxEvidenceSkills = 5

// SkillEvidence returns the Library skills matching a task, as files for
// `context/cloud-skills/<name>/...`. The layout matches a harness skill's,
// so an editor can copy one into `context/harness/skills/` unchanged.
func (c *Cloud) SkillEvidence(ctx context.Context, task Task) (Tree, error) {
	hits, err := c.Client.SearchLibrarySkills(ctx, c.Bearer, gocoder.SkillSearchRequest{
		Query: clip(task.Prompt, 2000), Mode: gocoder.SkillSearchDiscover, K: maxEvidenceSkills,
	})
	if err != nil {
		return nil, err
	}
	out := Tree{}
	for _, hit := range hits {
		skill, err := c.Client.GetLibrarySkill(ctx, c.Bearer, hit.Skill)
		if err != nil || skill == nil {
			continue
		}
		for _, file := range skill.Files {
			rel, err := NormalizeRel(file.Path)
			if err != nil || rel == "" {
				continue
			}
			data, err := c.Client.DownloadLibraryRaw(ctx, c.Bearer, file.ID)
			if err != nil {
				continue
			}
			out[hit.Skill+"/"+rel] = string(data)
		}
	}
	return out, nil
}

// RelatedLessons finds other domains' lessons for a task by semantic search
// over the experience library's EXPERIENCE.md files (crossDomain). Off by
// default: Harness² itself retrieves same-domain entries only.
func (c *Cloud) RelatedLessons(ctx context.Context, task Task) (Tree, error) {
	hits, err := c.Client.SearchLibrary(ctx, c.Bearer, clip(task.Prompt, 2000), harnessPrefix+"/", 8)
	if err != nil {
		return nil, err
	}
	out := Tree{}
	for _, hit := range hits {
		if hit.Node == nil || path.Base(hit.Node.Path) != "EXPERIENCE.md" {
			continue
		}
		rel := strings.TrimPrefix(hit.Node.Path, harnessPrefix+"/")
		domain := strings.SplitN(rel, "/", 2)[0]
		if domain == task.Domain {
			continue
		}
		data, err := c.Client.DownloadLibraryRaw(ctx, c.Bearer, hit.Node.ID)
		if err != nil {
			continue
		}
		out[strings.ReplaceAll(strings.TrimSuffix(rel, "/EXPERIENCE.md"), "/", "__")+".md"] = string(data)
	}
	return out, nil
}

// upload writes one file, creating its ancestor folders first (the Library
// tree has no virtual folders) and replacing an existing file.
func (c *Cloud) upload(ctx context.Context, libraryPath string, data []byte) error {
	parts := strings.Split(libraryPath, "/")
	for i := 1; i < len(parts); i++ {
		if err := c.Client.CreateLibraryFolder(ctx, c.Bearer, strings.Join(parts[:i], "/")); err != nil {
			return fmt.Errorf("ensure folder %s: %w", strings.Join(parts[:i], "/"), err)
		}
	}
	name := parts[len(parts)-1]
	_, err := c.Client.UploadLibraryFileWith(ctx, c.Bearer, libraryPath, name, data, gocoder.UploadOptions{})
	var apiErr *gocoder.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		_, err = c.Client.UploadLibraryFileWith(ctx, c.Bearer, libraryPath, name, data, gocoder.UploadOptions{Overwrite: true})
	}
	return err
}

// PublishReport says what PublishSkills did per skill.
type PublishReport struct {
	New       []string `json:"new"`
	Changed   []string `json:"changed"`
	Unchanged []string `json:"unchanged"`
	// Blocked are changed skills left alone because force was not set.
	Blocked []string `json:"blocked"`
}

// SkillStatus compares a tree's skills with the Library's `.skills/`.
func (c *Cloud) SkillStatus(ctx context.Context, tree Tree) (PublishReport, error) {
	var report PublishReport
	for _, name := range SkillNames(tree) {
		remote, err := c.Client.GetLibrarySkill(ctx, c.Bearer, name)
		if err != nil {
			return report, err
		}
		switch {
		case remote == nil:
			report.New = append(report.New, name)
		case skillHash(tree, name) == remoteHash(remote):
			report.Unchanged = append(report.Unchanged, name)
		default:
			report.Changed = append(report.Changed, name)
		}
	}
	return report, nil
}

// PublishSkills uploads a tree's skills to `.skills/<name>/`, as
// library_skill_store does: unchanged skills are skipped, and a changed one
// is replaced only with force.
func (c *Cloud) PublishSkills(ctx context.Context, tree Tree, force bool) (PublishReport, error) {
	report, err := c.SkillStatus(ctx, tree)
	if err != nil {
		return report, err
	}
	publish := append([]string(nil), report.New...)
	if force {
		publish = append(publish, report.Changed...)
	} else {
		report.Blocked, report.Changed = report.Changed, nil
	}
	for _, name := range publish {
		files := skillFiles(tree, name)
		// SKILL.md first, so its node exists before the rest is indexed.
		sort.SliceStable(files, func(i, j int) bool { return files[i] == "SKILL.md" && files[j] != "SKILL.md" })
		for _, rel := range files {
			if err := c.upload(ctx, skillsPrefix+"/"+name+"/"+rel, []byte(tree["skills/"+name+"/"+rel])); err != nil {
				return report, fmt.Errorf("publish skill %s: %w", name, err)
			}
		}
	}
	return report, nil
}

func skillFiles(tree Tree, name string) []string {
	prefix := "skills/" + name + "/"
	var out []string
	for _, rel := range tree.Paths() {
		if strings.HasPrefix(rel, prefix) {
			out = append(out, strings.TrimPrefix(rel, prefix))
		}
	}
	return out
}

// skillHash hashes a tree skill the way gocoder.SkillContentHash hashes a
// Library one, so the two compare.
func skillHash(tree Tree, name string) string {
	var files []gocoder.LibrarySkillFile
	for _, rel := range skillFiles(tree, name) {
		sum := sha256.Sum256([]byte(tree["skills/"+name+"/"+rel]))
		files = append(files, gocoder.LibrarySkillFile{Path: rel, SHA256: hex.EncodeToString(sum[:])})
	}
	return gocoder.SkillContentHash(files)
}

func remoteHash(skill *gocoder.LibrarySkill) string {
	for _, f := range skill.Files {
		if f.SHA256 == "" {
			return skill.ContentHash // a file without a digest: trust the server's hash
		}
	}
	return gocoder.SkillContentHash(skill.Files)
}

// CloudStore is the experience library in the Library under `.harness/`,
// with a LocalStore as read-through cache and upload spool. Reads come from
// the cache after a best-effort pull; a write lands in the cache first and
// is uploaded after, so a network failure never fails the run — the entry
// stays pending until Sync.
type CloudStore struct {
	Cloud *Cloud
	Cache *LocalStore
}

// Backend implements ExperienceStore.
func (s *CloudStore) Backend() string { return BackendCloud }

// ContextDir implements ExperienceStore.
func (s *CloudStore) ContextDir(ctx context.Context, domain string) (string, error) {
	_ = s.pull(ctx, domain) // offline is fine: the cache still serves
	return s.Cache.ContextDir(ctx, domain)
}

// Add implements ExperienceStore.
func (s *CloudStore) Add(ctx context.Context, entry Entry) error {
	if err := s.Cache.Add(ctx, entry); err != nil {
		return err
	}
	dir := s.Cache.entryDir(entry.Domain, entry.Task)
	if err := writeFileAll(filepath.Join(dir, pendingMarker), ""); err != nil {
		return err
	}
	_ = s.push(ctx, entry.Domain, entry.Task) // pending until it succeeds
	return nil
}

// push uploads one cached entry and clears its pending marker.
func (s *CloudStore) push(ctx context.Context, domain, task string) error {
	dir := s.Cache.entryDir(domain, task)
	err := filepath.WalkDir(dir, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() == pendingMarker {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return s.Cloud.upload(ctx, harnessPrefix+"/"+domain+"/harnesses/"+task+"/"+filepath.ToSlash(rel), data)
	})
	if err != nil {
		return err
	}
	if index, err := os.ReadFile(filepath.Join(s.Cache.domainDir(domain), "INDEX.md")); err == nil {
		_ = s.Cloud.upload(ctx, harnessPrefix+"/"+domain+"/INDEX.md", index)
	}
	return os.Remove(filepath.Join(dir, pendingMarker))
}

// pull downloads a domain's remote entries the cache lacks. Entries pending
// upload are never overwritten.
func (s *CloudStore) pull(ctx context.Context, domain string) error {
	nodes, err := s.Cloud.Client.ListLibraryTree(ctx, s.Cloud.Bearer, harnessPrefix+"/"+domain)
	if err != nil {
		return err
	}
	prefix := harnessPrefix + "/" + domain + "/harnesses/"
	for _, node := range nodes {
		if node.Type != gocoder.LibraryTypeFile || !strings.HasPrefix(node.Path, prefix) {
			continue
		}
		rel := strings.TrimPrefix(node.Path, prefix)
		task := strings.SplitN(rel, "/", 2)[0]
		clean, err := NormalizeRel(rel)
		if err != nil || task == "" {
			continue
		}
		dir := s.Cache.entryDir(domain, task)
		if _, err := os.Stat(filepath.Join(dir, pendingMarker)); err == nil {
			continue
		}
		target := filepath.Join(s.Cache.domainDir(domain), "harnesses", filepath.FromSlash(clean))
		if _, err := os.Stat(target); err == nil {
			continue
		}
		data, err := s.Cloud.Client.DownloadLibraryRaw(ctx, s.Cloud.Bearer, node.ID)
		if err != nil {
			return err
		}
		if err := writeFileAll(target, string(data)); err != nil {
			return err
		}
	}
	return s.Cache.rebuildIndex(domain)
}

// Sync uploads every pending entry and pulls every remote domain, returning
// how many entries it uploaded.
func (s *CloudStore) Sync(ctx context.Context) (int, error) {
	pushed := 0
	domains, _ := os.ReadDir(s.Cache.Root)
	for _, d := range domains {
		if !d.IsDir() {
			continue
		}
		tasks, _ := os.ReadDir(filepath.Join(s.Cache.domainDir(d.Name()), "harnesses"))
		for _, t := range tasks {
			if _, err := os.Stat(filepath.Join(s.Cache.entryDir(d.Name(), t.Name()), pendingMarker)); err != nil {
				continue
			}
			if err := s.push(ctx, d.Name(), t.Name()); err != nil {
				return pushed, fmt.Errorf("upload %s/%s: %w", d.Name(), t.Name(), err)
			}
			pushed++
		}
	}
	nodes, err := s.Cloud.Client.ListLibraryTree(ctx, s.Cloud.Bearer, harnessPrefix)
	if err != nil {
		return pushed, err
	}
	seen := map[string]bool{}
	for _, node := range nodes {
		rest := strings.TrimPrefix(node.Path, harnessPrefix+"/")
		if domain := strings.SplitN(rest, "/", 2)[0]; domain != "" && domain != rest && !seen[domain] {
			seen[domain] = true
			if err := s.pull(ctx, domain); err != nil {
				return pushed, err
			}
		}
	}
	return pushed, nil
}

// List implements ExperienceStore.
func (s *CloudStore) List(ctx context.Context, domain string) ([]EntryInfo, error) {
	_ = s.pull(ctx, domain)
	return s.Cache.List(ctx, domain)
}

// Domains implements ExperienceStore.
func (s *CloudStore) Domains(ctx context.Context) ([]string, error) { return s.Cache.Domains(ctx) }

// Get implements ExperienceStore.
func (s *CloudStore) Get(ctx context.Context, domain, task string) (Entry, error) {
	return s.Cache.Get(ctx, domain, task)
}

// Forget deletes the remote entry and the cached one.
func (s *CloudStore) Forget(ctx context.Context, domain, task string) error {
	nodes, err := s.Cloud.Client.ListLibraryTree(ctx, s.Cloud.Bearer, harnessPrefix+"/"+domain+"/harnesses/"+task)
	if err != nil {
		return err
	}
	// Deepest first, so folders go after their contents.
	sort.Slice(nodes, func(i, j int) bool { return len(nodes[i].Path) > len(nodes[j].Path) })
	for _, node := range nodes {
		if err := s.Cloud.Client.DeleteLibraryNode(ctx, s.Cloud.Bearer, node.ID); err != nil {
			return err
		}
	}
	err = s.Cache.Forget(ctx, domain, task)
	if err == nil {
		if index, rerr := os.ReadFile(filepath.Join(s.Cache.domainDir(domain), "INDEX.md")); rerr == nil {
			_ = s.Cloud.upload(ctx, harnessPrefix+"/"+domain+"/INDEX.md", index)
		}
	}
	return err
}

// SplitStore is the experience library on local disk while the run uses the
// Library for skills — the backend auto resolves to until `.harness/` exists
// server-side.
type SplitStore struct{ *LocalStore }

// Backend implements ExperienceStore.
func (s SplitStore) Backend() string { return BackendSplit }
