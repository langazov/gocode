package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// hooks.json is gocode's tool-loop lever. In opencode the harness component
// is plugin/<name>.ts — code that runs inside the tool loop, the one thing a
// model cannot skip. A linked Go binary cannot load TypeScript, and letting an
// improver write executable code that runs on every tool call is a security
// surface this port declines, so the lever is declarative instead:
//
//	{"rules": [
//	  {"when": "before", "tool": "write", "match": "\\.env\"", "action": "deny",
//	   "message": "never write .env files; put settings in config.example"},
//	  {"when": "after", "tool": "bash", "match": "FAILED", "action": "append",
//	   "message": "a test failed: read the failure before editing again"}
//	]}
//
// A before rule matches the JSON-encoded call arguments and refuses the call
// with its message. An after rule matches the tool output and appends its
// message to what the model sees. tool is a tool name or "*"; an empty match
// matches every call.

// HookRule is one declarative rule.
type HookRule struct {
	When    string `json:"when"`
	Tool    string `json:"tool"`
	Match   string `json:"match,omitempty"`
	Action  string `json:"action"`
	Message string `json:"message"`

	rx *regexp.Regexp
}

// Hooks is a parsed hooks.json.
type Hooks struct {
	Rules []HookRule `json:"rules"`
}

// ParseHooks parses and validates hooks.json, naming every problem so an
// editor can fix them in one retry: a malformed rule would otherwise never
// fire, and nobody would see why.
func ParseHooks(src string) (*Hooks, error) {
	var hooks Hooks
	if err := json.Unmarshal([]byte(src), &hooks); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %v", FileHooks, err)
	}
	if len(hooks.Rules) == 0 {
		return nil, fmt.Errorf(`%s needs a top-level {"rules": [...]} list with at least one rule`, FileHooks)
	}
	var errs []string
	for i := range hooks.Rules {
		rule := &hooks.Rules[i]
		at := fmt.Sprintf("%s: rules[%d]", FileHooks, i)
		switch rule.When {
		case "before":
			if rule.Action != "deny" {
				errs = append(errs, at+`: a "before" rule's action must be "deny"`)
			}
		case "after":
			if rule.Action != "append" {
				errs = append(errs, at+`: an "after" rule's action must be "append"`)
			}
		default:
			errs = append(errs, at+`: "when" must be "before" or "after"`)
		}
		if strings.TrimSpace(rule.Tool) == "" {
			errs = append(errs, at+`: "tool" must name a tool, or "*" for every tool`)
		}
		if strings.TrimSpace(rule.Message) == "" {
			errs = append(errs, at+`: "message" is empty`)
		}
		if rule.Match != "" {
			rx, err := regexp.Compile(rule.Match)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: match is not a valid regular expression: %v", at, err))
			}
			rule.rx = rx
		}
	}
	if len(errs) > 0 {
		return nil, errors.New(strings.Join(errs, " | "))
	}
	return &hooks, nil
}

func (r *HookRule) applies(tool, subject string) bool {
	if r.Tool != "*" && r.Tool != tool {
		return false
	}
	return r.rx == nil || r.rx.MatchString(subject)
}

// Before refuses a call a "before" rule matches. It satisfies the Before half
// of session.ToolGuard.
func (h *Hooks) Before(tool string, args map[string]any) error {
	if h == nil {
		return nil
	}
	encoded, _ := json.Marshal(args)
	for i := range h.Rules {
		rule := &h.Rules[i]
		if rule.When == "before" && rule.applies(tool, string(encoded)) {
			return fmt.Errorf("blocked by harness rule: %s", rule.Message)
		}
	}
	return nil
}

// After appends every matching "after" rule's message to the output.
func (h *Hooks) After(tool string, _ map[string]any, output string) string {
	if h == nil {
		return output
	}
	for i := range h.Rules {
		rule := &h.Rules[i]
		if rule.When == "after" && rule.applies(tool, output) {
			output += "\n\n[harness] " + rule.Message
		}
	}
	return output
}
