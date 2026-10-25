package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/langazov/gocode-go/internal/flock"
)

// The experience library, porting harness2/core/library.py: per domain, what
// earlier tasks adopted and the lesson each round supported.
//
//	<root>/<domain>/INDEX.md
//	<root>/<domain>/harnesses/<task>/harness/<rel>   the full adopted tree
//	<root>/<domain>/harnesses/<task>/EXPERIENCE.md   why it was adopted + the lesson
//
// Retrieval is by construction: a domain's directory only ever holds
// same-domain entries, and the whole tree is carried, never a summary.
// (It is the *experience* library, to keep it apart from the gocoder.org
// Library that library-plugin serves — see cloud.go.)

// Entry is one adopted harness in the library.
type Entry struct {
	Domain  string     `json:"domain"`
	Task    string     `json:"task"`
	Label   string     `json:"label"`
	Why     string     `json:"why,omitempty"`
	Lesson  Experience `json:"lesson"`
	Tree    Tree       `json:"-"`
	Pending bool       `json:"pending,omitempty"` // written locally, not yet uploaded
}

// EntryInfo is the listing view of an entry.
type EntryInfo struct {
	Domain     string `json:"domain"`
	Task       string `json:"task"`
	Cue        string `json:"cue,omitempty"`
	Components string `json:"components"`
	Pending    bool   `json:"pending,omitempty"`
}

// ExperienceStore is where adopted harnesses and lessons live. LocalStore
// keeps them on disk; CloudStore (cloud.go) keeps them in the gocoder.org
// Library with a local cache.
type ExperienceStore interface {
	// Backend names the store for summaries and the interface: local, cloud
	// or split.
	Backend() string
	// ContextDir returns a local directory holding the domain's library for
	// an improver to read, or "" while the domain has no entries.
	ContextDir(ctx context.Context, domain string) (string, error)
	Add(ctx context.Context, entry Entry) error
	List(ctx context.Context, domain string) ([]EntryInfo, error)
	Domains(ctx context.Context) ([]string, error)
	Get(ctx context.Context, domain, task string) (Entry, error)
	Forget(ctx context.Context, domain, task string) error
}

// LocalStore is the experience library on the local filesystem.
//
// Writes to a domain hold its lock: an exclusive advisory file lock, so two
// gocode processes adapting tasks in the same domain cannot interleave an
// entry write with the INDEX.md rebuild (harness2 serialises its summary
// writes the same way), plus an in-process mutex for platforms where the file
// lock is advisory-only or a no-op.
type LocalStore struct {
	Root string

	mu sync.Mutex
}

// withLock runs fn holding the domain's write lock.
func (s *LocalStore) withLock(domain string, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.domainDir(domain), 0o755); err != nil {
		return err
	}
	lock, err := flock.Lock(filepath.Join(s.domainDir(domain), ".lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn()
}

// Backend implements ExperienceStore.
func (s *LocalStore) Backend() string { return "local" }

func (s *LocalStore) domainDir(domain string) string { return filepath.Join(s.Root, domain) }

func (s *LocalStore) entryDir(domain, task string) string {
	return filepath.Join(s.Root, domain, "harnesses", task)
}

// ContextDir implements ExperienceStore.
func (s *LocalStore) ContextDir(_ context.Context, domain string) (string, error) {
	entries, err := s.tasks(domain)
	if err != nil || len(entries) == 0 {
		return "", err
	}
	return s.domainDir(domain), nil
}

func (s *LocalStore) tasks(domain string) ([]string, error) {
	items, err := os.ReadDir(filepath.Join(s.domainDir(domain), "harnesses"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, item := range items {
		if item.IsDir() {
			if _, err := os.Stat(filepath.Join(s.entryDir(domain, item.Name()), "EXPERIENCE.md")); err == nil {
				out = append(out, item.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Add stores one adopted tree and its lesson, then rebuilds INDEX.md. An
// entry without a complete lesson is not stored: adoption alone never
// establishes that an edit helped.
func (s *LocalStore) Add(_ context.Context, entry Entry) error {
	if !entry.Lesson.Complete() {
		return fmt.Errorf("experience entry for %s has no complete lesson", entry.Task)
	}
	if err := AssertTreeHasNoGraderVerdicts(entry.Tree); err != nil {
		return err
	}
	if err := AssertTreeHasNoGraderVerdicts(Tree{"EXPERIENCE.md": entry.Why + entry.Lesson.Cue + entry.Lesson.Lesson + entry.Lesson.Evidence}); err != nil {
		return err
	}
	return s.withLock(entry.Domain, func() error {
		dir := s.entryDir(entry.Domain, entry.Task)
		if err := os.RemoveAll(filepath.Join(dir, "harness")); err != nil {
			return err
		}
		if _, err := WriteTree(entry.Tree, filepath.Join(dir, "harness"), false); err != nil {
			return err
		}
		if err := writeFileAll(filepath.Join(dir, "EXPERIENCE.md"), experienceMarkdown(entry)); err != nil {
			return err
		}
		return s.rebuildIndex(entry.Domain)
	})
}

func experienceMarkdown(entry Entry) string {
	lines := []string{
		"# " + entry.Task,
		"",
		"- adopted: `" + entry.Label + "`",
		"- components: " + inventory(entry.Tree),
		"",
	}
	if why := strings.TrimSpace(entry.Why); why != "" {
		lines = append(lines, "## Why this was chosen", why, "")
	}
	if edits := strings.TrimSpace(entry.Lesson.Edits); edits != "" {
		lines = append(lines, "## What each edit did", edits, "")
	}
	lines = append(lines, "## Transferable lesson")
	for _, field := range [][2]string{
		{"cue", entry.Lesson.Cue}, {"lesson", entry.Lesson.Lesson},
		{"evidence", entry.Lesson.Evidence}, {"confidence", entry.Lesson.Confidence},
	} {
		if strings.TrimSpace(field[1]) != "" {
			lines = append(lines, "- **"+field[0]+"**: "+strings.TrimSpace(field[1]))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// parseExperienceMarkdown recovers the adopted label, the selection reason
// and the lesson from EXPERIENCE.md.
func parseExperienceMarkdown(text string) (string, string, Experience) {
	var exp Experience
	label := ""
	section := ""
	var why, edits []string
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "- adopted: `"):
			label = strings.TrimSuffix(strings.TrimPrefix(line, "- adopted: `"), "`")
		case strings.HasPrefix(line, "## "):
			section = strings.TrimPrefix(line, "## ")
		case strings.HasPrefix(line, "- **"):
			key, value, ok := strings.Cut(strings.TrimPrefix(line, "- **"), "**:")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch key {
			case "cue":
				exp.Cue = value
			case "lesson":
				exp.Lesson = value
			case "evidence":
				exp.Evidence = value
			case "confidence":
				exp.Confidence = value
			}
		default:
			switch section {
			case "Why this was chosen":
				why = append(why, line)
			case "What each edit did":
				edits = append(edits, line)
			}
		}
	}
	exp.Edits = strings.TrimSpace(strings.Join(edits, "\n"))
	return label, strings.TrimSpace(strings.Join(why, "\n")), exp
}

// inventory is a one-line census of the components in a tree.
func inventory(tree Tree) string {
	var bits []string
	if n := len(SkillNames(tree)); n > 0 {
		bits = append(bits, fmt.Sprintf("%d skills", n))
	}
	if n := len(AgentNames(tree)); n > 0 {
		bits = append(bits, fmt.Sprintf("%d subagents", n))
	}
	if n := ScriptCount(tree); n > 0 {
		bits = append(bits, fmt.Sprintf("%d scripts", n))
	}
	if _, ok := tree[FileHooks]; ok {
		bits = append(bits, "hooks")
	}
	if len(bits) == 0 {
		bits = []string{"always-on edits only"}
	}
	return fmt.Sprintf("%s; %d always-on chars", strings.Join(bits, ", "), len(AlwaysOnText(tree)))
}

func (s *LocalStore) rebuildIndex(domain string) error {
	rows := []string{
		"# Library — " + domain,
		"",
		"Each entry is an earlier task in THIS domain whose harness was chosen, stored with its FULL component tree " +
			"and why it was picked. Browse this index, then open the entries that look relevant to the matter in front of you.",
		"",
	}
	tasks, err := s.tasks(domain)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		rows = append(rows, "_(empty — this is the first task in this domain)_")
	}
	for _, task := range tasks {
		dir := s.entryDir(domain, task)
		tree, _ := ReadTree(filepath.Join(dir, "harness"), nil)
		_, _, exp := parseExperienceMarkdown(readOr(filepath.Join(dir, "EXPERIENCE.md"), ""))
		rows = append(rows,
			"## `harnesses/"+task+"/`",
			"- harness: `harnesses/"+task+"/harness/` — "+inventory(tree),
			"- why:     `harnesses/"+task+"/EXPERIENCE.md`",
		)
		if exp.Cue != "" {
			rows = append(rows, "- cue:     "+exp.Cue)
		}
		rows = append(rows, "")
	}
	return writeFileAll(filepath.Join(s.domainDir(domain), "INDEX.md"), strings.Join(rows, "\n"))
}

// List implements ExperienceStore.
func (s *LocalStore) List(_ context.Context, domain string) ([]EntryInfo, error) {
	tasks, err := s.tasks(domain)
	if err != nil {
		return nil, err
	}
	out := make([]EntryInfo, 0, len(tasks))
	for _, task := range tasks {
		dir := s.entryDir(domain, task)
		tree, _ := ReadTree(filepath.Join(dir, "harness"), nil)
		_, _, exp := parseExperienceMarkdown(readOr(filepath.Join(dir, "EXPERIENCE.md"), ""))
		_, pending := os.Stat(filepath.Join(dir, pendingMarker))
		out = append(out, EntryInfo{Domain: domain, Task: task, Cue: exp.Cue, Components: inventory(tree), Pending: pending == nil})
	}
	return out, nil
}

// pendingMarker flags an entry the cloud store has not uploaded yet.
const pendingMarker = ".pending"

// Domains implements ExperienceStore.
func (s *LocalStore) Domains(_ context.Context) ([]string, error) {
	items, err := os.ReadDir(s.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, item := range items {
		if item.IsDir() && !strings.HasPrefix(item.Name(), ".") {
			if tasks, _ := s.tasks(item.Name()); len(tasks) > 0 {
				out = append(out, item.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Get implements ExperienceStore.
func (s *LocalStore) Get(_ context.Context, domain, task string) (Entry, error) {
	dir := s.entryDir(domain, task)
	text, err := os.ReadFile(filepath.Join(dir, "EXPERIENCE.md"))
	if err != nil {
		return Entry{}, fmt.Errorf("no experience entry %s/%s", domain, task)
	}
	tree, err := ReadTree(filepath.Join(dir, "harness"), nil)
	if err != nil {
		return Entry{}, err
	}
	label, why, exp := parseExperienceMarkdown(string(text))
	_, pending := os.Stat(filepath.Join(dir, pendingMarker))
	return Entry{Domain: domain, Task: task, Label: label, Why: why, Lesson: exp, Tree: tree, Pending: pending == nil}, nil
}

// Forget implements ExperienceStore.
func (s *LocalStore) Forget(_ context.Context, domain, task string) error {
	dir := s.entryDir(domain, task)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no experience entry %s/%s", domain, task)
	}
	return s.withLock(domain, func() error {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
		return s.rebuildIndex(domain)
	})
}
