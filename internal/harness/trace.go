package harness

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TraceEvent is one step of a rollout, neutral of how the executor recorded
// it. cmd/gocode builds these from a session's durable messages.
type TraceEvent struct {
	// Kind is user, text, reasoning, tool or error.
	Kind   string
	Name   string // tool name
	Text   string // message text, or the error
	Input  map[string]any
	Output string
	// Failed marks a tool call that settled with an error; Output is then
	// the error text.
	Failed bool
}

// Rendering caps, from the per-benchmark defaults in harness2 (BenchSpec
// obs_trunc, max_chars) and the opencode substrate's args_trunc.
const (
	ObsTrunc  = 4_000
	ArgsTrunc = 400
	MaxTrace  = 400_000
)

const truncFmt = "…(+%d chars — render truncation)"

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\x00", "")
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf(truncFmt, len(s)-n)
}

// RenderTrace renders events into the shared trajectory grammar, porting
// render_events in harness2/substrate/opencode.py:
//
//	[USER] …   [ASSISTANT] …   [THINKING] …
//	[ACTION] tool({args})   [OBSERVATION] …   [OBSERVATION ERROR] …   [ERROR] …
//
// Each observation is clipped to ObsTrunc; the whole to MaxTrace. Both clips
// leave an explicit marker, which the improver prompts tell the model is the
// renderer's, not the solver's.
func RenderTrace(events []TraceEvent) string {
	var lines []string
	for _, ev := range events {
		switch ev.Kind {
		case "user":
			if t := strings.TrimSpace(ev.Text); t != "" {
				lines = append(lines, "[USER] "+clip(t, ObsTrunc))
			}
		case "text":
			if t := strings.TrimSpace(ev.Text); t != "" {
				lines = append(lines, "[ASSISTANT] "+clip(t, ObsTrunc))
			}
		case "reasoning":
			if t := strings.TrimSpace(ev.Text); t != "" {
				lines = append(lines, "[THINKING] "+clip(t, ObsTrunc))
			}
		case "tool":
			args := ""
			if ev.Input != nil {
				encoded, _ := json.Marshal(ev.Input)
				args = string(encoded)
			}
			lines = append(lines, fmt.Sprintf("[ACTION] %s(%s)", ev.Name, clip(args, ArgsTrunc)))
			if ev.Failed {
				lines = append(lines, "[OBSERVATION ERROR] "+clip(ev.Output, ObsTrunc))
			} else {
				lines = append(lines, "[OBSERVATION] "+clip(ev.Output, ObsTrunc))
			}
		case "error":
			lines = append(lines, "[ERROR] "+clip(ev.Text, 300))
		}
	}
	rendered := strings.TrimSpace(strings.Join(lines, "\n"))
	if rendered == "" {
		return "(no parseable events in trajectory)"
	}
	if len(rendered) > MaxTrace {
		rendered = rendered[:MaxTrace] + "\n" + fmt.Sprintf(truncFmt, len(rendered)-MaxTrace)
	}
	return rendered
}
