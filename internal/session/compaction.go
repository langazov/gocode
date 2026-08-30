package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anomalyco/opencode-go/internal/event"
	"github.com/anomalyco/opencode-go/internal/id"
	"github.com/anomalyco/opencode-go/internal/llm"
)

// errContextOverflow signals that a provider turn failed because the context
// exceeded the model window before producing any assistant content.
var errContextOverflow = errors.New("session: context overflow")

// overflowMarkers are substrings that indicate a provider rejected a request
// for exceeding its context window.
var overflowMarkers = []string{
	"context length",
	"context_length",
	"maximum context",
	"max context",
	"too long",
	"too many tokens",
	"token limit",
	"request too large",
	"content too large",
	"prompt is too long",
	"reduce the length",
	"reduce your prompt",
	"input is too long",
	"exceeds the model",
}

// isContextOverflow reports whether an error indicates the request exceeded
// the model's context window.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range overflowMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

const (
	defaultCompactionBuffer = 20_000
	defaultKeepTokens       = 8_000
	toolOutputMaxChars      = 2_000
	summaryOutputTokens     = 4_096
)

// SummaryTemplate mirrors the TypeScript compaction summary template.
const SummaryTemplate = `Output exactly the Markdown structure shown inside <template> and keep the section order unchanged. Do not include the <template> tags in your response.
<template>
## Objective
- [one or two brief sentences describing what the user is trying to accomplish]

## Important Details
- [constraints/preferences, decisions and why, important facts/assumptions, exact context needed to continue, or "(none)"]

## Work State
### Completed
- [finished work, verified facts, or changes made; otherwise "(none)"]

### Active
- [current work, partial changes, or investigation state; otherwise "(none)"]

### Blocked
- [blockers, failing commands, or unknowns; otherwise "(none)"]

## Next Move
1. [immediate concrete action, or "(none)"]
2. [next action if known, or "(none)"]

## Relevant Files
- [file or directory path: why it matters, or "(none)"]
</template>

Rules:
- Keep every section, even when empty.
- Use terse bullets, not prose paragraphs.
- Preserve exact file paths, symbols, commands, error strings, URLs, and identifiers when known.
- Do not mention the summary process or that context was compacted.`

type CompactionSettings struct {
	Auto       bool
	Buffer     int
	KeepTokens int
}

func DefaultCompactionSettings() CompactionSettings {
	return CompactionSettings{Auto: true, Buffer: defaultCompactionBuffer, KeepTokens: defaultKeepTokens}
}

// Compactor summarizes older history when a session approaches its context
// limit, porting the core of packages/core/src/session/compaction.ts.
type Compactor struct {
	Bus      *event.Bus
	Provider llm.StreamClient
	Settings CompactionSettings
}

// NeedsCompaction reports whether the projected history is close enough to the
// context limit to warrant compaction.
func (c *Compactor) NeedsCompaction(history []StoredMessage, contextLimit, requestTokens int) bool {
	if !c.Settings.Auto {
		return false
	}
	if contextLimit <= 0 {
		return false
	}
	return requestTokens > contextLimit-c.Settings.Buffer
}

// Compact summarizes the older history and projects a compaction message. It
// returns false when there is nothing to compact.
func (c *Compactor) Compact(ctx context.Context, sessionID string, history []StoredMessage, model ModelRef) (bool, error) {
	head, recent, ok := selectHistory(history, c.Settings.KeepTokens)
	if !ok {
		return false, nil
	}
	previousSummary, _ := latestCompaction(history)
	if head == "" && previousSummary == "" {
		return false, nil
	}
	prompt := buildCompactionPrompt(previousSummary, head)

	messageID, err := id.Ascending(id.KindMessage)
	if err != nil {
		return false, err
	}
	if _, err := c.Bus.Publish(ctx, CompactionStarted, map[string]any{
		"sessionID": sessionID,
		"timestamp": nowMillis(),
		"messageID": messageID,
		"reason":    "auto",
	}, event.PublishOptions{}); err != nil {
		return false, err
	}

	var summary strings.Builder
	var failed bool
	streamErr := c.Provider.Stream(ctx, llm.Request{
		ProviderID: model.ProviderID,
		ModelID:    model.ID,
		MaxTokens:  summaryOutputTokens,
		Messages:   []llm.Message{llm.UserText("", prompt)},
	}, func(streamEvent llm.StreamEvent) {
		switch streamEvent.Type {
		case llm.EventTextDelta:
			summary.WriteString(streamEvent.Text)
		case llm.EventProviderError:
			failed = true
		}
	})
	if streamErr != nil {
		failed = true
	}
	text := strings.TrimSpace(summary.String())
	if failed || text == "" {
		return false, nil
	}

	if _, err := c.Bus.Publish(ctx, CompactionEnded, map[string]any{
		"sessionID": sessionID,
		"timestamp": nowMillis(),
		"messageID": messageID,
		"reason":    "auto",
		"text":      text,
		"recent":    recent,
	}, event.PublishOptions{}); err != nil {
		return false, err
	}
	return true, nil
}

// latestCompaction returns the summary of the most recent compaction message.
func latestCompaction(history []StoredMessage) (string, string) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Type != "compaction" {
			continue
		}
		var data struct {
			Summary string `json:"summary"`
			Recent  string `json:"recent"`
		}
		if err := json.Unmarshal(history[i].Data, &data); err != nil {
			return "", ""
		}
		return data.Summary, data.Recent
	}
	return "", ""
}

// selectHistory splits serializable history into an older head (to summarize)
// and a recent tail (to keep verbatim), keeping roughly keepTokens of recent
// context.
func selectHistory(history []StoredMessage, keepTokens int) (string, string, bool) {
	var parts []string
	for _, message := range history {
		if message.Type == "compaction" {
			continue
		}
		if text := serializeMessage(message); text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "", "", false
	}
	total := 0
	split := len(parts)
	for index := len(parts) - 1; index >= 0; index-- {
		next := total + estimateTokens(parts[index])
		if next > keepTokens {
			break
		}
		total = next
		split = index
	}
	return strings.Join(parts[:split], "\n\n"), strings.Join(parts[split:], "\n\n"), true
}

func buildCompactionPrompt(previousSummary, head string) string {
	conversation := fmt.Sprintf("Here is the conversation so far:\n\n<conversation>\n%s\n</conversation>", head)
	if previousSummary == "" {
		return strings.Join([]string{
			conversation,
			"Create a new anchored summary from the conversation history in the <conversation> tags above so another coding agent can continue the work.",
			SummaryTemplate,
		}, "\n\n")
	}
	return strings.Join([]string{
		conversation,
		fmt.Sprintf("Here is the summary of the conversation before the <conversation> above:\n\n<prior-summary>\n%s\n</prior-summary>", previousSummary),
		"Construct a new summary that combines the prior summary and the conversation. Carry forward objectives, constraints, decisions, and work state; the conversation wins where they conflict.",
		SummaryTemplate,
	}, "\n\n")
}

func serializeMessage(message StoredMessage) string {
	var data map[string]any
	if err := json.Unmarshal(message.Data, &data); err != nil {
		return ""
	}
	switch message.Type {
	case "user":
		text, _ := data["text"].(string)
		if text == "" {
			return ""
		}
		return "[User]: " + text
	case "system":
		text, _ := data["text"].(string)
		if text == "" {
			return ""
		}
		return "[System update]: " + text
	case "synthetic":
		text, _ := data["text"].(string)
		if text == "" {
			return ""
		}
		return "[Synthetic context]: " + text
	case "shell":
		command, _ := data["command"].(string)
		output, _ := data["output"].(string)
		return fmt.Sprintf("[Shell]: %s\n%s", command, truncateToolOutput(output))
	case "assistant":
		return serializeAssistant(data)
	}
	return ""
}

func serializeAssistant(data map[string]any) string {
	content, ok := data["content"].([]any)
	if !ok {
		return ""
	}
	var lines []string
	for _, rawPart := range content {
		part, ok := rawPart.(map[string]any)
		if !ok {
			continue
		}
		partType, _ := part["type"].(string)
		switch partType {
		case "text":
			text, _ := part["text"].(string)
			if text != "" {
				lines = append(lines, "[Assistant]: "+text)
			}
		case "reasoning":
			text, _ := part["text"].(string)
			if text != "" {
				lines = append(lines, "[Assistant reasoning]: "+text)
			}
		case "tool":
			name, _ := part["name"].(string)
			state, _ := part["state"].(map[string]any)
			input := ""
			if state != nil {
				if inputString, ok := state["input"].(string); ok {
					input = inputString
				} else if encoded, err := json.Marshal(state["input"]); err == nil {
					input = string(encoded)
				}
			}
			lines = append(lines, fmt.Sprintf("[Assistant tool call]: %s(%s)", name, input))
			if state != nil {
				status, _ := state["status"].(string)
				if status == ToolCompleted {
					output, _ := state["output"].(string)
					lines = append(lines, "[Tool result]: "+truncateToolOutput(output))
				}
				if status == ToolError {
					errText, _ := state["error"].(string)
					lines = append(lines, "[Tool error]: "+errText)
				}
			}
		}
	}
	return strings.Join(lines, "\n")
}

func truncateToolOutput(value string) string {
	if len(value) <= toolOutputMaxChars {
		return value
	}
	return value[:toolOutputMaxChars] + "\n[truncated]"
}
