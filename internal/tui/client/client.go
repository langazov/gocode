// Package client is the TUI's API client, the Go equivalent of the
// TypeScript TUI's SDK usage against the opencode server.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

type Session struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectID"`
	// ParentID links a subagent/forked session to the session that spawned
	// it. The subagent footer (routes/session/subagent-footer.tsx) keys off
	// exactly this: it renders only when the open session has a parent.
	ParentID    string    `json:"parentID,omitempty"`
	Title       string    `json:"title"`
	Directory   string    `json:"directory"`
	Version     string    `json:"version"`
	Model       *ModelRef `json:"model,omitempty"`
	TimeCreated int64     `json:"timeCreated"`
	TimeUpdated int64     `json:"timeUpdated"`
}

type ModelRef struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Variant    string `json:"variant,omitempty"`
}

type Message struct {
	ID          string          `json:"id"`
	SessionID   string          `json:"sessionID"`
	Type        string          `json:"type"`
	Seq         int             `json:"seq"`
	TimeCreated int64           `json:"timeCreated"`
	Data        json.RawMessage `json:"data"`
}

type PermissionRequest struct {
	ID        string   `json:"id"`
	SessionID string   `json:"sessionID"`
	Agent     string   `json:"agent,omitempty"`
	Action    string   `json:"action"`
	Resources []string `json:"resources"`
}

type CreateInput struct {
	Directory string `json:"directory"`
	Title     string `json:"title,omitempty"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = nil
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out []Session
	err := c.do(ctx, http.MethodGet, "/api/session", nil, &out)
	return out, err
}

func (c *Client) Session(ctx context.Context, id string) (*Session, error) {
	var out Session
	err := c.do(ctx, http.MethodGet, "/api/session/"+id, nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CreateSession(ctx context.Context, input CreateInput) (*Session, error) {
	var out Session
	err := c.do(ctx, http.MethodPost, "/api/session", input, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Prompt(ctx context.Context, sessionID, text string) (string, error) {
	return c.PromptWith(ctx, sessionID, text, nil)
}

// PromptWith sends a message that may carry attachments, each a data: URI.
func (c *Client) PromptWith(ctx context.Context, sessionID, text string, files []FileAttachment) (string, error) {
	var out struct {
		MessageID string `json:"messageID"`
	}
	body := map[string]any{"text": text}
	if len(files) > 0 {
		body["files"] = files
	}
	err := c.do(ctx, http.MethodPost, "/api/session/"+sessionID+"/prompt", body, &out)
	return out.MessageID, err
}

func (c *Client) Interrupt(ctx context.Context, sessionID string) error {
	return c.do(ctx, http.MethodPost, "/api/session/"+sessionID+"/interrupt", map[string]any{}, nil)
}

func (c *Client) Messages(ctx context.Context, sessionID string) ([]Message, error) {
	var out []Message
	err := c.do(ctx, http.MethodGet, "/api/session/"+sessionID+"/message", nil, &out)
	return out, err
}

func (c *Client) Permissions(ctx context.Context, sessionID string) ([]PermissionRequest, error) {
	var out []PermissionRequest
	err := c.do(ctx, http.MethodGet, "/api/session/"+sessionID+"/permission", nil, &out)
	return out, err
}

func (c *Client) Reply(ctx context.Context, sessionID, requestID, reply string) error {
	return c.do(ctx, http.MethodPost,
		"/api/session/"+sessionID+"/permission/"+requestID+"/reply",
		map[string]string{"reply": reply}, nil)
}

// Event is a committed event from the /api/event SSE stream.
type Event struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Data    map[string]any `json:"data,omitempty"`
	Seq     *int           `json:"seq,omitempty"`
	Session string         `json:"sessionID,omitempty"`
}

// Events streams the server's event SSE feed until the context is canceled.
func (c *Client) Events(ctx context.Context, sessionID string) (<-chan Event, error) {
	url := c.BaseURL + "/api/event"
	if sessionID != "" {
		url += "?sessionID=" + sessionID
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	streamClient := &http.Client{Timeout: 0}
	res, err := streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, fmt.Errorf("event stream: %d", res.StatusCode)
	}
	out := make(chan Event, 128)
	go func() {
		defer res.Body.Close()
		defer close(out)
		scanner := bufio.NewScanner(res.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil {
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// AssistantData mirrors the projected assistant message payload.
type AssistantData struct {
	Agent string   `json:"agent"`
	Model ModelRef `json:"model"`
	Time  struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Content []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Text string `json:"text"`
		Name string `json:"name"`
		// Time is only populated for text/reasoning parts (set by
		// projectContentStarted/Ended); Created marks when the part started
		// streaming, Completed when it finished — reasoningBlock uses these
		// for the "done" state and duration label.
		Time *struct {
			Created   int64 `json:"created"`
			Completed int64 `json:"completed"`
		} `json:"time"`
		State *struct {
			Status string         `json:"status"`
			Input  map[string]any `json:"input"`
			Output string         `json:"output"`
			Error  string         `json:"error"`
		} `json:"state"`
	} `json:"content"`
	Finish string `json:"finish"`
	// Tokens and Cost back the footer's usage segment (prompt/index.tsx's
	// usage() memo reads them off the last assistant message).
	Tokens *AssistantTokens `json:"tokens,omitempty"`
	Cost   *float64         `json:"cost,omitempty"`
	Error  *AssistantError  `json:"error"`
}

// AssistantError is a settled assistant message's error. Type distinguishes a
// user-ordered interruption ("aborted") from a real failure ("unknown") — the
// port's stand-in for the TypeScript schema's named MessageAbortedError, which
// the original's UI branches on in several places.
type AssistantError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// AssistantTokens mirrors internal/session.AssistantTokens.
type AssistantTokens struct {
	Input     int `json:"input"`
	Output    int `json:"output"`
	Reasoning int `json:"reasoning"`
	Cache     struct {
		Read  int `json:"read"`
		Write int `json:"write"`
	} `json:"cache"`
}

// Total sums every token bucket the original adds up for the usage row:
// input + output + reasoning + cache read + cache write.
func (t *AssistantTokens) Total() int {
	if t == nil {
		return 0
	}
	return t.Input + t.Output + t.Reasoning + t.Cache.Read + t.Cache.Write
}

type UserData struct {
	Text  string           `json:"text"`
	Files []FileAttachment `json:"files,omitempty"`
}

// FileAttachment mirrors internal/session.FileAttachment (the subset the
// TUI renders as a pill: a directory-vs-file badge plus the name).
type FileAttachment struct {
	// URI is a data: URI when the interface is sending an attachment. It is
	// absent on attachments read back from a message, which only carry enough
	// to render the pill.
	URI  string `json:"uri,omitempty"`
	Mime string `json:"mime"`
	Name string `json:"name,omitempty"`
}

func DecodeUser(data json.RawMessage) (UserData, error) {
	var out UserData
	err := json.Unmarshal(data, &out)
	return out, err
}

func DecodeAssistant(data json.RawMessage) (AssistantData, error) {
	var out AssistantData
	err := json.Unmarshal(data, &out)
	return out, err
}

type Model struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	// ContextLimit is models.dev's `limit.context`. The prompt hint row's
	// usage segment divides the running token total by it to produce the
	// "(16%)" share; zero means unknown, and the original then renders the
	// token count with no percentage at all.
	ContextLimit int `json:"contextLimit,omitempty"`
	// CostInput is models.dev's `cost.input`. Zero across every model of a
	// provider is what marks that provider as free.
	CostInput float64 `json:"costInput,omitempty"`
}

type Provider struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Connected reports a stored credential; Available that the provider is
	// usable right now (a credential, or env vars, or config).
	Connected bool `json:"connected,omitempty"`
	Available bool `json:"available,omitempty"`
}

// AuthMethod is one way to log in to a provider, as served by
// GET /api/provider/{id}/auth.
type AuthMethod struct {
	Type      string       `json:"type"`
	Label     string       `json:"label"`
	Env       []string     `json:"env,omitempty"`
	Satisfied bool         `json:"satisfied,omitempty"`
	Prompts   []AuthPrompt `json:"prompts,omitempty"`
}

type AuthPrompt struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Options []string `json:"options,omitempty"`
}

// OAuthAttempt is an in-flight device or browser login.
type OAuthAttempt struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Code   string `json:"code"`
	Status string `json:"status"`
	Error  string `json:"error"`
}

type Agent struct {
	ID          string `json:"id"`
	Mode        string `json:"mode"`
	Description string `json:"description,omitempty"`
}

type Todo struct {
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
	Position int    `json:"position"`
}

func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var out []Model
	err := c.do(ctx, http.MethodGet, "/api/model", nil, &out)
	return out, err
}

func (c *Client) Providers(ctx context.Context) ([]Provider, error) {
	var out []Provider
	err := c.do(ctx, http.MethodGet, "/api/provider", nil, &out)
	return out, err
}

// AllProviders lists every catalog provider, including ones with no
// credential yet — what the connect dialog needs, as opposed to the usable
// subset the model picker shows.
func (c *Client) AllProviders(ctx context.Context) ([]Provider, error) {
	var out []Provider
	err := c.do(ctx, "GET", "/api/provider?all=true", nil, &out)
	return out, err
}

// AuthMethods lists a provider's login methods.
func (c *Client) AuthMethods(ctx context.Context, providerID string) ([]AuthMethod, error) {
	var out []AuthMethod
	err := c.do(ctx, "GET", "/api/provider/"+providerID+"/auth", nil, &out)
	return out, err
}

// SetProviderKey stores a pasted API key for a provider.
func (c *Client) SetProviderKey(ctx context.Context, providerID, key string) error {
	return c.do(ctx, "POST", "/api/provider/"+providerID+"/auth", map[string]string{"key": key}, nil)
}

// LogoutProvider removes a provider's stored credential.
func (c *Client) LogoutProvider(ctx context.Context, providerID string) error {
	return c.do(ctx, "DELETE", "/api/provider/"+providerID+"/auth", nil, nil)
}

// StartOAuth begins an OAuth login and returns the code to display.
func (c *Client) StartOAuth(ctx context.Context, providerID, method string, answers map[string]string) (*OAuthAttempt, error) {
	var out OAuthAttempt
	body := map[string]any{"method": method, "answers": answers}
	if err := c.do(ctx, "POST", "/api/provider/"+providerID+"/auth/oauth", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// OAuthStatus polls an in-flight login.
func (c *Client) OAuthStatus(ctx context.Context, attemptID string) (*OAuthAttempt, error) {
	var out OAuthAttempt
	if err := c.do(ctx, "GET", "/api/provider/auth/oauth/"+attemptID, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// LSPState is the language-server status shown in the sidebar.
type LSPState struct {
	Enabled   bool        `json:"enabled"`
	Servers   []LSPServer `json:"servers"`
	Available []string    `json:"available"`
}

type LSPServer struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Root   string `json:"root"`
	Status string `json:"status"`
}

// LSP fetches the language-server status.
func (c *Client) LSP(ctx context.Context) (*LSPState, error) {
	var out LSPState
	if err := c.do(ctx, "GET", "/api/lsp", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var out []Agent
	err := c.do(ctx, http.MethodGet, "/api/agent", nil, &out)
	return out, err
}

// MCPServer is one entry from GET /api/mcp, mirroring the {name, status,
// error?} shape TuiPluginApi.state.mcp() exposes to sidebar/footer/status
// plugins in the original — status is one of "connected", "disabled",
// "failed", "needs_auth", "needs_client_registration".
type MCPServer struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// MCPServers fetches live MCP server status, converting the server's
// name-keyed map into a name-sorted slice (state.mcp()'s shape).
func (c *Client) MCPServers(ctx context.Context) ([]MCPServer, error) {
	var raw map[string]struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/mcp", nil, &raw); err != nil {
		return nil, err
	}
	out := make([]MCPServer, 0, len(raw))
	for name, status := range raw {
		out = append(out, MCPServer{Name: name, Status: status.Status, Error: status.Error})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (c *Client) SetModel(ctx context.Context, sessionID, providerID, modelID string) error {
	return c.do(ctx, http.MethodPost, "/api/session/"+sessionID+"/model", map[string]string{
		"providerID": providerID, "id": modelID,
	}, nil)
}

func (c *Client) SetAgent(ctx context.Context, sessionID, agent string) error {
	return c.do(ctx, http.MethodPost, "/api/session/"+sessionID+"/agent", map[string]string{
		"agent": agent,
	}, nil)
}

func (c *Client) Rename(ctx context.Context, sessionID, title string) error {
	return c.do(ctx, http.MethodPost, "/api/session/"+sessionID+"/rename", map[string]string{
		"title": title,
	}, nil)
}

func (c *Client) Delete(ctx context.Context, sessionID string) error {
	return c.do(ctx, http.MethodDelete, "/api/session/"+sessionID, nil, nil)
}

func (c *Client) Todos(ctx context.Context, sessionID string) ([]Todo, error) {
	var out []Todo
	err := c.do(ctx, http.MethodGet, "/api/session/"+sessionID+"/todo", nil, &out)
	return out, err
}

type Stats struct {
	Cost             float64 `json:"cost"`
	TokensInput      int     `json:"tokensInput"`
	TokensOutput     int     `json:"tokensOutput"`
	TokensReasoning  int     `json:"tokensReasoning"`
	TokensCacheRead  int     `json:"tokensCacheRead"`
	TokensCacheWrite int     `json:"tokensCacheWrite"`
	Messages         int     `json:"messages"`
}

func (c *Client) Stats(ctx context.Context, sessionID string) (*Stats, error) {
	var out Stats
	err := c.do(ctx, http.MethodGet, "/api/session/"+sessionID+"/stats", nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Compact triggers immediate context compaction for a session.
func (c *Client) Compact(ctx context.Context, sessionID string) (bool, error) {
	var out struct {
		Compacted bool `json:"compacted"`
	}
	err := c.do(ctx, http.MethodPost, "/api/session/"+sessionID+"/compact", map[string]any{}, &out)
	return out.Compacted, err
}

// Fork copies the session up to messageID (empty = all) into a child.
func (c *Client) Fork(ctx context.Context, sessionID, messageID string) (*Session, error) {
	var out Session
	err := c.do(ctx, http.MethodPost, "/api/session/"+sessionID+"/fork", map[string]string{
		"messageID": messageID,
	}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Children lists sessions forked from the given parent.
func (c *Client) Children(ctx context.Context, sessionID string) ([]Session, error) {
	var out []Session
	err := c.do(ctx, http.MethodGet, "/api/session/"+sessionID+"/children", nil, &out)
	return out, err
}
