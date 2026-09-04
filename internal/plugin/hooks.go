package plugin

import "github.com/langazov/gocode-go/internal/config"

// The hook catalog, porting the `Hooks` interface in
// packages/plugin/src/index.ts. Each entry binds the wire name a plugin
// registers under to the input it is handed and the output it may mutate.
//
// Adding a hook is three steps: define it here with its input/output types,
// trigger it at the seam that owns the decision, and document it. Nothing else
// needs to change — the dispatch table is keyed by name and both tiers reach
// it through the same [Trigger].
var (
	// ConfigHook lets a plugin mutate the merged config after load, porting
	// `config`. TypeScript passes the config as the sole argument and mutates
	// it; here it arrives as the output, since that is the value that flows
	// back to the runtime.
	ConfigHook = Define[Empty, ConfigOutput]("config")

	// EventHook notifies plugins of a committed event, porting `event`. It
	// collects nothing, so it is the one hook with an empty output.
	EventHook = Define[EventInput, Empty]("event")

	// ChatMessage fires when a user message is received, porting
	// `chat.message`.
	ChatMessage = Define[ChatMessageInput, ChatMessageOutput]("chat.message")

	// ChatParams adjusts the sampling parameters of a provider request,
	// porting `chat.params`.
	ChatParams = Define[ChatInput, ChatParamsOutput]("chat.params")

	// ChatHeaders adjusts the HTTP headers of a provider request, porting
	// `chat.headers`.
	ChatHeaders = Define[ChatInput, ChatHeadersOutput]("chat.headers")

	// PermissionAsk lets a plugin answer a permission request before the user
	// is asked, porting `permission.ask`.
	PermissionAsk = Define[PermissionAskInput, PermissionAskOutput]("permission.ask")

	// ToolExecuteBefore rewrites a tool call's arguments, porting
	// `tool.execute.before`.
	ToolExecuteBefore = Define[ToolExecuteBeforeInput, ToolExecuteBeforeOutput]("tool.execute.before")

	// ToolExecuteAfter rewrites a tool call's result, porting
	// `tool.execute.after`.
	ToolExecuteAfter = Define[ToolExecuteAfterInput, ToolExecuteAfterOutput]("tool.execute.after")

	// ToolDefinition rewrites what a tool looks like to the model, porting
	// `tool.definition`.
	ToolDefinition = Define[ToolDefinitionInput, ToolDefinitionOutput]("tool.definition")

	// CommandExecuteBefore runs before a slash command expands, porting
	// `command.execute.before`.
	CommandExecuteBefore = Define[CommandExecuteBeforeInput, CommandExecuteBeforeOutput]("command.execute.before")

	// ShellEnv contributes environment variables to a spawned shell, porting
	// `shell.env`.
	ShellEnv = Define[ShellEnvInput, ShellEnvOutput]("shell.env")

	// SystemTransform rewrites the assembled system prompt, porting
	// `experimental.chat.system.transform`.
	SystemTransform = Define[SystemTransformInput, SystemTransformOutput]("experimental.chat.system.transform")

	// SessionCompacting customizes the compaction prompt, porting
	// `experimental.session.compacting`.
	SessionCompacting = Define[SessionCompactingInput, SessionCompactingOutput]("experimental.session.compacting")

	// TextComplete rewrites a finished assistant text part, porting
	// `experimental.text.complete`.
	TextComplete = Define[TextCompleteInput, TextCompleteOutput]("experimental.text.complete")
)

// ConfigOutput carries the merged config for a plugin to mutate.
type ConfigOutput struct {
	Config *config.Config `json:"config"`
}

// Event is a committed event as a plugin sees it, porting the SDK `Event`
// union's common shape. The boot wiring converts an event.Payload into this so
// the plugin package stays clear of the event store.
type Event struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// EventInput wraps the event, matching TypeScript's `{ event }` argument.
type EventInput struct {
	Event Event `json:"event"`
}

// ProviderInfo identifies the provider serving a request, porting
// `ProviderContext`.
type ProviderInfo struct {
	ID string `json:"id"`
	// Source is where the provider's configuration came from: "env",
	// "config", "custom" or "api".
	Source  string         `json:"source,omitempty"`
	Options map[string]any `json:"options,omitempty"`
}

// ModelInfo identifies the model serving a request.
type ModelInfo struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
	Name       string `json:"name,omitempty"`
	// Variant is the selected reasoning/effort variant, empty when none.
	Variant string `json:"variant,omitempty"`
}

// ChatInput is the shared input of the per-request hooks, porting the object
// `chat.params` and `chat.headers` both receive.
type ChatInput struct {
	SessionID string       `json:"sessionID"`
	Agent     string       `json:"agent"`
	Provider  ProviderInfo `json:"provider"`
	Model     ModelInfo    `json:"model"`
	// MessageID is the user message the turn is answering.
	MessageID string `json:"messageID,omitempty"`
}

// ChatParamsOutput is the mutable sampling configuration for a request.
//
// The numeric fields are pointers because zero is a real value a plugin may
// want to set — TypeScript distinguishes `temperature: 0` from an untouched
// parameter with `undefined`, and a bare float64 could not.
type ChatParamsOutput struct {
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            *float64       `json:"topP,omitempty"`
	TopK            *float64       `json:"topK,omitempty"`
	MaxOutputTokens *int           `json:"maxOutputTokens,omitempty"`
	Options         map[string]any `json:"options,omitempty"`
}

// ChatHeadersOutput is the mutable header set for a provider request.
type ChatHeadersOutput struct {
	Headers map[string]string `json:"headers"`
}

// MessageInfo is a user message as a plugin sees it. It is a deliberately
// small projection of the SDK's `UserMessage`: the fields a plugin can act on
// without the port having to mirror the whole message model.
type MessageInfo struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	Role      string `json:"role"`
	Time      int64  `json:"time,omitempty"`
}

// Part is one piece of a message, porting the SDK `Part` union loosely: Type
// discriminates, and the fields a given type does not use stay empty.
type Part struct {
	ID   string `json:"id,omitempty"`
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// Filename, Mime and URL carry a file part.
	Filename string         `json:"filename,omitempty"`
	Mime     string         `json:"mime,omitempty"`
	URL      string         `json:"url,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ChatMessageInput describes the turn a received message starts.
type ChatMessageInput struct {
	SessionID string     `json:"sessionID"`
	Agent     string     `json:"agent,omitempty"`
	Model     *ModelInfo `json:"model,omitempty"`
	MessageID string     `json:"messageID,omitempty"`
	Variant   string     `json:"variant,omitempty"`
}

// ChatMessageOutput is the message and its parts, both mutable.
type ChatMessageOutput struct {
	Message MessageInfo `json:"message"`
	Parts   []Part      `json:"parts,omitempty"`
}

// Permission statuses, porting the `status` union on `permission.ask`.
const (
	// PermissionAskStatus leaves the decision to the user.
	PermissionAskStatus = "ask"
	// PermissionAllow approves without asking.
	PermissionAllow = "allow"
	// PermissionDeny rejects without asking.
	PermissionDeny = "deny"
)

// PermissionAskInput describes the approval being requested, porting the SDK
// `Permission` object.
type PermissionAskInput struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID,omitempty"`
	CallID    string `json:"callID,omitempty"`
	// Action is the permission action being asked about ("edit", "bash",
	// "external_directory", ...).
	Action string `json:"action"`
	// Resources are the concrete things the action would touch: paths for a
	// file tool, the command for a shell.
	Resources []string       `json:"resources,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// PermissionAskOutput carries the decision. It starts at
// [PermissionAskStatus]; a plugin that leaves it alone changes nothing.
type PermissionAskOutput struct {
	Status string `json:"status"`
}

// ToolExecuteBeforeInput identifies the call whose arguments may be rewritten.
type ToolExecuteBeforeInput struct {
	Tool      string `json:"tool"`
	SessionID string `json:"sessionID"`
	CallID    string `json:"callID"`
}

// ToolExecuteBeforeOutput carries the arguments the tool will run with.
type ToolExecuteBeforeOutput struct {
	Args map[string]any `json:"args"`
}

// ToolExecuteAfterInput identifies the settled call.
type ToolExecuteAfterInput struct {
	Tool      string         `json:"tool"`
	SessionID string         `json:"sessionID"`
	CallID    string         `json:"callID"`
	Args      map[string]any `json:"args,omitempty"`
}

// ToolExecuteAfterOutput carries the result the model will see.
type ToolExecuteAfterOutput struct {
	Title    string         `json:"title,omitempty"`
	Output   string         `json:"output"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ToolDefinitionInput names the tool being advertised.
type ToolDefinitionInput struct {
	ToolID string `json:"toolID"`
}

// ToolDefinitionOutput carries the description and JSON Schema sent to the
// model.
type ToolDefinitionOutput struct {
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// CommandExecuteBeforeInput describes the slash command about to expand.
type CommandExecuteBeforeInput struct {
	Command   string `json:"command"`
	SessionID string `json:"sessionID"`
	Arguments string `json:"arguments,omitempty"`
}

// CommandExecuteBeforeOutput carries the parts the command expands into.
type CommandExecuteBeforeOutput struct {
	Parts []Part `json:"parts,omitempty"`
}

// ShellEnvInput describes the shell about to be spawned.
type ShellEnvInput struct {
	Cwd       string `json:"cwd"`
	SessionID string `json:"sessionID,omitempty"`
	CallID    string `json:"callID,omitempty"`
}

// ShellEnvOutput carries the environment overlay for the spawned shell.
type ShellEnvOutput struct {
	Env map[string]string `json:"env"`
}

// SystemTransformInput describes the turn whose system prompt is assembled.
type SystemTransformInput struct {
	SessionID string    `json:"sessionID,omitempty"`
	Model     ModelInfo `json:"model"`
}

// SystemTransformOutput carries the ordered system prompt blocks.
type SystemTransformOutput struct {
	System []string `json:"system"`
}

// SessionCompactingInput names the session about to be compacted.
type SessionCompactingInput struct {
	SessionID string `json:"sessionID"`
}

// SessionCompactingOutput customizes compaction: Context is appended to the
// default prompt, and a non-empty Prompt replaces it outright.
type SessionCompactingOutput struct {
	Context []string `json:"context,omitempty"`
	Prompt  string   `json:"prompt,omitempty"`
}

// TextCompleteInput identifies the finished assistant text part.
type TextCompleteInput struct {
	SessionID string `json:"sessionID"`
	MessageID string `json:"messageID"`
	PartID    string `json:"partID"`
}

// TextCompleteOutput carries the text, which a plugin may rewrite.
type TextCompleteOutput struct {
	Text string `json:"text"`
}
