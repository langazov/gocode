package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

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
		timeout = defaultTimeout
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
	if result.IsError {
		if text == "" {
			text = "MCP tool returned an error"
		}
		return "", fmt.Errorf("%s", text)
	}
	if text == "" && result.StructuredContent != nil {
		encoded, err := json.Marshal(result.StructuredContent)
		if err == nil {
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
