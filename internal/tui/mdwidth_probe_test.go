package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestHardwrapProbe(t *testing.T) {
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000")).Bold(true).Render("very/long/path/here -") +
		lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00")).Render(" tail")
	for _, w := range []int{10, 12, 21} {
		out := ansi.Hardwrap(styled, w, false)
		fmt.Printf("w=%d\n", w)
		for _, l := range strings.Split(out, "\n") {
			fmt.Printf("  [%2d] %q\n", ansi.StringWidth(l), l)
		}
	}
	// cjk boundary
	cjk := strings.Repeat("你好", 11) // 22 wide, cut at odd width 5
	for _, l := range strings.Split(ansi.Hardwrap(cjk, 5, false), "\n") {
		fmt.Printf("cjk [%2d] %q\n", ansi.StringWidth(l), l)
	}
}
