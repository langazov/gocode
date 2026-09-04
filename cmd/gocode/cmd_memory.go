package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/db"
	"github.com/langazov/gocode-go/internal/memory"
	"github.com/langazov/gocode-go/internal/session"
)

// memoryCommand manages durable memories from the shell, the way `session`
// manages sessions.
//
// It exists because the two other ways in are both inside a running agent: the
// TUI's /memory manager, and the tools the model calls. Seeding a project's
// memories from a script, or checking what is in there without starting a
// session, needs neither.
//
// It talks to the store directly rather than to a running server. Nothing else
// has to be up, and the memory table is the same one a server would serve —
// which does mean a `memory add` while the TUI is open will not show up there
// until its next refresh.
func memoryCommand() *clix.Command {
	scopeFlag := clix.Flag{
		Name: "scope", Kind: clix.KindString, Default: "project",
		Choices:  []string{"project", "global"},
		Describe: "project applies here only; global applies everywhere",
	}
	return &clix.Command{
		Name:     "memory",
		Describe: "manage durable memories",
		Demand:   true,
		Sub: []*clix.Command{
			{
				Name:     "add",
				Describe: "save a memory",
				Positionals: []clix.Positional{
					{Name: "content", Array: true, Required: true, Describe: "the instruction to remember"},
				},
				Flags: []clix.Flag{
					scopeFlag,
					{Name: "category", Kind: clix.KindString, Describe: "optional label, e.g. style or workflow"},
					{Name: "pin", Kind: clix.KindBool, Describe: "keep this memory ahead of others in the prompt budget"},
				},
				Run: runMemoryAdd,
			},
			{
				Name:     "list",
				Describe: "list memories",
				Flags: []clix.Flag{
					{Name: "scope", Kind: clix.KindString, Choices: []string{"project", "global", "all"},
						Default: "all", Describe: "which memories to show"},
					{Name: "format", Kind: clix.KindString, Default: "table", Choices: []string{"table", "json"},
						Describe: "output format"},
				},
				Run: runMemoryList,
			},
			{
				Name:        "rm",
				Describe:    "delete a memory",
				Positionals: []clix.Positional{{Name: "memoryID", Required: true, Describe: "memory ID to delete"}},
				Run:         runMemoryRemove,
			},
			{
				Name:     "edit",
				Describe: "revise a memory",
				Positionals: []clix.Positional{
					{Name: "memoryID", Required: true, Describe: "memory ID to revise"},
					{Name: "content", Array: true, Required: true, Describe: "the new wording"},
				},
				Flags: []clix.Flag{scopeFlag},
				Run:   runMemoryEdit,
			},
		},
	}
}

// openMemoryStore opens the database and resolves the project the way
// bootStack does, so the CLI and a running agent agree on what "this project"
// means.
func openMemoryStore(ctx context.Context) (*memory.Store, string, func(), error) {
	database, err := db.OpenDefault(ctx)
	if err != nil {
		return nil, "", nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		database.Close()
		return nil, "", nil, err
	}
	projectID, err := session.EnsureProject(ctx, database, cwd)
	if err != nil {
		database.Close()
		return nil, "", nil, err
	}
	return memory.New(database), projectID, func() { database.Close() }, nil
}

// cliScope maps the flag word onto a stored scope value, matching the tool and
// the HTTP routes.
func cliScope(requested, projectID string) string {
	if requested == "global" || projectID == "" {
		return memory.ScopeGlobal
	}
	return projectID
}

func runMemoryAdd(a *clix.Args) error {
	ctx := context.Background()
	store, projectID, closeFn, err := openMemoryStore(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	content := strings.TrimSpace(strings.Join(a.Array("content"), " "))
	if content == "" {
		return errors.New("a memory needs content")
	}
	pin := a.Bool("pin")
	saved, err := store.Create(ctx, memory.Memory{
		Scope:    cliScope(a.String("scope"), projectID),
		Content:  content,
		Category: a.String("category"),
		Origin:   memory.OriginUser,
		Pinned:   pin,
	})
	if err != nil {
		return err
	}
	// Create upserts, and its conflict clause deliberately leaves `pinned`
	// alone: an agent re-saving an instruction must not silently un-pin what
	// the user pinned. That makes --pin a no-op when the memory already
	// existed, so apply it explicitly here — an ignored flag is worse than a
	// second write.
	if pin && !saved.Pinned {
		saved, err = store.Update(ctx, saved.ID, memory.Patch{Pinned: &pin})
		if err != nil {
			return err
		}
	}
	fmt.Printf("Remembered %s for %s: %s\n", saved.ID, scopeWord(saved.Scope), saved.Content)
	return nil
}

func runMemoryList(a *clix.Args) error {
	ctx := context.Background()
	store, projectID, closeFn, err := openMemoryStore(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	input := memory.ListInput{IncludeDisabled: true}
	switch a.String("scope") {
	case "project":
		input.Scopes = []string{cliScope("project", projectID)}
	case "global":
		input.Scopes = []string{memory.ScopeGlobal}
	default:
		input.Scopes = []string{memory.ScopeGlobal, cliScope("project", projectID)}
	}

	memories, err := store.List(ctx, input)
	if err != nil {
		return err
	}

	if a.String("format") == "json" {
		data, err := json.MarshalIndent(memories, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	if len(memories) == 0 {
		fmt.Println("No memories yet. Add one with: gocode memory add \"<instruction>\"")
		return nil
	}

	maxID := 10
	for _, item := range memories {
		if len(item.ID) > maxID {
			maxID = len(item.ID)
		}
	}
	header := fmt.Sprintf("Memory ID%s  Scope    Updated           Content", pad("", maxID-9))
	fmt.Println(header)
	fmt.Println(repeat("─", len(header)))
	for _, item := range memories {
		fmt.Printf("%s  %s  %s  %s%s\n",
			padRight(item.ID, maxID),
			padRight(scopeColumn(item.Scope), 7),
			time.UnixMilli(item.TimeUpdated).Format("2006-01-02 15:04"),
			memoryMarks(item),
			truncate(strings.Join(strings.Fields(item.Content), " "), 60))
	}
	return nil
}

func runMemoryRemove(a *clix.Args) error {
	ctx := context.Background()
	store, _, closeFn, err := openMemoryStore(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	memoryID := a.Pos["memoryID"]
	// Read first so the confirmation can name what went — the user's only
	// check that they deleted the memory they meant to.
	existing, err := store.Get(ctx, memoryID)
	if errors.Is(err, memory.ErrNotFound) {
		return fmt.Errorf("Memory not found: %s", memoryID)
	}
	if err != nil {
		return err
	}
	if err := store.Delete(ctx, memoryID); err != nil {
		return err
	}
	fmt.Printf("Deleted %s: %s\n", existing.ID, existing.Content)
	return nil
}

func runMemoryEdit(a *clix.Args) error {
	ctx := context.Background()
	store, projectID, closeFn, err := openMemoryStore(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	content := strings.TrimSpace(strings.Join(a.Array("content"), " "))
	if content == "" {
		return errors.New("a memory needs content")
	}
	patch := memory.Patch{Content: &content}
	// Only move the scope when it was actually asked for, so an edit does not
	// silently drag a global memory back into the project.
	if a.Has("scope") {
		scope := cliScope(a.String("scope"), projectID)
		patch.Scope = &scope
	}

	memoryID := a.Pos["memoryID"]
	updated, err := store.Update(ctx, memoryID, patch)
	if errors.Is(err, memory.ErrNotFound) {
		return fmt.Errorf("Memory not found: %s", memoryID)
	}
	if err != nil {
		return err
	}
	fmt.Printf("Updated %s for %s: %s\n", updated.ID, scopeWord(updated.Scope), updated.Content)
	return nil
}

func scopeWord(scope string) string {
	if scope == memory.ScopeGlobal {
		return "every project"
	}
	return "this project"
}

func scopeColumn(scope string) string {
	if scope == memory.ScopeGlobal {
		return "global"
	}
	return "project"
}

// memoryMarks prefixes the content column with the flags that change how a
// memory behaves, so `list` shows why one is or is not in the prompt.
func memoryMarks(item memory.Memory) string {
	var marks string
	if item.Pinned {
		marks += "*"
	}
	if item.Disabled {
		marks += "(muted) "
	}
	return marks
}
