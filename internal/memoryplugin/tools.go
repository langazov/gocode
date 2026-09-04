package memoryplugin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/memory"
	"github.com/langazov/gocode-go/internal/plugin"
)

// The model-facing surface: two tools, not three.
//
// There is no memory_list, and that is a design choice rather than an
// omission: every active memory is already in the system prompt with its id
// (see memory.Render), so a listing tool would spend a round-trip telling the
// model something it can already read. If the render budget ever starts
// truncating in real use, a search tool — not a list — is the thing to add.
//
// The descriptions carry more weight here than in most tools. The failure mode
// for this feature is not a crash, it is a model that saves every passing
// remark until the prompt block is full of noise the user has to prune by
// hand. So they say plainly what belongs here and what does not.

const writeDescription = `Save a durable instruction that should be honored in future sessions, or revise one you already saved.

Use this only for standing directions: a stated preference ("always run make check before pushing"), a project constraint ("the api package must not import the tui package"), or a correction the user expects to stick.

Do NOT use this for:
- task or conversation state — that is what todowrite is for
- anything already written in AGENTS.md or the project's docs
- facts you can rediscover by reading the code
- one-off requests that only apply to the current task

Saving the same instruction twice updates the existing memory instead of duplicating it. To revise a memory that is already in your context, pass its id.`

const deleteDescription = `Delete a saved memory permanently, by the id shown in the <memories> block.

Use this when the user says an instruction no longer applies, or when they contradict one they saved earlier. Prefer revising a memory with memory_write when the instruction has changed rather than gone away.`

// tools builds the two tools bound to a store and the project they scope to.
func tools(store *memory.Store, projectID string) []plugin.Tool {
	return []plugin.Tool{
		{
			Name:        "memory_write",
			Description: writeDescription,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"content": map[string]any{
						"type": "string",
						"description": "The instruction to remember, written as a standalone imperative " +
							"sentence that will still make sense with no conversation around it.",
					},
					"id": map[string]any{
						"type": "string",
						"description": "The id of an existing memory to revise, as shown in the <memories> " +
							"block. Omit to save a new one.",
					},
					"scope": map[string]any{
						"type":        "string",
						"enum":        []string{"project", "global"},
						"description": "project (the default) applies here only; global applies in every project.",
					},
					"category": map[string]any{
						"type":        "string",
						"description": "Optional one-word label, e.g. style, workflow, architecture.",
					},
				},
				"required": []string{"content"},
			},
			Execute: func(ctx context.Context, args map[string]any, tc plugin.ToolContext) (plugin.ToolResult, error) {
				return write(ctx, store, projectID, args, tc)
			},
		},
		{
			Name:        "memory_delete",
			Description: deleteDescription,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{
						"type":        "string",
						"description": "The id of the memory to delete, as shown in the <memories> block.",
					},
				},
				"required": []string{"id"},
			},
			Execute: func(ctx context.Context, args map[string]any, tc plugin.ToolContext) (plugin.ToolResult, error) {
				return remove(ctx, store, args)
			},
		},
	}
}

func write(ctx context.Context, store *memory.Store, projectID string, args map[string]any, tc plugin.ToolContext) (plugin.ToolResult, error) {
	content := strings.TrimSpace(text(args, "content"))
	if content == "" {
		return plugin.ToolResult{}, errors.New("memory_write: content is required")
	}
	scope, err := resolveScope(text(args, "scope"), projectID)
	if err != nil {
		return plugin.ToolResult{}, fmt.Errorf("memory_write: %w", err)
	}
	category := strings.TrimSpace(text(args, "category"))

	// An id turns this into a revision of a specific memory. Everything else
	// is a save, which the store upserts on (scope, content).
	if existing := strings.TrimSpace(text(args, "id")); existing != "" {
		patch := memory.Patch{Content: &content, Scope: &scope}
		if category != "" {
			patch.Category = &category
		}
		updated, err := store.Update(ctx, existing, patch)
		if errors.Is(err, memory.ErrNotFound) {
			return plugin.ToolResult{}, fmt.Errorf(
				"memory_write: no memory with id %q; omit the id to save this as a new memory", existing)
		}
		if err != nil {
			return plugin.ToolResult{}, fmt.Errorf("memory_write: %w", err)
		}
		return result(updated, "Updated"), nil
	}

	saved, err := store.Create(ctx, memory.Memory{
		Scope:     scope,
		Content:   content,
		Category:  category,
		Origin:    memory.OriginAgent,
		SessionID: tc.SessionID,
	})
	if err != nil {
		return plugin.ToolResult{}, fmt.Errorf("memory_write: %w", err)
	}
	return result(saved, "Saved"), nil
}

func remove(ctx context.Context, store *memory.Store, args map[string]any) (plugin.ToolResult, error) {
	memoryID := strings.TrimSpace(text(args, "id"))
	if memoryID == "" {
		return plugin.ToolResult{}, errors.New("memory_delete: id is required")
	}
	// Read before deleting so the output can name what went, which is what the
	// user sees in the tool call and their only chance to catch a wrong delete.
	existing, err := store.Get(ctx, memoryID)
	if errors.Is(err, memory.ErrNotFound) {
		return plugin.ToolResult{}, fmt.Errorf("memory_delete: no memory with id %q", memoryID)
	}
	if err != nil {
		return plugin.ToolResult{}, fmt.Errorf("memory_delete: %w", err)
	}
	if err := store.Delete(ctx, memoryID); err != nil {
		return plugin.ToolResult{}, fmt.Errorf("memory_delete: %w", err)
	}
	return plugin.ToolResult{
		Title:  "Deleted memory",
		Output: fmt.Sprintf("Deleted memory %s: %s", existing.ID, existing.Content),
		Metadata: map[string]any{
			"id":    existing.ID,
			"scope": existing.Scope,
		},
	}, nil
}

// result reports what was stored, including the id, so the model can revise or
// delete this memory later in the same turn without waiting for the next
// prompt's <memories> block.
func result(item memory.Memory, verb string) plugin.ToolResult {
	scope := "this project"
	if item.Scope == memory.ScopeGlobal {
		scope = "every project"
	}
	return plugin.ToolResult{
		Title:  verb + " memory",
		Output: fmt.Sprintf("%s memory %s for %s: %s", verb, item.ID, scope, item.Content),
		Metadata: map[string]any{
			"id":       item.ID,
			"scope":    item.Scope,
			"category": item.Category,
		},
	}
}

func text(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}
