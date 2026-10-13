package tui

import (
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

// The question tool's label: the header when the model gave one, the
// question text otherwise, a count when a round carries several, and a
// placeholder while the input has yet to land.
func TestQuestionToolLabel(t *testing.T) {
	cases := []struct {
		name  string
		input map[string]any
		label string
	}{
		{
			"header",
			map[string]any{"questions": []any{map[string]any{
				"question": "Which database?",
				"header":   "Database",
				"options":  []any{},
			}}},
			"Database",
		},
		{
			"question text without header",
			map[string]any{"questions": []any{map[string]any{
				"question": "Which database?",
				"options":  []any{},
			}}},
			"Which database?",
		},
		{
			"several questions",
			map[string]any{"questions": []any{
				map[string]any{"question": "First?", "header": "One"},
				map[string]any{"question": "Second?", "header": "Two"},
			}},
			"Asked 2 questions",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			icon, label := toolLabel("question", tc.input, nil)
			if icon != "?" {
				t.Errorf("icon = %q, want ?", icon)
			}
			if label != tc.label {
				t.Errorf("label = %q, want %q", label, tc.label)
			}
		})
	}
	if _, label := toolLabel("question", map[string]any{}, nil); label != "Asking you..." {
		t.Errorf("label without input = %q, want Asking you...", label)
	}
}

// parseQuestionAnswers decodes the exact format the tool emits — one pair
// per question — and refuses anything else rather than guessing.
func TestParseQuestionAnswers(t *testing.T) {
	got := parseQuestionAnswers(
		"The user answered your questions:\n" +
			"\n1. Which database?\n   Answer: Postgres\n" +
			"\n2. Run migrations now?\n   Answer: Yes, defer them\n" +
			"\nContinue with the user's answers in mind.")
	if len(got) != 2 {
		t.Fatalf("pairs = %+v, want 2", got)
	}
	if got[0].Question != "Which database?" || got[0].Answer != "Postgres" {
		t.Errorf("pair 0 = %+v", got[0])
	}
	if got[1].Question != "Run migrations now?" || got[1].Answer != "Yes, defer them" {
		t.Errorf("pair 1 = %+v", got[1])
	}
}

func TestParseQuestionAnswersRejectsForeignOutput(t *testing.T) {
	for name, output := range map[string]string{
		"legacy flat line": `User has answered your questions: "Which database?"="Postgres".`,
		"empty":            "",
		"interrupted":      "The user answered your questions:\n\n1. Which database?\n",
	} {
		t.Run(name, func(t *testing.T) {
			if pairs := parseQuestionAnswers(output); pairs != nil {
				t.Fatalf("pairs = %+v, want nil", pairs)
			}
		})
	}
}

// A settled question round renders as a Q/A panel: every question and its
// answer on their own rows, never one long wrapped line.
func TestQuestionBlockRendersAnswerPairs(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark")}
	state := &toolState{
		Status: "done",
		Input: map[string]any{"questions": []any{map[string]any{
			"question": "Which database?",
			"header":   "Database",
		}}},
		Output: "The user answered your questions:\n\n1. Which database?\n   Answer: Postgres\n\nContinue with the user's answers in mind.",
	}
	got := plain(first(app.toolRow(client.Message{}, "t1", "question", state)))
	for _, want := range []string{"# Questions", "Which database?", "Postgres"} {
		if !strings.Contains(got, want) {
			t.Errorf("block missing %q:\n%s", want, got)
		}
	}
}

// Before the round settles — or when the output is not the answer format —
// the row falls back to the one-line label, the way every other tool row
// without a block does.
func TestQuestionRowFallsBackToLabelWhilePending(t *testing.T) {
	app := &App{width: 100, height: 30, theme: themeResolve("gocode-dark")}
	state := &toolState{
		Status: "running",
		Input: map[string]any{"questions": []any{map[string]any{
			"question": "Which database?",
			"header":   "Database",
		}}},
	}
	got := plain(first(app.toolRow(client.Message{}, "t1", "question", state)))
	if !strings.Contains(got, "Database") {
		t.Fatalf("pending row should show the label, got %q", got)
	}
	if strings.Contains(got, "# Questions") {
		t.Fatalf("pending row must not render an answers block, got %q", got)
	}
}
