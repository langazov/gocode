package session

import (
	"context"

	"github.com/langazov/gocode-go/internal/llm"
	"github.com/langazov/gocode-go/internal/plugin"
)

// The runner's plugin seams, porting the `plugin.trigger` call sites in
// packages/opencode/src/session/llm/request.ts and the tool execution path.
//
// Each helper is a no-op when no plugin registered the hook, so a build with
// no plugins loaded runs exactly the code it ran before this file existed.
// Hook failures are already reported through the host's error sink, and the
// output keeps every successful hook's edits, so these deliberately proceed
// with what they have rather than failing the turn — see [plugin.Trigger].

// applyToolDefinitions runs the tool.definition hook over the tools being
// advertised, letting a plugin retitle or reshape one before the model sees
// it. It edits the slice in place.
func (r *Runner) applyToolDefinitions(ctx context.Context, tools []llm.ToolDefinition) {
	if r.Plugins == nil || !r.Plugins.Registered(plugin.ToolDefinition.Name()) {
		return
	}
	for i, definition := range tools {
		out := plugin.ToolDefinitionOutput{
			Description: definition.Description,
			Parameters:  definition.InputSchema,
		}
		_ = plugin.Trigger(ctx, r.Plugins, plugin.ToolDefinition,
			plugin.ToolDefinitionInput{ToolID: definition.Name}, &out)
		tools[i].Description = out.Description
		if out.Parameters != nil {
			tools[i].InputSchema = out.Parameters
		}
	}
}

// applySystemTransform runs the system prompt hook, porting
// `experimental.chat.system.transform`. The hook owns the whole ordered list,
// so a plugin can prepend, append, or replace blocks.
func (r *Runner) applySystemTransform(ctx context.Context, sessionID string, resolved resolvedAgent, system []string) []string {
	if r.Plugins == nil || !r.Plugins.Registered(plugin.SystemTransform.Name()) {
		return system
	}
	out := plugin.SystemTransformOutput{System: system}
	_ = plugin.Trigger(ctx, r.Plugins, plugin.SystemTransform, plugin.SystemTransformInput{
		SessionID: sessionID,
		Model:     modelInfo(resolved),
	}, &out)
	return out.System
}

// applyChatParams runs the chat.params hook over an assembled request,
// porting the trigger in request.ts. The hook is handed the values the runner
// computed, so a plugin sees what it is overriding.
func (r *Runner) applyChatParams(ctx context.Context, sessionID string, resolved resolvedAgent, request *llm.Request) {
	if r.Plugins == nil || !r.Plugins.Registered(plugin.ChatParams.Name()) {
		return
	}
	out := plugin.ChatParamsOutput{
		Temperature:     request.Temperature,
		TopP:            request.TopP,
		MaxOutputTokens: plugin.Int(request.MaxTokens),
		Options:         request.Reasoning,
	}
	_ = plugin.Trigger(ctx, r.Plugins, plugin.ChatParams, plugin.ChatInput{
		SessionID: sessionID,
		Agent:     resolved.ID,
		Provider:  plugin.ProviderInfo{ID: resolved.Model.ProviderID},
		Model:     modelInfo(resolved),
	}, &out)
	request.Temperature = out.Temperature
	request.TopP = out.TopP
	if out.MaxOutputTokens != nil && *out.MaxOutputTokens > 0 {
		request.MaxTokens = *out.MaxOutputTokens
	}
	if out.Options != nil {
		request.Reasoning = out.Options
	}
}

// applyToolArgs runs the tool.execute.before hook, letting a plugin rewrite a
// call's arguments before the tool runs. It returns the arguments to use.
func (r *Runner) applyToolArgs(ctx context.Context, sessionID, callID, name string, args map[string]any) map[string]any {
	if r.Plugins == nil || !r.Plugins.Registered(plugin.ToolExecuteBefore.Name()) {
		return args
	}
	out := plugin.ToolExecuteBeforeOutput{Args: args}
	_ = plugin.Trigger(ctx, r.Plugins, plugin.ToolExecuteBefore, plugin.ToolExecuteBeforeInput{
		Tool:      name,
		SessionID: sessionID,
		CallID:    callID,
	}, &out)
	if out.Args == nil {
		return args
	}
	return out.Args
}

// applyToolOutput runs the tool.execute.after hook over a settled call's
// result. Only the output text reaches the model through this port's tool
// contract, so the title and metadata a plugin sets are available to later
// hooks but are not injected into the transcript.
func (r *Runner) applyToolOutput(ctx context.Context, sessionID, callID, name string, args map[string]any, output string) string {
	if r.Plugins == nil || !r.Plugins.Registered(plugin.ToolExecuteAfter.Name()) {
		return output
	}
	out := plugin.ToolExecuteAfterOutput{Output: output}
	_ = plugin.Trigger(ctx, r.Plugins, plugin.ToolExecuteAfter, plugin.ToolExecuteAfterInput{
		Tool:      name,
		SessionID: sessionID,
		CallID:    callID,
		Args:      args,
	}, &out)
	return out.Output
}

// askPlugins runs the permission.ask hook, giving a plugin the chance to
// settle an approval before the user is interrupted. It returns the decision,
// which is [plugin.PermissionAskStatus] when no plugin took one.
func (r *Runner) askPlugins(ctx context.Context, input ToolPermissionInput) string {
	if r.Plugins == nil || !r.Plugins.Registered(plugin.PermissionAsk.Name()) {
		return plugin.PermissionAskStatus
	}
	out := plugin.PermissionAskOutput{Status: plugin.PermissionAskStatus}
	_ = plugin.Trigger(ctx, r.Plugins, plugin.PermissionAsk, plugin.PermissionAskInput{
		SessionID: input.SessionID,
		MessageID: input.AssistantMessageID,
		CallID:    input.CallID,
		Action:    input.Action,
		Resources: input.Resources,
	}, &out)
	return out.Status
}

// modelInfo projects the turn's resolved model into the plugin-facing shape.
func modelInfo(resolved resolvedAgent) plugin.ModelInfo {
	return plugin.ModelInfo{
		ProviderID: resolved.Model.ProviderID,
		ModelID:    resolved.Model.ID,
		Variant:    resolved.Model.Variant,
	}
}
