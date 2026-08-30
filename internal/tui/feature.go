package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/anomalyco/opencode-go/internal/tui/client"
)

// spinnerFrames mirrors the braille spinner used by the TypeScript TUI.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	toastTTL    = 3 * time.Second
	spinnerTick = 120 * time.Millisecond
)

func (a *App) spinnerLabel() string {
	frame := spinnerFrames[a.spinnerFrame%len(spinnerFrames)]
	return a.styles().Warning.Render(frame + " working…")
}

type spinnerTickMsg struct{}

func (a *App) startSpinner() tea.Cmd {
	if !a.busy {
		return nil
	}
	return tea.Tick(spinnerTick, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// toast is a transient notification, mirroring ui/toast.tsx.
type toast struct {
	text    string
	isError bool
	expires time.Time
}

func (a *App) showToast(text string, isError bool) tea.Cmd {
	a.toast = &toast{text: text, isError: isError, expires: time.Now().Add(toastTTL)}
	return tea.Tick(toastTTL, func(time.Time) tea.Msg { return toastExpiredMsg{} })
}

type toastExpiredMsg struct{}

// viewToast renders the active toast, docked bottom-right like the original.
func (a *App) viewToast(width int) string {
	if a.toast == nil || time.Now().After(a.toast.expires) {
		return ""
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(a.theme.BorderActive).
		Padding(0, 1)
	text := a.toast.text
	if a.toast.isError {
		style = style.BorderForeground(a.theme.Error)
		text = a.styles().Error.Render(text)
	} else {
		text = a.styles().Text.Render(text)
	}
	box := style.Render(truncateRunes(text, width/3))
	pad := width - lipgloss.Width(box)
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + box
}

// timelineOverlayItems lists user prompts for the timeline dialog
// (ctrl+x g), newest first like DialogTimeline, with the created time as the
// footer annotation.
func (a *App) timelineOverlayItems() []overlayItem {
	items := make([]overlayItem, 0, len(a.timeline))
	for i := len(a.timeline) - 1; i >= 0; i-- {
		message := a.timeline[i]
		if message.Type != "user" {
			continue
		}
		data, err := client.DecodeUser(message.Data)
		if err != nil || data.Text == "" {
			continue
		}
		messageID := message.ID
		items = append(items, overlayItem{
			label:  strings.ReplaceAll(data.Text, "\n", " "),
			value:  messageID,
			footer: time.UnixMilli(message.TimeCreated).Format("3:04 PM"),
			action: func() tea.Msg {
				return a.forkFrom(messageID)
			},
		})
	}
	return items
}

// forkFrom forks the active session at a message and opens the child.
func (a *App) forkFrom(messageID string) tea.Cmd {
	if a.active == nil {
		return staticMsg(statusMsg{text: "open a session first"})
	}
	c := a.client
	parent := a.active.ID
	return func() tea.Msg {
		child, err := c.Fork(a.ctx, parent, messageID)
		if err != nil {
			return statusMsg{text: "fork failed: " + err.Error()}
		}
		return sessionOpenedMsg{session: child}
	}
}

// childrenOverlay lists forked child sessions (the subagent dialog).
func (a *App) childrenOverlay() tea.Cmd {
	if a.active == nil {
		return staticMsg(statusMsg{text: "open a session first"})
	}
	c := a.client
	parent := a.active.ID
	return func() tea.Msg {
		children, err := c.Children(a.ctx, parent)
		if err != nil {
			return statusMsg{text: "failed to load children: " + err.Error()}
		}
		items := make([]overlayItem, 0, len(children))
		for i := range children {
			child := children[i]
			items = append(items, overlayItem{
				label: sessionTitleOf(child),
				hint:  relativeTime(child.TimeUpdated),
				value: child.ID,
				action: func() tea.Msg {
					a.active = &child
					a.view = viewChat
					a.timeline = nil
					a.scrollOffset = 0
					return reloadMsg{}
				},
			})
		}
		if len(items) == 0 {
			items = append(items, overlayItem{label: "(no forked sessions)"})
		}
		a.openList("Forked sessions", items)
		return nil
	}
}

// compactNow triggers immediate compaction (leader+c / session.compact).
func (a *App) compactNow() tea.Cmd {
	if a.active == nil {
		return staticMsg(statusMsg{text: "open a session first"})
	}
	c := a.client
	sessionID := a.active.ID
	return func() tea.Msg {
		compacted, err := c.Compact(a.ctx, sessionID)
		if err != nil {
			return statusMsg{text: "compact failed: " + err.Error()}
		}
		if !compacted {
			return statusMsg{text: "nothing to compact"}
		}
		return statusMsg{text: "context compacted"}
	}
}

// copyTranscript writes the whole conversation to the terminal clipboard via
// OSC52 (session.copy).
func (a *App) copyTranscript() tea.Cmd {
	var builder strings.Builder
	for _, message := range a.timeline {
		switch message.Type {
		case "user":
			if data, err := client.DecodeUser(message.Data); err == nil {
				builder.WriteString("you: " + data.Text + "\n\n")
			}
		case "assistant":
			if data, err := client.DecodeAssistant(message.Data); err == nil {
				for _, part := range data.Content {
					switch part.Type {
					case "text":
						if part.Text != "" {
							builder.WriteString("assistant: " + part.Text + "\n\n")
						}
					case "tool":
						builder.WriteString("assistant: [tool " + part.Name + "]\n")
					}
				}
			}
		}
	}
	text := builder.String()
	if strings.TrimSpace(text) == "" {
		return staticMsg(statusMsg{text: "nothing to copy"})
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Fprintf(os.Stdout, "\033]52;c;%s\a", encoded)
	return staticMsg(statusMsg{text: "transcript copied (OSC52)"})
}

// exportToEditor opens the current prompt in $EDITOR (session.export,
// ctrl+x e), suspending the interface for the edit session.
func (a *App) exportToEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	tmp, err := os.CreateTemp("", "opencode-prompt-*.md")
	if err != nil {
		return staticMsg(statusMsg{text: err.Error()})
	}
	if _, err := tmp.WriteString(a.input.Value()); err != nil {
		tmp.Close()
		return staticMsg(statusMsg{text: err.Error()})
	}
	tmp.Close()
	execCmd := exec.Command(editor, tmp.Name())
	editorPath := tmp.Name()
	return tea.ExecProcess(execCmd, func(err error) tea.Msg {
		os.Remove(editorPath)
		if err != nil {
			return statusMsg{text: "editor failed: " + err.Error()}
		}
		if content, readErr := os.ReadFile(editorPath); readErr == nil {
			a.input.SetValue(strings.TrimSpace(string(content)))
		}
		return nil
	})
}
