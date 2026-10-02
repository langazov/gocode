package acp

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/langazov/gocode-go/internal/jsonrpc"
	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/permission"
)

func TestV2InitializeAdvertisesTheV2Surface(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{}, fixtureOptions{})
	var result map[string]any
	f.client.call("initialize", map[string]any{"protocolVersion": 2, "capabilities": map[string]any{}, "info": map[string]any{"name": "c"}}, &result)
	if _, ok := result["info"].(map[string]any); !ok {
		t.Fatal("v2 info is required")
	}
	caps, _ := result["capabilities"].(map[string]any)
	sessionCaps, _ := caps["session"].(map[string]any)
	prompt, _ := sessionCaps["prompt"].(map[string]any)
	if _, ok := prompt["image"].(map[string]any); !ok {
		t.Fatalf("support markers are objects in v2: %v", prompt)
	}
	mcp, _ := sessionCaps["mcp"].(map[string]any)
	if _, ok := mcp["sse"]; ok {
		t.Error("sse was removed in v2")
	}
	for _, removed := range []string{"agentCapabilities", "agentInfo"} {
		if _, ok := result[removed]; ok {
			t.Errorf("v2 response carries v1 field %s", removed)
		}
	}
	methods, _ := result["authMethods"].([]any)
	first, _ := methods[0].(map[string]any)
	if first["methodId"] == nil || first["type"] != "agent" {
		t.Fatalf("v2 auth method shape: %v", first)
	}
}

func TestV2RemovedMethodsAreNotFound(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{}, fixtureOptions{})
	sessionID := f.newSession()
	for _, method := range []string{"session/load", "session/set_mode", "authenticate", "logout"} {
		err := f.client.try(method, map[string]any{"sessionId": sessionID, "cwd": f.dir}, nil)
		var rpcErr *jsonrpc.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeMethodNotFound {
			t.Errorf("%s in v2: want method not found, got %v", method, err)
		}
	}
}

func TestV2PromptLifecycle(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("Hello")}}, fixtureOptions{})
	sessionID := f.newSession()
	var result map[string]any
	f.client.call("session/prompt", textPrompt(sessionID, "hi"), &result)
	messageID, _ := result["messageId"].(string)
	if messageID == "" {
		t.Fatalf("v2 prompt response must carry the inserted messageId: %v", result)
	}
	if _, ok := result["stopReason"]; ok {
		t.Fatal("v2 prompt response must not end the turn")
	}
	idle := f.client.waitFor("idle", func(u map[string]any) bool {
		return u["sessionUpdate"] == "state_update" && u["state"] == "idle"
	})
	if idle["stopReason"] != "end_turn" {
		t.Fatalf("idle = %v", idle)
	}

	updates := f.client.snapshot()
	order := kinds(updates)
	index := func(match func(map[string]any) bool) int {
		for i, update := range updates {
			if match(update) {
				return i
			}
		}
		return -1
	}
	user := index(func(u map[string]any) bool { return u["sessionUpdate"] == "user_message" })
	running := index(func(u map[string]any) bool { return u["sessionUpdate"] == "state_update" && u["state"] == "running" })
	chunk := index(isUpdate("agent_message_chunk"))
	idleAt := index(func(u map[string]any) bool { return u["sessionUpdate"] == "state_update" && u["state"] == "idle" })
	if user < 0 || running < 0 || chunk < 0 || !(user < running && running < chunk && chunk < idleAt) {
		t.Fatalf("lifecycle order = %v", order)
	}
	if updates[user]["messageId"] != messageID {
		t.Fatalf("user_message id %v != response id %v", updates[user]["messageId"], messageID)
	}
	if updates[chunk]["messageId"] == nil || updates[chunk]["messageId"] == "" {
		t.Fatal("v2 chunks require a messageId")
	}
}

func TestV2ToolCallsAreUpsertsOnly(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_1", "bash", map[string]any{"command": "echo hi"}),
		textTurn("done"),
	}}
	f := newFixture(t, ProtocolV2, provider, fixtureOptions{rules: permission.Ruleset{
		{Action: "bash", Resource: "*", Effect: permission.Ask},
	}})
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "run"), nil)
	f.client.waitFor("idle", func(u map[string]any) bool { return u["sessionUpdate"] == "state_update" && u["state"] == "idle" })

	for _, kind := range kinds(f.client.snapshot()) {
		if kind == "tool_call" || kind == "plan" || kind == "current_mode_update" {
			t.Fatalf("v1-only update %q sent on a v2 connection", kind)
		}
	}
	first := f.client.waitFor("tool creation", func(u map[string]any) bool {
		return u["sessionUpdate"] == "tool_call_update" && u["toolCallId"] == "call_1"
	})
	if first["title"] == nil || first["status"] != "pending" {
		t.Fatalf("the first update creates the call and should carry its title: %v", first)
	}
	f.client.waitFor("requires_action", func(u map[string]any) bool {
		return u["sessionUpdate"] == "state_update" && u["state"] == "requires_action"
	})

	request := f.client.waitRequest("session/request_permission")
	if request["title"] == nil || request["toolCall"] != nil {
		t.Fatalf("v2 permission requests carry a title and a subject, not a bare toolCall: %v", request)
	}
	subject, _ := request["subject"].(map[string]any)
	if subject["type"] != "command" || subject["command"] != "echo hi" || subject["cwd"] != f.dir {
		t.Fatalf("bash asks use the command subject: %v", subject)
	}

	// The display terminal: created, then fed base64 output, then exited.
	terminal := f.client.waitFor("terminal", isUpdate("terminal_update"))
	if terminal["command"] != "echo hi" {
		t.Fatalf("terminal_update = %v", terminal)
	}
	chunk := f.client.waitFor("terminal output", isUpdate("terminal_output_chunk"))
	decoded, err := base64.StdEncoding.DecodeString(chunk["data"].(string))
	if err != nil || !strings.Contains(string(decoded), "hi") {
		t.Fatalf("terminal chunk = %v (%v)", chunk, err)
	}
	f.client.waitFor("exit", func(u map[string]any) bool {
		return u["sessionUpdate"] == "terminal_update" && u["exitStatus"] != nil
	})
}

func TestV2StructuredDiff(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{}, fixtureOptions{})
	target := filepath.Join(f.dir, "new.txt")
	f.provider.turns = [][]llm.StreamEvent{
		toolTurn("call_w", "write", map[string]any{"path": "new.txt", "content": "fresh\n"}),
		textTurn("done"),
	}
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "write"), nil)
	done := f.client.waitFor("completed", func(u map[string]any) bool {
		return u["toolCallId"] == "call_w" && u["status"] == "completed"
	})
	var diff map[string]any
	for _, item := range done["content"].([]any) {
		if entry := item.(map[string]any); entry["type"] == "diff" {
			diff = entry
		}
	}
	if diff == nil {
		t.Fatalf("no diff in %v", done)
	}
	changes, _ := diff["changes"].([]any)
	change, _ := changes[0].(map[string]any)
	if change["operation"] != "add" || change["path"] != target {
		t.Fatalf("changes = %v", changes)
	}
	patch, _ := diff["patch"].(map[string]any)
	text, _ := patch["text"].(string)
	if patch["format"] != "git_patch" || !strings.HasPrefix(text, "diff --git "+target) || !strings.Contains(text, "--- /dev/null") || !strings.Contains(text, "+fresh") {
		t.Fatalf("patch = %q", text)
	}
	if _, ok := diff["oldText"]; ok {
		t.Fatal("v2 diffs have no oldText")
	}
}

func TestV2PlanUpdate(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_todo", "todowrite", map[string]any{"todos": []any{
			map[string]any{"content": "write tests", "status": "in_progress", "priority": "high"},
			map[string]any{"content": "ship", "status": "pending", "priority": "low"},
		}}),
		textTurn("planned"),
	}}
	f := newFixture(t, ProtocolV2, provider, fixtureOptions{})
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "plan it"), nil)
	update := f.client.waitFor("plan", isUpdate("plan_update"))
	plan, _ := update["plan"].(map[string]any)
	entries, _ := plan["entries"].([]any)
	if plan["type"] != "items" || plan["planId"] != planID || len(entries) != 2 {
		t.Fatalf("plan_update = %v", update)
	}
}

func TestV2CancelReportsIdleCancelled(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{nil}, started: make(chan struct{}, 1)}
	f := newFixture(t, ProtocolV2, provider, fixtureOptions{})
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "forever"), nil)
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("turn never started")
	}
	f.client.conn.Notify("session/cancel", map[string]any{"sessionId": sessionID})
	idle := f.client.waitFor("idle", func(u map[string]any) bool {
		return u["sessionUpdate"] == "state_update" && u["state"] == "idle"
	})
	if idle["stopReason"] != "cancelled" {
		t.Fatalf("idle = %v", idle)
	}
}

func TestV2ResumeReplayFromStart(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("Paris.")}}, fixtureOptions{})
	sessionID := f.newSession()
	var prompted map[string]any
	f.client.call("session/prompt", textPrompt(sessionID, "capital of France?"), &prompted)
	f.client.waitFor("idle", func(u map[string]any) bool { return u["sessionUpdate"] == "state_update" && u["state"] == "idle" })

	f.client.reset()
	f.client.call("session/resume", map[string]any{"sessionId": sessionID, "cwd": f.dir}, nil)
	if len(f.client.snapshot()) != 0 {
		t.Fatalf("resume without replayFrom must not replay: %v", kinds(f.client.snapshot()))
	}

	var result map[string]any
	f.client.call("session/resume", map[string]any{"sessionId": sessionID, "cwd": f.dir, "replayFrom": map[string]any{"type": "start"}}, &result)
	updates := f.client.snapshot()
	got := kinds(updates)
	if len(got) < 2 || got[0] != "user_message" || got[1] != "agent_message" {
		t.Fatalf("replay = %v", got)
	}
	if updates[0]["messageId"] != prompted["messageId"] {
		t.Fatalf("replayed user message must reuse the prompt's id: %v vs %v", updates[0]["messageId"], prompted["messageId"])
	}
	content, _ := updates[1]["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["text"] != "Paris." {
		t.Fatalf("whole-message replay content = %v", updates[1])
	}

	err := f.client.try("session/resume", map[string]any{"sessionId": sessionID, "cwd": f.dir, "replayFrom": map[string]any{"type": "_middle"}}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("unknown replay cursor must be refused, got %v", err)
	}
}

func TestV2ResumeRejectsAnotherDirectory(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{}, fixtureOptions{})
	sessionID := f.newSession()
	err := f.client.try("session/resume", map[string]any{"sessionId": sessionID, "cwd": t.TempDir()}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("resuming a session in another directory must be refused, got %v", err)
	}
}

func TestV2ConfigOptionsUseConfigIDAndTypedValues(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{}, fixtureOptions{clientCaps: map[string]any{
		"session": map[string]any{"configOptions": map[string]any{"boolean": map[string]any{}}},
	}})
	var created map[string]any
	f.client.call("session/new", map[string]any{"cwd": f.dir}, &created)
	sessionID := created["sessionId"].(string)
	if _, ok := created["modes"]; ok {
		t.Fatal("v2 setup responses carry no modes")
	}
	commands, _ := created["availableCommands"].([]any)
	if len(commands) == 0 {
		t.Fatal("v2 setup responses may carry the initial commands; this agent sends them")
	}
	for _, command := range commands {
		if input, ok := command.(map[string]any)["input"].(map[string]any); ok && input["type"] != "text" {
			t.Fatalf("v2 command input needs its type discriminator: %v", command)
		}
	}
	var auto map[string]any
	for _, option := range created["configOptions"].([]any) {
		entry := option.(map[string]any)
		if entry["configId"] == nil {
			t.Fatalf("v2 config options are keyed by configId: %v", entry)
		}
		if entry["configId"] == configAutoApprove {
			auto = entry
		}
	}
	if auto == nil || auto["type"] != "boolean" {
		t.Fatal("the boolean option is offered to a client that advertised boolean support")
	}

	err := f.client.try("session/set_config_option", map[string]any{"sessionId": sessionID, "configId": "mode", "value": "plan"}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("v2 set_config_option requires a type, got %v", err)
	}
	var result map[string]any
	f.client.call("session/set_config_option", map[string]any{"sessionId": sessionID, "configId": "thought_level", "type": "id", "value": "high"}, &result)
	f.client.call("session/set_config_option", map[string]any{"sessionId": sessionID, "configId": configAutoApprove, "type": "boolean", "value": true}, &result)
	values := map[string]any{}
	for _, option := range result["configOptions"].([]any) {
		entry := option.(map[string]any)
		values[entry["configId"].(string)] = entry["currentValue"]
	}
	if values["thought_level"] != "high" || values[configAutoApprove] != true {
		t.Fatalf("config state = %v", values)
	}
}

func TestV2AutoApproveAnswersAsksWithoutTheClient(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_1", "bash", map[string]any{"command": "echo hi"}),
		textTurn("done"),
	}}
	f := newFixture(t, ProtocolV2, provider, fixtureOptions{
		rules:      permission.Ruleset{{Action: "bash", Resource: "*", Effect: permission.Ask}},
		clientCaps: map[string]any{"session": map[string]any{"configOptions": map[string]any{"boolean": map[string]any{}}}},
	})
	sessionID := f.newSession()
	f.client.call("session/set_config_option", map[string]any{"sessionId": sessionID, "configId": configAutoApprove, "type": "boolean", "value": true}, nil)
	f.client.call("session/prompt", textPrompt(sessionID, "run"), nil)
	f.client.waitFor("completed", func(u map[string]any) bool {
		return u["toolCallId"] == "call_1" && u["status"] == "completed"
	})
	f.client.mu.Lock()
	defer f.client.mu.Unlock()
	for _, request := range f.client.requests {
		if request.method == "session/request_permission" {
			t.Fatal("auto-approve must not prompt the client")
		}
	}
}

func TestV2AuthLoginAndLogout(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{}, fixtureOptions{})
	f.client.call("auth/login", map[string]any{"methodId": authMethodID}, nil)
	f.client.call("auth/logout", map[string]any{}, nil)
	err := f.client.try("auth/login", map[string]any{"methodId": "nope"}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("unknown method id: %v", err)
	}
}

func TestV2MCPConfigRequiresTypeAndRejectsSSE(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{}, fixtureOptions{})
	for _, server := range []map[string]any{
		{"name": "untyped", "command": "/bin/true"},
		{"type": "sse", "name": "old", "url": "https://example.com/sse"},
	} {
		err := f.client.try("session/new", map[string]any{"cwd": f.dir, "mcpServers": []any{server}}, nil)
		var rpcErr *jsonrpc.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
			t.Errorf("%v: want invalid params, got %v", server, err)
		}
	}
}

func TestQuestionsUseFormElicitation(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_q", "question", map[string]any{"questions": []any{map[string]any{
			"question": "Which approach?",
			"header":   "Approach",
			"options": []any{
				map[string]any{"label": "Conservative"},
				map[string]any{"label": "Aggressive"},
			},
		}}}),
		textTurn("going aggressive"),
	}}
	f := newFixture(t, ProtocolV2, provider, fixtureOptions{clientCaps: map[string]any{
		"elicitation": map[string]any{"form": map[string]any{}},
	}})
	f.client.handle("elicitation/create", func(params map[string]any) (any, error) {
		return map[string]any{"action": "accept", "content": map[string]any{"q1": "Aggressive"}}, nil
	})
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "decide"), nil)
	request := f.client.waitRequest("elicitation/create")
	if request["mode"] != "form" || request["sessionId"] != sessionID {
		t.Fatalf("elicitation = %v", request)
	}
	schema, _ := request["requestedSchema"].(map[string]any)
	properties, _ := schema["properties"].(map[string]any)
	q1, _ := properties["q1"].(map[string]any)
	if enum, _ := q1["enum"].([]any); len(enum) != 2 {
		t.Fatalf("schema = %v", schema)
	}
	done := f.client.waitFor("question answered", func(u map[string]any) bool {
		return u["toolCallId"] == "call_q" && (u["status"] == "completed" || u["status"] == "failed")
	})
	raw, _ := done["rawOutput"].(map[string]any)
	if done["status"] != "completed" || !strings.Contains(raw["output"].(string), "Aggressive") {
		t.Fatalf("question result = %v", done)
	}
}

func TestQuestionsFallBackToPermissionOptions(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_q", "question", map[string]any{"questions": []any{map[string]any{
			"question": "Which approach?",
			"header":   "Approach",
			"options":  []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}},
		}}}),
		textTurn("ok"),
	}}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{})
	f.client.permission = func(params map[string]any) map[string]any {
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "choice-1"}}
	}
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "decide"), nil)
	done := f.client.waitFor("question answered", func(u map[string]any) bool {
		return u["toolCallId"] == "call_q" && u["status"] == "completed"
	})
	if raw, _ := done["rawOutput"].(map[string]any); !strings.Contains(raw["output"].(string), "B") {
		t.Fatalf("answer not delivered: %v", done)
	}
}

func TestPromptContentConversion(t *testing.T) {
	f := newFixture(t, ProtocolV2, &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("ok")}}, fixtureOptions{})
	sessionID := f.newSession()
	source := filepath.Join(f.dir, "main.go")
	os.WriteFile(source, []byte("package main"), 0o644)
	f.client.call("session/prompt", map[string]any{"sessionId": sessionID, "prompt": []any{
		map[string]any{"type": "text", "text": "Review"},
		map[string]any{"type": "resource_link", "uri": "file://" + source, "name": "main.go"},
		map[string]any{"type": "resource", "resource": map[string]any{"uri": "file://" + source, "text": "package main // unsaved"}},
		map[string]any{"type": "image", "mimeType": "image/png", "data": "iVBORw0KGgo="},
	}}, nil)
	f.client.waitFor("idle", func(u map[string]any) bool { return u["sessionUpdate"] == "state_update" && u["state"] == "idle" })
	f.provider.mu.Lock()
	messages := f.provider.requests[0].Messages
	f.provider.mu.Unlock()
	last := messages[len(messages)-1]
	var text string
	images := 0
	for _, part := range last.Content {
		switch part.Type {
		case llm.PartImage:
			images++
		default:
			text += part.Text
		}
	}
	if !strings.Contains(text, "Review") || !strings.Contains(text, "@main.go") || !strings.Contains(text, "package main // unsaved") {
		t.Fatalf("prompt text = %q", text)
	}
	if images != 1 {
		t.Fatalf("image attachment not forwarded (%d images)", images)
	}

	err := f.client.try("session/prompt", map[string]any{"sessionId": sessionID, "prompt": []any{
		map[string]any{"type": "audio", "mimeType": "audio/wav", "data": "UklGRg=="},
	}}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("audio is not advertised and must be refused, got %v", err)
	}
}

func TestRequestCancellation(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{nil}, started: make(chan struct{}, 1)}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{})
	sessionID := f.newSession()
	done := make(chan map[string]any, 1)
	go func() {
		var result map[string]any
		f.client.call("session/prompt", textPrompt(sessionID, "forever"), &result)
		done <- result
	}()
	<-provider.started
	// The prompt is the client's third request on this connection:
	// initialize (1), session/new (2), session/prompt (3).
	f.client.conn.Notify("$/cancel_request", map[string]any{"requestId": 3})
	select {
	case result := <-done:
		if result["stopReason"] != "cancelled" {
			t.Fatalf("stopReason = %v", result["stopReason"])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("$/cancel_request did not end the prompt")
	}
}
