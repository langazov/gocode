package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/langazov/gocode-go/internal/installation"
	"github.com/langazov/gocode-go/internal/tui/client"
)

var appVersion = installation.Version

// homeDirectory is the status bar's left segment: the abbreviated working
// directory, suffixed with the git branch when one is checked out.
func (a *App) homeDirectory() string {
	out := abbreviateHome(a.cwd, a.homeDir)
	if a.gitBranch != "" {
		out += ":" + a.gitBranch
	}
	return out
}

// abbreviateHome replaces the user's home prefix with "~", mirroring the
// TypeScript abbreviateHome helper.
func abbreviateHome(input, home string) string {
	if home == "" {
		return input
	}
	rel, err := filepath.Rel(home, input)
	if err != nil {
		return input
	}
	if rel == "." {
		return "~"
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return input
	}
	return "~" + string(filepath.Separator) + rel
}

// gitBranch walks up from dir to the repository root and returns the
// checked-out branch, or "" when detached or outside a repository.
func gitBranch(dir string) string {
	for {
		head, err := os.ReadFile(filepath.Join(dir, ".git", "HEAD"))
		if err == nil {
			ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
			if ok {
				return ref
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func sessionTitleOf(session client.Session) string {
	if strings.TrimSpace(session.Title) != "" {
		return session.Title
	}
	return session.ID
}

func relativeTime(millis int64) string {
	if millis <= 0 {
		return ""
	}
	diff := time.Since(time.UnixMilli(millis))
	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		return fmt.Sprintf("%dm ago", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(diff.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(diff.Hours()/24))
	}
}

// truncateRunes shortens visible text to max cells while preserving ANSI
// escape sequences intact.
func truncateRunes(value string, max int) string {
	if lipgloss.Width(value) <= max {
		return value
	}
	var out strings.Builder
	cells := 0
	inEscape := false
	for _, r := range value {
		if inEscape {
			out.WriteRune(r)
			if r == 'm' {
				inEscape = false
			}
			continue
		}
		if r == 0x1b {
			inEscape = true
			out.WriteRune(r)
			continue
		}
		if cells >= max-1 {
			out.WriteString("…")
			break
		}
		out.WriteRune(r)
		cells++
	}
	return out.String()
}
