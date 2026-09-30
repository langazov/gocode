package acp

import (
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

func TestV1InitializeAdvertisesTheV1Surface(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	var result map[string]any
	f.client.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}, &result)
	caps, _ := result["agentCapabilities"].(map[string]any)
	if caps["loadSession"] != true {
		t.Fatalf("loadSession not advertised: %v", caps)
	}
	sessionCaps, _ := caps["sessionCapabilities"].(map[string]any)
	for _, key := range []string{"list", "resume", "close", "delete", "additionalDirectories"} {
		if _, ok := sessionCaps[key]; !ok {
			t.Errorf("sessionCapabilities.%s missing", key)
		}
	}
	if _, ok := result["agentInfo"]; !ok {
		t.Error("agentInfo missing")
	}
	if _, ok := result["info"]; ok {
		t.Error("v1 response must not carry the v2 info field")
	}
}

func TestVersionNegotiationFallsBackToLatest(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	var result map[string]any
	f.client.call("initialize", map[string]any{"protocolVersion": 99}, &result)
	if result["protocolVersion"] != float64(LatestProtocol) {
		t.Fatalf("an unsupported version must be answered with our latest, got %v", result["protocolVersion"])
	}
}

func TestV1NewSessionReturnsModesAndConfigOptions(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	var result map[string]any
	f.client.call("session/new", map[string]any{"cwd": f.dir, "mcpServers": []any{}}, &result)
	modes, _ := result["modes"].(map[string]any)
	if modes["currentModeId"] != "build" {
		t.Fatalf("modes = %v", modes)
	}
	available, _ := modes["availableModes"].([]any)
	if len(available) != 2 {
		t.Fatalf("subagents must not be offered as modes: %v", available)
	}
	options, _ := result["configOptions"].([]any)
	ids := []string{}
	for _, option := range options {
		entry := option.(map[string]any)
		ids = append(ids, entry["id"].(string))
		if _, v2 := entry["configId"]; v2 {
			t.Error("v1 config options use id, not configId")
		}
	}
	if strings.Join(ids, ",") != "mode,model,thought_level" {
		t.Fatalf("config option order = %v", ids)
	}
	f.client.waitFor("available commands", isUpdate("available_commands_update"))
}

func TestV1NewSessionRejectsRelativeCwd(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	err := f.client.try("session/new", map[string]any{"cwd": "relative/dir", "mcpServers": []any{}}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("want invalid params, got %v", err)
	}
}

func TestV1PromptStreamsTextAndEndsTurn(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("Hello there")}}, fixtureOptions{})
	sessionID := f.newSession()
	var result map[string]any
	f.client.call("session/prompt", textPrompt(sessionID, "say hello"), &result)
	if result["stopReason"] != "end_turn" {
		t.Fatalf("stopReason = %v", result["stopReason"])
	}
	chunk := f.client.waitFor("agent message chunk", isUpdate("agent_message_chunk"))
	content, _ := chunk["content"].(map[string]any)
	if content["text"] != "Hello there" || chunk["messageId"] == "" {
		t.Fatalf("chunk = %v", chunk)
	}
	// The response is sent after every update of the turn: nothing about
	// this turn may arrive once the client has the stop reason.
	usage := f.client.waitFor("usage", isUpdate("usage_update"))
	if usage["size"] != float64(128000) || usage["used"] != float64(120) {
		t.Fatalf("usage = %v", usage)
	}
	f.client.waitFor("title", isUpdate("session_info_update"))
}

func TestV1ToolCallWithPermission(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_1", "bash", map[string]any{"command": "echo hi"}),
		textTurn("done"),
	}}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{rules: permission.Ruleset{
		{Action: "bash", Resource: "*", Effect: permission.Ask},
	}})
	sessionID := f.newSession()
	var result map[string]any
	f.client.call("session/prompt", textPrompt(sessionID, "run it"), &result)
	if result["stopReason"] != "end_turn" {
		t.Fatalf("stopReason = %v", result["stopReason"])
	}
	request := f.client.waitRequest("session/request_permission")
	toolCall, _ := request["toolCall"].(map[string]any)
	if toolCall["toolCallId"] != "call_1" {
		t.Fatalf("permission not tied to the call: %v", request)
	}
	options, _ := request["options"].([]any)
	if len(options) < 2 {
		t.Fatalf("options = %v", options)
	}

	created := f.client.waitFor("tool_call", isUpdate("tool_call"))
	if created["kind"] != "execute" || created["status"] != "pending" || created["title"] != "echo hi" {
		t.Fatalf("tool_call = %v", created)
	}
	f.client.waitFor("in_progress", func(u map[string]any) bool {
		return u["sessionUpdate"] == "tool_call_update" && u["status"] == "in_progress"
	})
	done := f.client.waitFor("completed", func(u map[string]any) bool {
		return u["sessionUpdate"] == "tool_call_update" && u["status"] == "completed"
	})
	raw, _ := done["rawOutput"].(map[string]any)
	if !strings.Contains(raw["output"].(string), "hi") {
		t.Fatalf("completed update = %v", done)
	}
}

func TestV1PermissionRejectFailsTheCall(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_1", "bash", map[string]any{"command": "echo hi"}),
		textTurn("ok, not running it"),
	}}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{rules: permission.Ruleset{
		{Action: "bash", Resource: "*", Effect: permission.Ask},
	}})
	f.client.permission = func(map[string]any) map[string]any {
		return map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": "reject"}}
	}
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "run it"), nil)
	f.client.waitFor("failed", func(u map[string]any) bool {
		return u["sessionUpdate"] == "tool_call_update" && u["status"] == "failed"
	})
}

func TestV1UnknownPermissionOutcomeIsNotApproval(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_1", "bash", map[string]any{"command": "echo hi"}),
		textTurn("ok"),
	}}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{rules: permission.Ruleset{
		{Action: "bash", Resource: "*", Effect: permission.Ask},
	}})
	f.client.permission = func(map[string]any) map[string]any {
		return map[string]any{"outcome": map[string]any{"outcome": "_something_new"}}
	}
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "run it"), nil)
	f.client.waitFor("failed", func(u map[string]any) bool {
		return u["sessionUpdate"] == "tool_call_update" && u["status"] == "failed"
	})
}

func TestV1EditReportsDiff(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	target := filepath.Join(f.dir, "notes.txt")
	os.WriteFile(target, []byte("alpha\nbeta\n"), 0o644)
	f.provider.turns = [][]llm.StreamEvent{
		toolTurn("call_edit", "edit", map[string]any{"path": "notes.txt", "oldString": "beta", "newString": "gamma"}),
		textTurn("edited"),
	}
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "edit it"), nil)
	done := f.client.waitFor("completed edit", func(u map[string]any) bool {
		return u["sessionUpdate"] == "tool_call_update" && u["status"] == "completed"
	})
	content, _ := done["content"].([]any)
	var diff map[string]any
	for _, item := range content {
		if entry := item.(map[string]any); entry["type"] == "diff" {
			diff = entry
		}
	}
	if diff == nil || diff["path"] != target || diff["oldText"] != "alpha\nbeta\n" || diff["newText"] != "alpha\ngamma\n" {
		t.Fatalf("diff = %v", diff)
	}
	created := f.client.waitFor("tool_call", isUpdate("tool_call"))
	locations, _ := created["locations"].([]any)
	if len(locations) != 1 || locations[0].(map[string]any)["path"] != target {
		t.Fatalf("locations must be absolute: %v", created["locations"])
	}
}

func TestV1CancelEndsTurnWithCancelled(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{nil}, started: make(chan struct{}, 1)}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{})
	sessionID := f.newSession()
	done := make(chan map[string]any, 1)
	go func() {
		var result map[string]any
		f.client.call("session/prompt", textPrompt(sessionID, "think forever"), &result)
		done <- result
	}()
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("turn never reached the provider")
	}
	f.client.conn.Notify("session/cancel", map[string]any{"sessionId": sessionID})
	select {
	case result := <-done:
		if result["stopReason"] != "cancelled" {
			t.Fatalf("stopReason = %v", result["stopReason"])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled prompt never answered")
	}
}

func TestV1LoadReplaysHistory(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("Paris.")}}, fixtureOptions{})
	sessionID := f.newSession()
	var prompted map[string]any
	f.client.call("session/prompt", textPrompt(sessionID, "capital of France?"), &prompted)
	f.client.reset()

	var result map[string]any
	f.client.call("session/load", map[string]any{"sessionId": sessionID, "cwd": f.dir, "mcpServers": []any{}}, &result)
	updates := f.client.snapshot()
	got := kinds(updates)
	if len(got) < 2 || got[0] != "user_message_chunk" || got[1] != "agent_message_chunk" {
		t.Fatalf("replay order = %v", got)
	}
	meta, _ := prompted["_meta"].(map[string]any)
	userMessageID := meta["gocode"].(map[string]any)["userMessageId"]
	if updates[0]["messageId"] != userMessageID {
		t.Fatalf("replayed user message id %v, prompt returned %v", updates[0]["messageId"], userMessageID)
	}
	if _, ok := result["modes"]; !ok {
		t.Error("session/load response should carry the mode state")
	}
}

func TestV1ResumeDoesNotReplay(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("hi")}}, fixtureOptions{})
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "hello"), nil)
	time.Sleep(50 * time.Millisecond)
	f.client.reset()
	f.client.call("session/resume", map[string]any{"sessionId": sessionID, "cwd": f.dir, "mcpServers": []any{}}, nil)
	for _, kind := range kinds(f.client.snapshot()) {
		if strings.HasSuffix(kind, "_chunk") {
			t.Fatalf("resume must not replay history, got %s", kind)
		}
	}
}

func TestSessionListFiltersAndPaginates(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	for i := 0; i < sessionPageSize+3; i++ {
		f.newSession()
	}
	var page map[string]any
	f.client.call("session/list", map[string]any{"cwd": f.dir}, &page)
	sessions, _ := page["sessions"].([]any)
	if len(sessions) != sessionPageSize || page["nextCursor"] == nil {
		t.Fatalf("first page: %d sessions, cursor %v", len(sessions), page["nextCursor"])
	}
	var next map[string]any
	f.client.call("session/list", map[string]any{"cwd": f.dir, "cursor": page["nextCursor"]}, &next)
	if rest, _ := next["sessions"].([]any); len(rest) != 3 || next["nextCursor"] != nil {
		t.Fatalf("second page: %v", next)
	}
	var other map[string]any
	f.client.call("session/list", map[string]any{"cwd": "/nowhere/else"}, &other)
	if rest, _ := other["sessions"].([]any); len(rest) != 0 {
		t.Fatalf("cwd filter ignored: %v", other)
	}
	err := f.client.try("session/list", map[string]any{"cursor": "garbage!"}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("invalid cursor must be refused, got %v", err)
	}
}

func TestSessionCloseAndDelete(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	sessionID := f.newSession()
	f.client.call("session/close", map[string]any{"sessionId": sessionID}, nil)
	err := f.client.try("session/prompt", textPrompt(sessionID, "hi"), nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != codeResourceNotFound {
		t.Fatalf("a closed session must be gone, got %v", err)
	}
	f.client.call("session/delete", map[string]any{"sessionId": sessionID}, nil)
	f.client.call("session/delete", map[string]any{"sessionId": "ses_never_existed"}, nil)
	var page map[string]any
	f.client.call("session/list", map[string]any{}, &page)
	if sessions, _ := page["sessions"].([]any); len(sessions) != 0 {
		t.Fatalf("deleted session still listed: %v", sessions)
	}
}

func TestV1SetModeAndConfigOption(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	sessionID := f.newSession()
	f.client.call("session/set_mode", map[string]any{"sessionId": sessionID, "modeId": "plan"}, nil)

	var result map[string]any
	f.client.call("session/set_config_option", map[string]any{"sessionId": sessionID, "configId": "model", "value": "faketest/other-model"}, &result)
	options, _ := result["configOptions"].([]any)
	values := map[string]any{}
	for _, option := range options {
		entry := option.(map[string]any)
		values[entry["id"].(string)] = entry["currentValue"]
	}
	if values["mode"] != "plan" || values["model"] != "faketest/other-model" {
		t.Fatalf("config state = %v", values)
	}
	if _, ok := values["thought_level"]; ok {
		t.Fatal("a model without variants must not offer a thought level")
	}
	err := f.client.try("session/set_mode", map[string]any{"sessionId": sessionID, "modeId": "explore"}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("a subagent is not a mode, got %v", err)
	}
}

func TestV1ClientFileSystemDelegation(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_read", "read", map[string]any{"path": "buffer.txt"}),
		toolTurn("call_write", "write", map[string]any{"path": "out.txt", "content": "written"}),
		textTurn("done"),
	}}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{clientCaps: map[string]any{
		"fs": map[string]any{"readTextFile": true, "writeTextFile": true},
	}})
	os.WriteFile(filepath.Join(f.dir, "buffer.txt"), []byte("on disk"), 0o644)
	written := map[string]string{}
	f.client.handle("fs/read_text_file", func(params map[string]any) (any, error) {
		return map[string]any{"content": "unsaved editor buffer"}, nil
	})
	f.client.handle("fs/write_text_file", func(params map[string]any) (any, error) {
		written[params["path"].(string)] = params["content"].(string)
		return map[string]any{}, nil
	})
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "read then write"), nil)

	read := f.client.waitFor("read result", func(u map[string]any) bool {
		return u["toolCallId"] == "call_read" && u["status"] == "completed"
	})
	if raw, _ := read["rawOutput"].(map[string]any); !strings.Contains(raw["output"].(string), "unsaved editor buffer") {
		t.Fatalf("read did not go through the client: %v", read)
	}
	if written[filepath.Join(f.dir, "out.txt")] != "written" {
		t.Fatalf("write did not go through the client: %v", written)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "out.txt")); err == nil {
		t.Fatal("a client-served write must not also land on disk")
	}
}

func TestV1ClientTerminalDelegation(t *testing.T) {
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_sh", "bash", map[string]any{"command": "make test"}),
		textTurn("done"),
	}}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{clientCaps: map[string]any{"terminal": true}})
	f.client.handle("terminal/create", func(params map[string]any) (any, error) {
		return map[string]any{"terminalId": "term_x"}, nil
	})
	f.client.handle("terminal/wait_for_exit", func(map[string]any) (any, error) {
		return map[string]any{"exitCode": 0, "signal": nil}, nil
	})
	f.client.handle("terminal/output", func(map[string]any) (any, error) {
		return map[string]any{"output": "all tests passed\n", "truncated": false, "exitStatus": map[string]any{"exitCode": 0}}, nil
	})
	released := make(chan struct{}, 1)
	f.client.handle("terminal/release", func(map[string]any) (any, error) {
		released <- struct{}{}
		return map[string]any{}, nil
	})
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "run tests"), nil)

	create := f.client.waitRequest("terminal/create")
	args, _ := create["args"].([]any)
	if len(args) != 2 || args[1] != "make test" || create["cwd"] != f.dir {
		t.Fatalf("terminal/create = %v", create)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal never released")
	}
	done := f.client.waitFor("completed", func(u map[string]any) bool {
		return u["toolCallId"] == "call_sh" && u["status"] == "completed"
	})
	content, _ := done["content"].([]any)
	if len(content) == 0 || content[0].(map[string]any)["terminalId"] != "term_x" {
		t.Fatalf("the embedded terminal must stay in the settled call: %v", done)
	}
}

func TestV1SlashCommandExpands(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{turns: [][]llm.StreamEvent{textTurn("ok")}}, fixtureOptions{})
	sessionID := f.newSession()
	f.client.call("session/prompt", textPrompt(sessionID, "/init"), nil)
	f.provider.mu.Lock()
	defer f.provider.mu.Unlock()
	messages := f.provider.requests[0].Messages
	text := messages[len(messages)-1].Content[0].Text
	if strings.HasPrefix(text, "/init") || !strings.Contains(text, "AGENTS.md") {
		t.Fatalf("/init was not expanded to its template: %q", text)
	}
}

func TestUnknownExtensionMethodIsMethodNotFound(t *testing.T) {
	f := newFixture(t, ProtocolV1, &scriptedProvider{}, fixtureOptions{})
	err := f.client.try("_vendor/whatever", map[string]any{}, nil)
	var rpcErr *jsonrpc.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeMethodNotFound {
		t.Fatalf("want method not found, got %v", err)
	}
	// Unknown notifications are ignored, and the connection keeps working.
	f.client.conn.Notify("_vendor/ping", map[string]any{})
	f.client.call("session/list", map[string]any{}, nil)
}

func TestAdditionalDirectoriesWidenTheSandbox(t *testing.T) {
	extra := t.TempDir()
	os.WriteFile(filepath.Join(extra, "shared.txt"), []byte("shared lib"), 0o644)
	provider := &scriptedProvider{turns: [][]llm.StreamEvent{
		toolTurn("call_read", "read", map[string]any{"path": filepath.Join(extra, "shared.txt")}),
		textTurn("done"),
	}}
	f := newFixture(t, ProtocolV1, provider, fixtureOptions{})
	var result map[string]any
	f.client.call("session/new", map[string]any{"cwd": f.dir, "additionalDirectories": []any{extra}, "mcpServers": []any{}}, &result)
	sessionID := result["sessionId"].(string)
	f.client.call("session/prompt", textPrompt(sessionID, "read the shared file"), nil)
	read := f.client.waitFor("read", func(u map[string]any) bool {
		return u["toolCallId"] == "call_read" && (u["status"] == "completed" || u["status"] == "failed")
	})
	if read["status"] != "completed" {
		t.Fatalf("a file in an additional root must be readable: %v", read)
	}
}
