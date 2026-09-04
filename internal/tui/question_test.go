package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/langazov/gocode-go/internal/tui/client"
)

func planQuestion() client.QuestionRequest {
	return client.QuestionRequest{
		ID:        "que_1",
		SessionID: "ses_1",
		Questions: []client.QuestionPrompt{{
			Question: "Would you like to switch to the plan agent and design this before implementing?",
			Header:   "Plan Agent",
			Options: []client.QuestionOption{
				{Label: "Yes", Description: "Switch to the plan agent and design the change first"},
				{Label: "No", Description: "Stay with the current agent and continue implementing"},
			},
		}},
	}
}

// bannerKey names the keys the ask banners handle, which are the named ones
// (enter/esc/arrows) that keys_test.go's rune-based helper cannot express.
func bannerKey(name string) tea.KeyMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	return tea.KeyPressMsg{Code: []rune(name)[0], Text: name}
}

// TestQuestionBlocksUntilAnswered is the regression for the reported hang:
// plan_enter parks the turn on a question, and before this the interface had
// no way to see it or answer it, so the tool call sat pending forever.
func TestQuestionAnswerReachesTheServer(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	app.applyQuestions([]client.QuestionRequest{planQuestion()})
	if app.question == nil {
		t.Fatal("the pending question was not adopted")
	}

	view := app.View()
	if !strings.Contains(view, "Plan Agent") {
		t.Fatalf("the banner does not name the question, got %q", view)
	}
	if !strings.Contains(view, "Yes") || !strings.Contains(view, "No") {
		t.Fatal("the banner does not offer the options")
	}

	driveCmd(t, app, app.handleKey(bannerKey("enter")))

	if len(api.answered) != 1 {
		t.Fatalf("expected one reply, got %d", len(api.answered))
	}
	if got := api.answered[0]; len(got) != 1 || len(got[0]) != 1 || got[0][0] != "Yes" {
		t.Fatalf("answers = %v, want [[Yes]]", got)
	}
	if app.question != nil {
		t.Fatal("the banner should clear once answered")
	}
}

func TestQuestionArrowSelectsTheOtherOption(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.applyQuestions([]client.QuestionRequest{planQuestion()})

	driveCmd(t, app, app.handleKey(bannerKey("right")))
	driveCmd(t, app, app.handleKey(bannerKey("enter")))

	if got := api.answered; len(got) != 1 || got[0][0][0] != "No" {
		t.Fatalf("answers = %v, want [[No]]", got)
	}
}

// Escape has to reject rather than merely dismiss: the tool is parked on a
// channel, so a banner that just disappeared would leave the turn hung with
// no way back.
func TestQuestionEscapeRejects(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.applyQuestions([]client.QuestionRequest{planQuestion()})

	driveCmd(t, app, app.handleKey(bannerKey("esc")))

	if got := api.rejected; len(got) != 1 || got[0] != "que_1" {
		t.Fatalf("rejected = %v, want [que_1]", got)
	}
	if len(api.answered) != 0 {
		t.Fatal("escape must not send an answer")
	}
	if app.question != nil {
		t.Fatal("the banner should clear on reject")
	}
}

// A request may carry several prompts; they are answered in order and replied
// to as one set, which is the shape the server's [][]string expects.
func TestQuestionAnswersEveryPromptBeforeReplying(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.applyQuestions([]client.QuestionRequest{{
		ID: "que_2", SessionID: "ses_1",
		Questions: []client.QuestionPrompt{
			{Question: "First?", Header: "One", Options: []client.QuestionOption{{Label: "A"}, {Label: "B"}}},
			{Question: "Second?", Header: "Two", Options: []client.QuestionOption{{Label: "C"}, {Label: "D"}}},
		},
	}})

	driveCmd(t, app, app.handleKey(bannerKey("enter")))
	if len(api.answered) != 0 {
		t.Fatal("the reply must wait for every prompt")
	}
	if !strings.Contains(app.View(), "Second?") {
		t.Fatal("the banner did not advance to the second prompt")
	}
	driveCmd(t, app, app.handleKey(bannerKey("right")))
	driveCmd(t, app, app.handleKey(bannerKey("enter")))

	if len(api.answered) != 1 {
		t.Fatalf("expected one reply, got %d", len(api.answered))
	}
	if got := api.answered[0]; len(got) != 2 || got[0][0] != "A" || got[1][0] != "D" {
		t.Fatalf("answers = %v, want [[A] [D]]", got)
	}
}

func TestQuestionMultiSelectTogglesWithSpace(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.applyQuestions([]client.QuestionRequest{{
		ID: "que_3", SessionID: "ses_1",
		Questions: []client.QuestionPrompt{{
			Question: "Which?", Header: "Pick", Multiple: true,
			Options: []client.QuestionOption{{Label: "A"}, {Label: "B"}, {Label: "C"}},
		}},
	}})

	driveCmd(t, app, app.handleKey(bannerKey("space"))) // tick A
	driveCmd(t, app, app.handleKey(bannerKey("right"))) // -> B
	driveCmd(t, app, app.handleKey(bannerKey("right"))) // -> C
	driveCmd(t, app, app.handleKey(bannerKey("space"))) // tick C
	driveCmd(t, app, app.handleKey(bannerKey("enter")))

	if len(api.answered) != 1 {
		t.Fatalf("expected one reply, got %d", len(api.answered))
	}
	if got := api.answered[0][0]; len(got) != 2 || got[0] != "A" || got[1] != "C" {
		t.Fatalf("answers = %v, want [A C]", got)
	}
}

// With nothing ticked, enter falls back to the highlighted option rather than
// replying with an empty answer the tool cannot act on.
func TestQuestionMultiSelectEnterWithNothingTicked(t *testing.T) {
	api, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.applyQuestions([]client.QuestionRequest{{
		ID: "que_4", SessionID: "ses_1",
		Questions: []client.QuestionPrompt{{
			Question: "Which?", Header: "Pick", Multiple: true,
			Options: []client.QuestionOption{{Label: "A"}, {Label: "B"}},
		}},
	}})

	driveCmd(t, app, app.handleKey(bannerKey("enter")))

	if got := api.answered; len(got) != 1 || len(got[0][0]) != 1 || got[0][0][0] != "A" {
		t.Fatalf("answers = %v, want [[A]]", got)
	}
}

// A refetch on the reconciliation tick must not reset a partly answered round.
func TestQuestionRefetchKeepsProgress(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	request := client.QuestionRequest{
		ID: "que_5", SessionID: "ses_1",
		Questions: []client.QuestionPrompt{
			{Question: "First?", Header: "One", Options: []client.QuestionOption{{Label: "A"}, {Label: "B"}}},
			{Question: "Second?", Header: "Two", Options: []client.QuestionOption{{Label: "C"}, {Label: "D"}}},
		},
	}
	app.applyQuestions([]client.QuestionRequest{request})
	driveCmd(t, app, app.handleKey(bannerKey("enter")))

	app.applyQuestions([]client.QuestionRequest{request})

	if app.questionIndex != 1 {
		t.Fatalf("questionIndex = %d, want 1", app.questionIndex)
	}
	if len(app.questionAnswers) != 1 {
		t.Fatalf("the first answer was lost: %v", app.questionAnswers)
	}
}

// Answered elsewhere (another client, or the run interrupted): the banner has
// to retract, or it invites an answer for a request that no longer exists.
func TestQuestionClearsWhenNoLongerPending(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.applyQuestions([]client.QuestionRequest{planQuestion()})

	app.applyQuestions(nil)

	if app.question != nil {
		t.Fatal("the banner should clear when nothing is pending")
	}
}

func TestQuestionIgnoresOtherSessions(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	other := planQuestion()
	other.SessionID = "ses_child"

	app.applyQuestions([]client.QuestionRequest{other})

	if app.question != nil {
		t.Fatal("a subagent's question must not take over the parent's banner")
	}
}

// An outstanding permission is the older ask (it is raised before the tool
// runs), so it keeps the banner and the keys until it is settled.
func TestPermissionTakesPrecedenceOverQuestion(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)
	app.applyPermissions([]client.PermissionRequest{{
		ID: "per_1", SessionID: "ses_1", Action: "bash", Resources: []string{"ls"},
	}})
	app.applyQuestions([]client.QuestionRequest{planQuestion()})

	if !strings.Contains(app.View(), "Permission required") {
		t.Fatal("the permission banner should hold the slot")
	}
}

// The ask events are the only signal a parked turn produces: no steps, no
// text, nothing. Without the refetch they trigger, the prompt waits out the
// 10s reconciliation tick.
func TestAskEventTriggersRefetch(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	state := newTree()
	if !state.apply(client.Event{
		Type:    "session.next.question.asked",
		Session: "ses_1",
		Data:    map[string]any{"requestID": "que_1"},
	}) {
		t.Fatal("an ask must register as a change")
	}
	effect := app.applySnapshot(state.snapshot(0))

	if !effect.asks {
		t.Fatal("the snapshot should ask for a pending-request refetch")
	}
	// Only once per ask: a later snapshot with no new ask must not re-poll.
	if again := app.applySnapshot(state.snapshot(0)); again.asks {
		t.Fatal("an unchanged ask count should not refetch again")
	}
}

func TestPermissionAskEventAlsoTriggersRefetch(t *testing.T) {
	_, server := newMockAPI(t)
	app := newTestApp(t, server.URL)
	openSession(t, app)

	state := newTree()
	state.apply(client.Event{
		Type:    "session.next.permission.asked",
		Session: "ses_1",
		Data:    map[string]any{"requestID": "per_1"},
	})

	if !app.applySnapshot(state.snapshot(0)).asks {
		t.Fatal("a permission ask should refetch too")
	}
}
