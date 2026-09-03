package tui

import (
	"math/rand"
	"strings"
)

// tips ports the home screen tip rotation (packages/tui
// src/feature-plugins/home/tips-view.tsx): one tip is picked at random at
// startup, with {highlight}…{/highlight} segments rendered in text color and
// the rest muted. Shortcut-based entries are pre-formatted with this port's
// fixed keybinds.
var tips = []string{
	"Type {highlight}@{/highlight} followed by a filename to fuzzy search and attach files",
	"Start a message with {highlight}!{/highlight} to run shell commands (e.g., {highlight}!ls -la{/highlight})",
	"Press {highlight}tab{/highlight} to cycle between Build and Plan agents",
	"Use {highlight}/undo{/highlight} to revert the last message and file changes",
	"Use {highlight}/redo{/highlight} to restore previously undone messages and file changes",
	"Run {highlight}/share{/highlight} to create a public opencode.ai link",
	"Drag and drop images or PDFs into the terminal as context",
	"Use {highlight}/editor{/highlight} to compose messages in your external editor",
	"Run {highlight}/init{/highlight} to auto-generate project rules based on your codebase",
	"Use {highlight}/models{/highlight} to switch between available AI models",
	"Use {highlight}/themes{/highlight} or {highlight}ctrl+x t{/highlight} to switch between 2 built-in themes",
	"Use {highlight}/new{/highlight} to start a fresh conversation session",
	"Use {highlight}/sessions{/highlight} to list, pin, and continue sessions",
	"Run {highlight}/compact{/highlight} to summarize long sessions near context limits",
	"Use {highlight}/export{/highlight} to save the conversation as Markdown",
	"Press {highlight}ctrl+p{/highlight} to see all available actions and commands",
	"Run {highlight}/connect{/highlight} to add API keys for 75+ supported LLM providers",
	"The leader key is {highlight}ctrl+x{/highlight}; combine with other keys for quick actions",
	"Switch to {highlight}Plan{/highlight} agent for suggestions without making changes",
	"Use {highlight}@agent-name{/highlight} in prompts to invoke specialized subagents",
	"Create {highlight}gocode.json{/highlight} for server settings, and {highlight}tui.json{/highlight} for TUI",
	"Place TUI settings in {highlight}~/.config/gocode/tui.json{/highlight} for global config",
	"Add {highlight}$schema{/highlight} to your config for autocomplete in your editor",
	"Configure {highlight}model{/highlight} in config to set your default model",
	"Override any keybind in {highlight}tui.json{/highlight} via the {highlight}keybinds{/highlight} section",
	"Set any keybind to {highlight}none{/highlight} to disable it completely",
	"Configure local or remote MCP servers in the {highlight}mcp{/highlight} config section",
	"Add {highlight}.md{/highlight} files to {highlight}.gocode/commands/{/highlight} for reusable prompts",
	"Use {highlight}$ARGUMENTS{/highlight}, {highlight}$1{/highlight}, {highlight}$2{/highlight} in custom commands for dynamic input",
	"Use backticks to inject shell output (e.g., {highlight}`git status`{/highlight})",
	"Add {highlight}.md{/highlight} files to {highlight}.gocode/agents/{/highlight} for specialized AI personas",
	"Configure per-agent permissions for {highlight}edit{/highlight}, {highlight}bash{/highlight}, and {highlight}webfetch{/highlight} tools",
	`Use patterns like {highlight}"git *": "allow"{/highlight} for granular bash permissions`,
	`Set {highlight}"rm -rf *": "deny"{/highlight} to block destructive commands`,
	`Configure {highlight}"git push": "ask"{/highlight} to require approval before pushing`,
	`Set {highlight}"formatter": true{/highlight} to enable built-in formatters`,
	`Set {highlight}"formatter": false{/highlight} to disable inherited formatters`,
	"Define custom formatter commands with file extensions in config",
	`Set {highlight}"lsp": true{/highlight} to enable built-in LSP code analysis`,
	"Create {highlight}.ts{/highlight} files in {highlight}.gocode/tools/{/highlight} to define new LLM tools",
	"Tool definitions can invoke scripts written in Python, Go, etc",
	"Add {highlight}.ts{/highlight} files to {highlight}.gocode/plugins/{/highlight} for event hooks",
	"Use plugins to send OS notifications when sessions complete",
	"Create a plugin to prevent GoCode from reading sensitive files",
	"Use {highlight}gocode run{/highlight} for non-interactive scripting",
	"Use {highlight}gocode --continue{/highlight} to resume the last session",
	"Use {highlight}gocode run -f file.ts{/highlight} to attach files via CLI",
	"Use {highlight}--format json{/highlight} for machine-readable output in scripts",
	"Run {highlight}gocode serve{/highlight} for headless API access to GoCode",
	"Use {highlight}gocode run --attach{/highlight} to connect to a running server",
	"Run {highlight}gocode upgrade{/highlight} to update to the latest version",
	"Run {highlight}gocode auth list{/highlight} to see all configured providers",
	"Run {highlight}gocode agent create{/highlight} for guided agent creation",
	"Use {highlight}/gocode{/highlight} in GitHub issues/PRs to trigger AI actions",
	"Run {highlight}gocode github install{/highlight} to set up the GitHub workflow",
	"Comment {highlight}/gocode fix this{/highlight} on issues to auto-create PRs",
	"Comment {highlight}/oc{/highlight} on PR code lines for targeted code reviews",
	`Use {highlight}"theme": "system"{/highlight} to match your terminal's colors`,
	"Create JSON theme files in {highlight}.gocode/themes/{/highlight} directory",
	"Themes support dark/light variants for both modes",
	"Use numeric xterm color codes 0-255 in custom theme JSON",
	"Use {highlight}{env:VAR_NAME}{/highlight} for environment variables in config",
	"Use {highlight}{file:path}{/highlight} to include file contents in config values",
	"Use {highlight}instructions{/highlight} in config to load additional rules files",
	"Set agent {highlight}temperature{/highlight} from 0.0 (focused) to 1.0 (creative)",
	"Configure {highlight}steps{/highlight} to limit agentic iterations per request",
	`Set {highlight}"tools": {"bash": false}{/highlight} to disable specific tools`,
	`Set {highlight}"mcp_*": false{/highlight} to disable all tools from an MCP server`,
	"Override global tool settings per agent configuration",
	`Set {highlight}"share": "auto"{/highlight} to automatically share all sessions`,
	`Set {highlight}"share": "disabled"{/highlight} to prevent any session sharing`,
	"Run {highlight}/unshare{/highlight} to remove a session from public access",
	"Permission {highlight}doom_loop{/highlight} prevents infinite tool call loops",
	"Permission {highlight}external_directory{/highlight} protects files outside project",
	"Run {highlight}gocode debug config{/highlight} to troubleshoot configuration",
	"Use {highlight}--print-logs{/highlight} flag to see detailed logs in stderr",
	"Enable {highlight}scroll_acceleration{/highlight} in {highlight}tui.json{/highlight} for smooth scrolling",
	"Run {highlight}docker run -it --rm ghcr.io/anomalyco/gocode{/highlight} in a container",
	"Use {highlight}/connect{/highlight} with OpenCode Zen for curated, tested models",
	"Commit your project's {highlight}AGENTS.md{/highlight} file to Git for team sharing",
	"Use {highlight}/review{/highlight} to review uncommitted changes, branches, or PRs",
	"Use {highlight}/help{/highlight} to show the help dialog",
	"Use {highlight}/rename{/highlight} to rename the current session",
}

func randomTip() string {
	return tips[rand.Intn(len(tips))]
}

type tipPart struct {
	text      string
	highlight bool
}

// parseTip splits a tip into plain and highlighted segments.
func parseTip(tip string) []tipPart {
	var parts []tipPart
	for {
		start := strings.Index(tip, "{highlight}")
		if start < 0 {
			break
		}
		rest := tip[start+len("{highlight}"):]
		end := strings.Index(rest, "{/highlight}")
		if end < 0 {
			break
		}
		if start > 0 {
			parts = append(parts, tipPart{text: tip[:start]})
		}
		parts = append(parts, tipPart{text: rest[:end], highlight: true})
		tip = rest[end+len("{/highlight}"):]
	}
	if tip != "" {
		parts = append(parts, tipPart{text: tip})
	}
	return parts
}

// tipLine renders the "● Tip …" row: label in warning color, highlighted
// segments in text color, plain text muted.
func (a *App) tipLine(maxWidth int) string {
	out := a.styles().Warning.Render("● Tip ")
	for _, part := range parseTip(a.tip) {
		if part.highlight {
			out += a.styles().Text.Render(part.text)
			continue
		}
		out += a.styles().Muted.Render(part.text)
	}
	return truncateRunes(out, maxWidth)
}
