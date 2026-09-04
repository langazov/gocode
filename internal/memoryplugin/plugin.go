// Package memoryplugin exposes durable memories to the agent: it injects them
// into every system prompt and gives the model tools to record and forget
// them.
//
// It is a native plugin rather than core wiring for two reasons. The seam it
// needs already exists and is already triggered every turn
// (experimental.chat.system.transform, internal/session/runner.go), so nothing
// in the session runner has to change. And routing through the plugin host
// means memory appears in `/plugins` and GET /api/plugin with its hooks and
// tools listed, like any other extension.
//
// Storage and rendering live in internal/memory. This package is the seam
// between that store and a turn; it holds no state of its own.
package memoryplugin

import (
	"context"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/memory"
	"github.com/langazov/gocode-go/internal/plugin"
)

// Name is the plugin's id, as it appears in the status surfaces and as the ref
// a config entry uses to pass options.
const Name = "memory"

func init() { plugin.Register(Name, New) }

// New builds the hooks. It opts out — nil hooks, which the loader treats as a
// successful load that registered nothing — when there is no database to read,
// which is how the CLI subcommands that boot without one behave, and when the
// user has switched the feature off.
func New(ctx context.Context, in plugin.Input, opts plugin.Options) (*plugin.Hooks, error) {
	if in.Services.DB == nil {
		return nil, nil
	}
	settings := parseOptions(opts)
	if !settings.Enabled {
		return nil, nil
	}

	store := memory.New(in.Services.DB)
	// The project scope is fixed for the life of the host: a boot is scoped to
	// one working directory, and the runtime has already resolved it to a
	// project id.
	projectID := in.Services.ProjectID

	hooks := &plugin.Hooks{Tools: tools(store, projectID)}

	plugin.On(hooks, plugin.SystemTransform,
		func(ctx context.Context, _ plugin.SystemTransformInput, out *plugin.SystemTransformOutput) error {
			active, err := store.Active(ctx, projectID)
			if err != nil {
				return fmt.Errorf("loading memories: %w", err)
			}
			block := memory.Render(active, settings.Budget)
			if block == "" {
				return nil
			}
			// Appended last so memories read as standing amendments to the
			// agent's instructions rather than as part of them. It also keeps
			// the volatile block at the tail of the system prompt, which
			// limits how much of a cached prefix a memory edit invalidates.
			out.System = append(out.System, block)
			return nil
		})

	return hooks, nil
}

// settings is the plugin's resolved configuration.
type settings struct {
	Enabled bool
	Budget  memory.Budget
}

// parseOptions reads the options bag from a config entry:
//
//	"plugin": [["memory", {"enabled": true, "maxEntries": 50, "maxChars": 4000}]]
//
// Natives load with no options at all in the ordinary case, so every field
// defaults to something usable and an unparseable value is ignored rather than
// failing the load — a typo in a settings bag should not cost the user their
// memories.
func parseOptions(opts plugin.Options) settings {
	resolved := settings{Enabled: true, Budget: memory.DefaultBudget}
	if enabled, ok := opts["enabled"].(bool); ok {
		resolved.Enabled = enabled
	}
	if value, ok := number(opts["maxEntries"]); ok && value > 0 {
		resolved.Budget.MaxEntries = value
	}
	if value, ok := number(opts["maxChars"]); ok && value > 0 {
		resolved.Budget.MaxChars = value
	}
	return resolved
}

// number accepts both float64 (how JSON decodes into an any) and int (how a
// Go caller would write it in a test).
func number(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	}
	return 0, false
}

// resolveScope maps the model-facing scope word onto a stored scope value. The
// tool takes "project"/"global" rather than a raw id because a project id here
// is an absolute filesystem path, which is neither guessable nor stable enough
// to put in a tool schema.
func resolveScope(requested, projectID string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(requested)) {
	case "", "project", "local":
		if projectID == "" {
			return memory.ScopeGlobal, nil
		}
		return projectID, nil
	case "global", "user", "all":
		return memory.ScopeGlobal, nil
	default:
		return "", fmt.Errorf("unknown scope %q: use \"project\" or \"global\"", requested)
	}
}
