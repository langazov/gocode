package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxToolOutputBytes bounds what one MCP tool call may return into the
// session. MCP servers are foreign code with no output discipline of their
// own: a `describe everything` dump or a `list all` can be megabytes — a
// result that would be persisted whole into session_message and event rows,
// replayed to the model on every later turn, and re-rendered by the
// interface on every repaint, bloating the database and stalling the
// renderer for the rest of the session.
//
// Unlike the shell, which truncates its own bounded output and says so, an
// oversize MCP result is rejected as an error rather than clipped. A
// truncated half-result invites the model to answer from data it cannot know
// is cut off mid-structure — truncated JSON parses as nothing, truncated YAML
// as garbage — while an error names the problem and the fix, which the model
// can act on: narrow the call. The figure matches the shell tool's own
// maxOutputBytes: generous for real tool output, small enough that a runaway
// server cannot dominate a session.
const maxToolOutputBytes = 512 * 1024

// toolOutputTooLargeError is what an oversize result settles with. Its text
// is what the model sees as the tool result and what the interface shows on
// the failed row, so it states the size, the limit, and the fix.
type toolOutputTooLargeError struct {
	tool  string
	bytes int
}

func (e *toolOutputTooLargeError) Error() string {
	return fmt.Sprintf(
		"mcp: %s result too large: %s exceeds the %s limit. "+
			"Narrow the call — filter, limit, or paginate the result — and retry.",
		e.tool, formatBytes(e.bytes), formatBytes(maxToolOutputBytes))
}

// formatBytes renders a byte count in the largest sensible unit, the way the
// oversize error quotes sizes to the model.
func formatBytes(n int) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.1f KiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// mcpTool adapts one MCP tool from a connected server into internal/tool.Tool
// (wraps the ai-sdk dynamicTool built by convertTool() in catalog.ts).
type mcpTool struct {
	clientName string
	def        *sdkmcp.Tool
	session    *sdkmcp.ClientSession
	timeout    time.Duration
}

func (t *mcpTool) Name() string        { return ToolName(t.clientName, t.def.Name) }
func (t *mcpTool) Description() string { return t.def.Description }

func (t *mcpTool) InputSchema() map[string]any {
	if schema, ok := t.def.InputSchema.(map[string]any); ok {
		return schema
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// Execute mirrors convertTool()'s execute in catalog.ts: call the tool,
// flatten its result content to text (this port's tool.Tool interface has
// no multi-part/attachment result type, unlike the TS session message
// schema — image/binary content becomes a placeholder note instead of a
// real attachment), and turn CallToolResult.IsError into a Go error so the
// runner's normal tool-error handling applies.
func (t *mcpTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	timeout := t.timeout
	if timeout <= 0 {
		// Fallback for a tool registered with no timeout at all (e.g. a
		// test-built mcpTool): same default the registration paths use.
		timeout = defaultToolTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result, err := t.session.CallTool(callCtx, &sdkmcp.CallToolParams{
		Name:      t.def.Name,
		Arguments: input,
	})
	if err != nil {
		return "", fmt.Errorf("mcp tool %s failed: %w", t.Name(), err)
	}

	text := flattenContent(result.Content)
	if len(text) > maxToolOutputBytes {
		// IsError results carry a message too, and a multi-megabyte error
		// dump is just as unbounded as a success payload. The check runs
		// before the IsError branch so both paths get the same treatment.
		return "", &toolOutputTooLargeError{tool: t.Name(), bytes: len(text)}
	}
	if result.IsError {
		if text == "" {
			text = "MCP tool returned an error"
		}
		return "", fmt.Errorf("%s", text)
	}
	if text == "" && result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err == nil {
			if len(encoded) > maxToolOutputBytes {
				return "", &toolOutputTooLargeError{tool: t.Name(), bytes: len(encoded)}
			}
			text = string(encoded)
		}
	}
	return text, nil
}

// flattenContent mirrors the text-content-joining half of catalog.ts's
// result conversion (the image/resource-to-attachment half has no Go
// equivalent — see Execute's doc comment).
func flattenContent(content []sdkmcp.Content) string {
	var parts []string
	for _, c := range content {
		switch v := c.(type) {
		case *sdkmcp.TextContent:
			parts = append(parts, v.Text)
		case *sdkmcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image content, %s, not displayed]", orUnknown(v.MIMEType)))
		case *sdkmcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio content, %s, not displayed]", orUnknown(v.MIMEType)))
		case *sdkmcp.EmbeddedResource:
			parts = append(parts, flattenResource(v))
		default:
			parts = append(parts, "[unsupported content]")
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func flattenResource(r *sdkmcp.EmbeddedResource) string {
	if r == nil || r.Resource == nil {
		return ""
	}
	res := r.Resource
	if len(res.Blob) > 0 {
		return fmt.Sprintf("[binary resource %s, %s, not displayed]", res.URI, orUnknown(res.MIMEType))
	}
	return res.Text
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown type"
	}
	return s
}
