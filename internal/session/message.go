package session

// Message types mirror packages/schema/src/session-message.ts. The database
// stores the discriminator in session_message.type and the remainder as JSON
// in session_message.data.
type Message struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data any    `json:"-"`
}

const (
	TypeUser          = "user"
	TypeAssistant     = "assistant"
	TypeSynthetic     = "synthetic"
	TypeSystem        = "system"
	TypeShell         = "shell"
	TypeAgentSwitched = "agent-switched"
	TypeModelSwitched = "model-switched"
	TypeCompaction    = "compaction"
)

type UserMessage struct {
	Text   string            `json:"text"`
	Files  []FileAttachment  `json:"files,omitempty"`
	Agents []AgentAttachment `json:"agents,omitempty"`
	Time   TimeCreated       `json:"time"`
}

type TimeCreated struct {
	Created int64 `json:"created"`
}

type ModelRef struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Variant    string `json:"variant,omitempty"`
}

type AssistantMessage struct {
	Agent    string             `json:"agent"`
	Model    ModelRef           `json:"model"`
	Content  []AssistantContent `json:"content"`
	Finish   string             `json:"finish,omitempty"`
	Cost     *float64           `json:"cost,omitempty"`
	Tokens   *AssistantTokens   `json:"tokens,omitempty"`
	Error    *UnknownError      `json:"error,omitempty"`
	Time     AssistantTime      `json:"time"`
	Snapshot *AssistantSnapshot `json:"snapshot,omitempty"`
}

type AssistantTime struct {
	Created   int64 `json:"created"`
	Completed int64 `json:"completed,omitempty"`
}

type AssistantSnapshot struct {
	Start string   `json:"start,omitempty"`
	End   string   `json:"end,omitempty"`
	Files []string `json:"files,omitempty"`
}

type AssistantTokens struct {
	Input     int        `json:"input"`
	Output    int        `json:"output"`
	Reasoning int        `json:"reasoning"`
	Cache     CacheUsage `json:"cache"`
}

type CacheUsage struct {
	Read  int `json:"read"`
	Write int `json:"write"`
}

type UnknownError struct {
	Type    string `json:"type"`
	Message string `json:"message,omitempty"`
}

// AssistantContent is the text | reasoning | tool tagged union.
type AssistantContent struct {
	Type     string       `json:"type"`
	ID       string       `json:"id"`
	Text     string       `json:"text,omitempty"`
	Name     string       `json:"name,omitempty"`
	Provider *ToolMeta    `json:"provider,omitempty"`
	State    *ToolState   `json:"state,omitempty"`
	Time     *ContentTime `json:"time,omitempty"`
}

type ToolMeta struct {
	Executed       bool           `json:"executed"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	ResultMetadata map[string]any `json:"resultMetadata,omitempty"`
}

type ToolState struct {
	Status    string         `json:"status"`
	Input     map[string]any `json:"input,omitempty"`
	Error     string         `json:"error,omitempty"`
	Output    string         `json:"output,omitempty"`
	Completed *int64         `json:"completed,omitempty"`
}

const (
	ToolPending   = "pending"
	ToolRunning   = "running"
	ToolCompleted = "completed"
	ToolError     = "error"
)

type ContentTime struct {
	Created   int64 `json:"created"`
	Ran       int64 `json:"ran,omitempty"`
	Completed int64 `json:"completed,omitempty"`
	Pruned    int64 `json:"pruned,omitempty"`
}
