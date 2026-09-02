package tui

import "charm.land/lipgloss/v2"

// styles mirrors the semantic style keys the TypeScript TUI reads from the
// resolved theme (text, muted, primary, …).
type styles struct {
	Title    lipgloss.Style
	Text     lipgloss.Style
	Muted    lipgloss.Style
	Primary  lipgloss.Style
	Accent   lipgloss.Style
	Warning  lipgloss.Style
	Error    lipgloss.Style
	Success  lipgloss.Style
	Selected lipgloss.Style
}

func (a *App) styles() styles {
	c := a.theme.Colors
	return styles{
		Title:    lipgloss.NewStyle().Bold(true).Foreground(c.Primary),
		Text:     lipgloss.NewStyle().Foreground(c.Text),
		Muted:    lipgloss.NewStyle().Foreground(c.TextMuted),
		Primary:  lipgloss.NewStyle().Foreground(c.Primary),
		Accent:   lipgloss.NewStyle().Foreground(c.Accent),
		Warning:  lipgloss.NewStyle().Foreground(c.Warning),
		Error:    lipgloss.NewStyle().Foreground(c.Error),
		Success:  lipgloss.NewStyle().Foreground(c.Success),
		Selected: lipgloss.NewStyle().Bold(true).Background(c.BackgroundElement),
	}
}
