package mcp

import "regexp"

var sanitizeRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitize mirrors catalog.ts's sanitize(): non [a-zA-Z0-9_-] characters
// become underscores.
func sanitize(s string) string {
	return sanitizeRe.ReplaceAllString(s, "_")
}

// ToolName mirrors catalog.ts's toolName(): a single underscore joins the
// sanitized client and tool names — not "mcp__server__tool".
func ToolName(clientName, name string) string {
	return sanitize(clientName) + "_" + sanitize(name)
}

// resourceKey mirrors catalog.ts's client-scoped key for prompts/resources:
// "<sanitized client>:<sanitized item name>".
func resourceKey(clientName, name string) string {
	return sanitize(clientName) + ":" + sanitize(name)
}
