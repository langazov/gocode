package session

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/agent"
	"github.com/langazov/gocode-go/internal/event"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/permission"
	"github.com/langazov/gocode-go/internal/plugin"
	"github.com/langazov/gocode-go/internal/tool"
)

// outageThenAnswerProvider drops the first attempt at the network layer, then
// answers every later one, recording each request it was sent.
type outageThenAnswerProvider struct {
	mu       sync.Mutex
	requests []llm.Request
}

func (p *outageThenAnswerProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	attempt := len(p.requests)
	p.mu.Unlock()
	if attempt == 1 {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "answered"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
	return nil
}

func userTexts(request llm.Request) []string {
	var texts []string
	for _, message := range request.Messages {
		if message.Role != llm.RoleUser {
			continue
		}
		for _, part := range message.Content {
			if part.Type == llm.PartText {
				texts = append(texts, part.Text)
			}
		}
	}
	return texts
}

// The regression for a network hold that merged two queued requests: the
// retry used to re-run promotion, and for queue delivery that promoted the
// *next* queued row into a turn meant to answer only the first.
func TestNetworkRetryDoesNotPromoteTheNextQueuedRequest(t *testing.T) {
	shortRetries(t, time.Minute)
	provider := &outageThenAnswerProvider{}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = provider
	for _, input := range []struct{ id, text string }{{"msg_q1", "first"}, {"msg_q2", "second"}} {
		if _, err := Admit(context.Background(), bus, runner.DB, AdmitInput{
			ID:        input.id,
			SessionID: "ses_1",
			Prompt:    Prompt{Text: input.text},
			Delivery:  DeliveryQueue,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected outage + retry + second request, got %d attempts", len(provider.requests))
	}
	retry := strings.Join(userTexts(provider.requests[1]), "|")
	if retry != "first" {
		t.Fatalf("the retried turn must carry only the first request, got %q", retry)
	}
	next := strings.Join(userTexts(provider.requests[2]), "|")
	if next != "first|second" {
		t.Fatalf("the second request runs as its own turn, got %q", next)
	}
}

// An overflow nothing can recover from used to end the turn with no trace in
// the transcript: the sentinel was returned without a step.failed, and the
// provider's message was lost.
func TestUnrecoverableOverflowSettlesTheStep(t *testing.T) {
	overflow := errors.New("prompt is too long: maximum context length exceeded")
	t.Run("no compactor", func(t *testing.T) {
		provider := &fakeProvider{turns: [][]llm.StreamEvent{
			{{Type: llm.EventProviderError, Error: overflow}},
		}}
		runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
		admitPrompt(t, bus, runner, "hello")

		err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"})
		if err == nil || !strings.Contains(err.Error(), "maximum context length") {
			t.Fatalf("expected the provider's overflow error, got %v", err)
		}
		assertSettledWith(t, runner, "maximum context length")
	})

	t.Run("still overflows after compaction", func(t *testing.T) {
		provider := &fakeProvider{turns: [][]llm.StreamEvent{
			{{Type: llm.EventProviderError, Error: overflow}},
			{{Type: llm.EventProviderError, Error: overflow}},
		}}
		runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
		runner.Compactor = &Compactor{
			Bus:      bus,
			Provider: &summaryProvider{},
			Settings: CompactionSettings{Auto: true, Buffer: 20000, KeepTokens: 0},
		}
		admitPrompt(t, bus, runner, "hello")

		err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"})
		if err == nil || !strings.Contains(err.Error(), "maximum context length") {
			t.Fatalf("expected the provider's overflow error, got %v", err)
		}
		if provider.callCount() != 2 {
			t.Fatalf("expected the overflow and exactly one retry, got %d", provider.callCount())
		}
		assertSettledWith(t, runner, "maximum context length")
	})
}

func assertSettledWith(t *testing.T, runner *Runner, message string) {
	t.Helper()
	assistant := lastAssistantData(t, runner)
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil {
		t.Fatalf("expected the failure recorded on an assistant message, got %v", assistant)
	}
	if text, _ := recorded["message"].(string); !strings.Contains(text, message) {
		t.Fatalf("recorded error = %v, want it to contain %q", recorded["message"], message)
	}
}

// stallingSummary blocks the compaction stream until its context ends.
type stallingSummary struct{ started chan struct{} }

func (p *stallingSummary) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	close(p.started)
	<-ctx.Done()
	return ctx.Err()
}

// Compaction used to run on the uncancellable publish context, so an
// interrupt during a long summary did nothing until the summary finished.
func TestInterruptStopsCompaction(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{{Type: llm.EventProviderError, Error: errors.New("maximum context length exceeded")}},
	}}
	summary := &stallingSummary{started: make(chan struct{})}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.Compactor = &Compactor{
		Bus:      bus,
		Provider: summary,
		Settings: CompactionSettings{Auto: true, Buffer: 20000, KeepTokens: 0},
	}
	admitPrompt(t, bus, runner, "hello")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, RunInput{SessionID: "ses_1"}) }()
	select {
	case <-summary.started:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupt did not stop compaction")
	}
	assistant := lastAssistantData(t, runner)
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil || recorded["type"] != ErrorTypeAborted {
		t.Fatalf("an interrupted compaction settles as interrupted, got %v", assistant["error"])
	}
}

// A provider that ignores tool_choice "none" on the last step used to have its
// tool run and the turn continue — forever, since every later step is also
// the last one.
func TestLastStepRefusesToolCalls(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "echo", Input: map[string]any{}}},
		{Type: llm.EventFinish, Finish: "tool_use"},
	}}}
	registry := tool.NewRegistry()
	echo := &fakeTool{name: "echo", output: "ran"}
	registry.Register(echo)
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.MaxSteps = 1
	admitPrompt(t, bus, runner, "do work")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(echo.inputs) != 0 {
		t.Fatalf("no tool may run on the last step, ran %d times", len(echo.inputs))
	}
	if provider.callCount() != 1 {
		t.Fatalf("the last step must not continue, got %d provider calls", provider.callCount())
	}
	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := DecodeAssistant(messages[len(messages)-1].Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range assistant.Content {
		if part.Type == "tool" && (part.State == nil || part.State.Status != ToolError) {
			t.Fatalf("the refused call must settle as an error, got %+v", part.State)
		}
	}
}

// recordingGate declines or denies by action, and records every ask that
// would have reached the user.
type recordingGate struct {
	decline string
	deny    string
	asked   []string
}

func (g *recordingGate) Assert(ctx context.Context, input ToolPermissionInput) error {
	g.asked = append(g.asked, input.Action)
	if input.Action == g.decline {
		return permission.ErrDeclined
	}
	return nil
}

func (g *recordingGate) Denied(input ToolPermissionInput) error {
	if input.Action == g.deny {
		return &permission.BlockedError{}
	}
	return nil
}

// A declined permission used to be just another tool error: the model got a
// fresh turn and could ask again, or route around the refusal.
func TestDeclinedPermissionStopsTheRequest(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "bash", Input: map[string]any{"command": "rm -rf build"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "trying another way"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	registry := tool.NewRegistry()
	bash := &fakeTool{name: "bash", output: "should not run"}
	registry.Register(bash)
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.Permissions = &recordingGate{decline: "bash"}
	admitPrompt(t, bus, runner, "clean up")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(bash.inputs) != 0 {
		t.Fatalf("a declined tool must not run, ran %d times", len(bash.inputs))
	}
	if provider.callCount() != 1 {
		t.Fatalf("a declined permission ends the request, got %d provider calls", provider.callCount())
	}
}

// scopedTool declares an extra permission, the way the shell does for paths
// outside the working directory.
type scopedTool struct{ fakeTool }

func (t *scopedTool) ExtraPermissions(input map[string]any) []tool.ExtraPermission {
	return []tool.ExtraPermission{{Action: "external_directory", Resources: []string{"/elsewhere/*"}}}
}

// A deny on a tool's own action used to be checked only after its extra
// permissions had been asked for, so the user approved a prompt for a command
// that a rule then refused anyway.
func TestDenyIsCheckedBeforeExtraPermissionsAsk(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "bash", Input: map[string]any{"command": "ls /elsewhere"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	registry := tool.NewRegistry()
	bash := &scopedTool{fakeTool{name: "bash", output: "should not run"}}
	registry.Register(bash)
	runner, bus := newRunnerFixture(t, provider, registry)
	gate := &recordingGate{deny: "bash"}
	runner.Permissions = gate
	admitPrompt(t, bus, runner, "list it")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(bash.inputs) != 0 {
		t.Fatalf("a denied tool must not run, ran %d times", len(bash.inputs))
	}
	if len(gate.asked) != 0 {
		t.Fatalf("a denied call must not ask for anything first, asked %v", gate.asked)
	}
}

// scriptedProvider plays one scripted step per request and records each
// request it was sent.
type scriptedProvider struct {
	mu       sync.Mutex
	steps    []func(emit func(llm.StreamEvent)) error
	requests []llm.Request
}

func (p *scriptedProvider) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	step := p.steps[len(p.requests)-1]
	p.mu.Unlock()
	return step(emit)
}

func answer(text string) func(func(llm.StreamEvent)) error {
	return func(emit func(llm.StreamEvent)) error {
		emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: text})
		emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
		return nil
	}
}

func callTool(id, name string) func(func(llm.StreamEvent)) error {
	return func(emit func(llm.StreamEvent)) error {
		emit(llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: id, Name: name, Input: map[string]any{}}})
		emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "tool_use"})
		return nil
	}
}

func dropLink() func(func(llm.StreamEvent)) error {
	return func(emit func(llm.StreamEvent)) error {
		err := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ENETUNREACH}
		emit(llm.StreamEvent{Type: llm.EventProviderError, Error: err})
		return err
	}
}

// funcTool runs a closure, for tools whose side effect is the test.
type funcTool struct {
	name string
	run  func(ctx context.Context) (string, error)
}

func (t *funcTool) Name() string                { return t.name }
func (t *funcTool) Description() string         { return "func tool" }
func (t *funcTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (t *funcTool) Execute(ctx context.Context, input map[string]any) (string, error) {
	return t.run(ctx)
}

// The compaction retry is the other path that used to re-run promotion: an
// overflow, a summary, and a retry that also carried the next queued request.
func TestCompactionRetryDoesNotPromoteTheNextQueuedRequest(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{{Type: llm.EventProviderError, Error: errors.New("maximum context length exceeded")}},
		{{Type: llm.EventTextDelta, Text: "first answered"}, {Type: llm.EventFinish, Finish: "end_turn"}},
		{{Type: llm.EventTextDelta, Text: "second answered"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.Compactor = &Compactor{
		Bus:      bus,
		Provider: &summaryProvider{},
		Settings: CompactionSettings{Auto: true, Buffer: 20000, KeepTokens: 0},
	}
	for _, input := range []struct{ id, text string }{{"msg_q1", "first"}, {"msg_q2", "second"}} {
		if _, err := Admit(context.Background(), bus, runner.DB, AdmitInput{
			ID:        input.id,
			SessionID: "ses_1",
			Prompt:    Prompt{Text: input.text},
			Delivery:  DeliveryQueue,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 3 {
		t.Fatalf("expected overflow + retry + second request, got %d calls", provider.callCount())
	}
	for _, text := range userTexts(provider.requests[1]) {
		if strings.Contains(text, "second") {
			t.Fatalf("the compaction retry must not carry the next queued request, got %q", userTexts(provider.requests[1]))
		}
	}
	if texts := strings.Join(userTexts(provider.requests[2]), "|"); !strings.Contains(texts, "second") {
		t.Fatalf("the second request runs as its own turn, got %q", texts)
	}
}

// A steer promoted at a step boundary resets the step count. The retry after
// a network hold used to promote nothing, so it fell back to the old count —
// and with MaxSteps 2, ran as the last step with tools disabled.
func TestRetryKeepsTheStepResetFromAPromotedSteer(t *testing.T) {
	shortRetries(t, time.Minute)
	provider := &scriptedProvider{steps: []func(func(llm.StreamEvent)) error{
		callTool("call_1", "steer"),
		dropLink(),
		answer("done"),
	}}
	registry := tool.NewRegistry()
	runner, bus := newRunnerFixture(t, nil, registry)
	runner.Provider = provider
	runner.MaxSteps = 2
	// The user steers while the first step's tool is running.
	registry.Register(&funcTool{name: "steer", run: func(ctx context.Context) (string, error) {
		_, err := Admit(context.WithoutCancel(ctx), bus, runner.DB, AdmitInput{
			ID:        "msg_steer",
			SessionID: "ses_1",
			Prompt:    Prompt{Text: "also this"},
			Delivery:  DeliverySteer,
		})
		return "steered", err
	}})
	admitPrompt(t, bus, runner, "start")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected tool step + cut-off attempt + retry, got %d requests", len(provider.requests))
	}
	for i, request := range provider.requests[1:] {
		if request.ToolChoice == "none" {
			t.Fatalf("request %d ran as the last step; the promoted steer reset the count", i+2)
		}
	}
}

// correctingGate refuses with feedback, which is guidance for the model rather
// than a decline.
type correctingGate struct{}

func (correctingGate) Assert(ctx context.Context, input ToolPermissionInput) error {
	return &permission.CorrectedError{Feedback: "use make clean instead"}
}

// The counterweight to the decline test: feedback given with a refusal is
// meant for the model, so the request continues and the model gets to act on it.
func TestRefusalWithFeedbackContinues(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "bash", Input: map[string]any{"command": "rm -rf build"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "using make clean"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	registry := tool.NewRegistry()
	bash := &fakeTool{name: "bash", output: "should not run"}
	registry.Register(bash)
	runner, bus := newRunnerFixture(t, provider, registry)
	runner.Permissions = correctingGate{}
	admitPrompt(t, bus, runner, "clean up")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(bash.inputs) != 0 {
		t.Fatalf("a refused tool must not run, ran %d times", len(bash.inputs))
	}
	if provider.callCount() != 2 {
		t.Fatalf("feedback continues the request, got %d provider calls", provider.callCount())
	}
}

// Extra permissions used to go straight to the user, skipping the
// permission.ask hook that every other ask passes through.
func TestPluginSettlesExtraPermissions(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{
		{
			{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{ID: "call_1", Name: "bash", Input: map[string]any{"command": "ls /elsewhere"}}},
			{Type: llm.EventFinish, Finish: "tool_use"},
		},
		{{Type: llm.EventTextDelta, Text: "done"}, {Type: llm.EventFinish, Finish: "end_turn"}},
	}}
	registry := tool.NewRegistry()
	bash := &scopedTool{fakeTool{name: "bash", output: "listed"}}
	registry.Register(bash)
	runner, bus := newRunnerFixture(t, provider, registry)
	// Asked directly, the user would decline the outside-directory access.
	gate := &recordingGate{decline: "external_directory"}
	runner.Permissions = gate
	runner.Plugins = hostWith(t, func(h *plugin.Hooks) {
		plugin.On(h, plugin.PermissionAsk, func(_ context.Context, in plugin.PermissionAskInput, out *plugin.PermissionAskOutput) error {
			if in.Action == "external_directory" {
				out.Status = plugin.PermissionAllow
			}
			return nil
		})
	})
	admitPrompt(t, bus, runner, "list it")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if len(bash.inputs) != 1 {
		t.Fatalf("the plugin settled the extra ask, so the tool runs; ran %d times", len(bash.inputs))
	}
	if strings.Join(gate.asked, ",") != "bash" {
		t.Fatalf("only the tool's own action reaches the user, asked %v", gate.asked)
	}
}

// countingSummary answers every compaction and counts how often it was asked.
type countingSummary struct {
	mu    sync.Mutex
	calls int
}

func (p *countingSummary) Stream(ctx context.Context, request llm.Request, emit func(llm.StreamEvent)) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	emit(llm.StreamEvent{Type: llm.EventTextDelta, Text: "## Objective\n- summary"})
	emit(llm.StreamEvent{Type: llm.EventFinish, Finish: "end_turn"})
	return nil
}

// bigSchemaTool advertises a ~100k-token schema.
type bigSchemaTool struct{ fakeTool }

func (t *bigSchemaTool) InputSchema() map[string]any {
	return map[string]any{"type": "object", "description": strings.Repeat("x", 400_000)}
}

// The proactive estimate used to count the runner's default system prompt —
// not the agent's, which replaces it — and no tool schemas at all, so a
// request could be well past the window with the check still reading it as
// small.
func TestCompactionEstimateCountsTheWholeRequest(t *testing.T) {
	newRunner := func(t *testing.T, registry *tool.Registry) (*Runner, *event.Bus, *countingSummary) {
		provider := &fakeProvider{turns: [][]llm.StreamEvent{{
			{Type: llm.EventTextDelta, Text: "ok"},
			{Type: llm.EventFinish, Finish: "end_turn"},
		}}}
		runner, bus := newRunnerFixture(t, provider, registry)
		summary := &countingSummary{}
		// A small history against an 80k budget (100k window − 20k buffer).
		runner.ContextLimit = 100_000
		runner.Compactor = &Compactor{
			Bus:      bus,
			Provider: summary,
			Settings: CompactionSettings{Auto: true, Buffer: 20_000, KeepTokens: 0},
		}
		return runner, bus, summary
	}

	t.Run("the agent's system prompt", func(t *testing.T) {
		runner, bus, summary := newRunner(t, tool.NewRegistry())
		agents := agent.NewRegistry()
		agents.Update(agent.Info{ID: "plan", Mode: "primary", System: strings.Repeat("x", 400_000)})
		runner.Agents = agents
		if _, err := runner.DB.Exec(context.Background(), `UPDATE session SET agent = 'plan' WHERE id = 'ses_1'`); err != nil {
			t.Fatal(err)
		}
		admitPrompt(t, bus, runner, "hello")
		if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
			t.Fatal(err)
		}
		if summary.calls != 1 {
			t.Fatalf("a ~100k-token agent prompt must trigger compaction, summary calls = %d", summary.calls)
		}
	})

	t.Run("the tool schemas", func(t *testing.T) {
		registry := tool.NewRegistry()
		registry.Register(&bigSchemaTool{fakeTool{name: "huge", output: "x"}})
		runner, bus, summary := newRunner(t, registry)
		admitPrompt(t, bus, runner, "hello")
		if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
			t.Fatal(err)
		}
		if summary.calls != 1 {
			t.Fatalf("~100k tokens of tool schema must trigger compaction, summary calls = %d", summary.calls)
		}
	})

	t.Run("a small request does not compact", func(t *testing.T) {
		runner, bus, summary := newRunner(t, tool.NewRegistry())
		admitPrompt(t, bus, runner, "hello")
		if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
			t.Fatal(err)
		}
		if summary.calls != 0 {
			t.Fatalf("a small request must not compact, summary calls = %d", summary.calls)
		}
	})
}

// The proactive path — compaction before the turn is built — was
// uncancellable too.
func TestInterruptStopsProactiveCompaction(t *testing.T) {
	summary := &stallingSummary{started: make(chan struct{})}
	runner, bus := newRunnerFixture(t, nil, tool.NewRegistry())
	runner.Provider = &blockingProvider{started: make(chan struct{})}
	runner.ContextLimit = 10 // any history is over budget
	runner.Compactor = &Compactor{
		Bus:      bus,
		Provider: summary,
		Settings: CompactionSettings{Auto: true, Buffer: 0, KeepTokens: 0},
	}
	admitPrompt(t, bus, runner, "hello")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx, RunInput{SessionID: "ses_1"}) }()
	select {
	case <-summary.started:
	case <-time.After(5 * time.Second):
		t.Fatal("proactive compaction never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupt did not stop proactive compaction")
	}
	assistant := lastAssistantData(t, runner)
	recorded, _ := assistant["error"].(map[string]any)
	if recorded == nil || recorded["type"] != ErrorTypeAborted {
		t.Fatalf("the interrupted turn settles as interrupted, got %v", assistant["error"])
	}
}

// Compact publishes on a detached context: a cancelled summary is a
// compaction that did not happen, never a storage error.
func TestCancelledCompactionIsNotAnError(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventTextDelta, Text: "ok"},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	admitPrompt(t, bus, runner, "hello")
	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	history, err := runner.Messages.ListForRunner(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	compactor := &Compactor{
		Bus:      bus,
		Provider: &stallingSummary{started: make(chan struct{})},
		Settings: CompactionSettings{Auto: true, KeepTokens: 0},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	compacted, err := compactor.Compact(ctx, "ses_1", history, runner.Model)
	if err != nil {
		t.Fatalf("a cancelled summary must not surface as an error: %v", err)
	}
	if compacted {
		t.Fatal("a cancelled summary must not report a compaction")
	}
}

// A tool the provider ran itself on the last step still settles from its
// output, but does not continue the turn.
func TestLastStepProviderExecutedToolDoesNotContinue(t *testing.T) {
	provider := &fakeProvider{turns: [][]llm.StreamEvent{{
		{Type: llm.EventToolCall, ToolCall: &llm.ToolCall{
			ID: "srv_1", Name: "web_search", Input: map[string]any{},
			ProviderExecuted: true, Output: "results",
		}},
		{Type: llm.EventFinish, Finish: "end_turn"},
	}}}
	runner, bus := newRunnerFixture(t, provider, tool.NewRegistry())
	runner.MaxSteps = 1
	admitPrompt(t, bus, runner, "search")

	if err := runner.Run(context.Background(), RunInput{SessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if provider.callCount() != 1 {
		t.Fatalf("the last step must not continue, got %d provider calls", provider.callCount())
	}
	messages, err := runner.Messages.List(context.Background(), "ses_1")
	if err != nil {
		t.Fatal(err)
	}
	assistant, err := DecodeAssistant(messages[len(messages)-1].Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range assistant.Content {
		if part.Type == "tool" && (part.State == nil || part.State.Status != ToolCompleted || part.State.Output != "results") {
			t.Fatalf("a provider-executed call settles from its own output, got %+v", part.State)
		}
	}
}
