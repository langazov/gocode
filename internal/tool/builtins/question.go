package builtins

import (
	"context"
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/question"
	"github.com/langazov/gocode-go/internal/tool"
)

// Asker is the seam the question tool needs. internal/question does not
// import internal/tool, so this could depend on the service directly; keeping
// it an interface makes the tool testable without a live service.
type Asker interface {
	Ask(ctx context.Context, input question.AskInput) ([]question.Answer, error)
}

// QuestionTool asks the user something mid-run and blocks until answered.
//
// Blocking is safe: the runner dispatches every tool on its own goroutine, so
// a parked question does not stall the stream, sibling tools, or subagents.
type QuestionTool struct {
	asker Asker
}

func NewQuestionTool(asker Asker) *QuestionTool {
	return &QuestionTool{asker: asker}
}

func (t *QuestionTool) Name() string { return "question" }

func (t *QuestionTool) Description() string {
	return strings.Join([]string{
		"Use this tool when you need to ask the user questions during execution. This allows you to:",
		"1. Gather user preferences or requirements",
		"2. Clarify ambiguous instructions",
		"3. Get decisions on implementation choices as you work",
		"4. Offer choices to the user about what direction to take.",
		"",
		"Usage notes:",
		"- A \"Type your own answer\" option is added automatically; don't include \"Other\" or catch-all options",
		"- Answers are returned as arrays of labels; set `multiple: true` to allow selecting more than one",
		"- If you recommend a specific option, make that the first option in the list and add \"(Recommended)\" at the end of the label",
	}, "\n")
}

func (t *QuestionTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"questions": map[string]any{
				"type":        "array",
				"description": "Questions to ask",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"question": map[string]any{"type": "string", "description": "Complete question"},
						"header":   map[string]any{"type": "string", "description": "Very short label (max 30 chars)"},
						"multiple": map[string]any{"type": "boolean", "description": "Allow selecting multiple choices"},
						"options": map[string]any{
							"type":        "array",
							"description": "Available choices",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"label":       map[string]any{"type": "string", "description": "Display text (1-5 words, concise)"},
									"description": map[string]any{"type": "string", "description": "Explanation of choice"},
								},
								"required": []string{"label", "description"},
							},
						},
					},
					"required": []string{"question", "header", "options"},
				},
			},
		},
		"required": []string{"questions"},
	}
}

func (t *QuestionTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	return t.ExecuteWithContext(ctx, input, tool.ExecContext{})
}

func (t *QuestionTool) ExecuteWithContext(ctx context.Context, input map[string]any, exec tool.ExecContext) (string, error) {
	prompts, err := decodePrompts(input)
	if err != nil {
		return "", err
	}
	if t.asker == nil {
		return "", fmt.Errorf("question: no question service configured")
	}

	var source *question.Source
	if exec.CallID != "" {
		source = &question.Source{MessageID: exec.AssistantMessageID, CallID: exec.CallID}
	}
	answers, err := t.asker.Ask(ctx, question.AskInput{
		SessionID: exec.SessionID,
		Questions: prompts,
		Source:    source,
	})
	if err != nil {
		return "", err
	}

	parts := make([]string, 0, len(prompts))
	for i, prompt := range prompts {
		answer := "Unanswered"
		if i < len(answers) && len(answers[i]) > 0 {
			answer = strings.Join(answers[i], ", ")
		}
		parts = append(parts, fmt.Sprintf("%q=%q", prompt.Question, answer))
	}
	return "User has answered your questions: " + strings.Join(parts, ", ") +
		". You can now continue with the user's answers in mind.", nil
}

func decodePrompts(input map[string]any) ([]question.Prompt, error) {
	raw, ok := input["questions"].([]any)
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("question: questions must be a non-empty array")
	}
	prompts := make([]question.Prompt, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("question: each question must be an object")
		}
		prompt := question.Prompt{
			Question: stringArg(entry, "question"),
			Header:   stringArg(entry, "header"),
			Multiple: boolArg(entry, "multiple"),
		}
		if prompt.Question == "" {
			return nil, fmt.Errorf("question: question text is required")
		}
		for _, rawOption := range asSlice(entry["options"]) {
			option, ok := rawOption.(map[string]any)
			if !ok {
				continue
			}
			prompt.Options = append(prompt.Options, question.Option{
				Label:       stringArg(option, "label"),
				Description: stringArg(option, "description"),
			})
		}
		prompts = append(prompts, prompt)
	}
	return prompts, nil
}

func asSlice(value any) []any {
	out, _ := value.([]any)
	return out
}
