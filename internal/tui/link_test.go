package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/anomalyco/opencode-go/internal/tui/theme"
)

// These tests cover the ui/spinner.ts, ui/toast.tsx, and ui/link.tsx ports:
// the animations-disabled spinner fallback, toast variant/title/duration
// fidelity and its top-right absolute placement, and Link's OSC8 hyperlink
// render plus click-to-open behavior.

func TestSpinnerGlyphRespectsAnimationsEnabled(t *testing.T) {
	app := &App{spinnerFrame: 2, animationsEnabled: true}
	if got, want := app.spinnerGlyph(), spinnerFrames[2]; got != want {
		t.Fatalf("animated glyph = %q, want %q", got, want)
	}
	app.animationsEnabled = false
	if got := app.spinnerGlyph(); got != "⋯" {
		t.Fatalf("disabled fallback = %q, want the static ⋯ glyph (Spinner's <Show> fallback)", got)
	}
}

func TestShowToastOptionsDefaultsAndFields(t *testing.T) {
	app := &App{width: 100, height: 30}
	cmd := app.showToastOptions(toastOptions{title: "Heads up", message: "something happened", variant: toastWarning})
	if cmd == nil {
		t.Fatal("expected an expiry-tick command")
	}
	if app.toast == nil {
		t.Fatal("expected a toast to be set")
	}
	if app.toast.title != "Heads up" || app.toast.text != "something happened" || app.toast.variant != toastWarning {
		t.Fatalf("unexpected toast: %+v", app.toast)
	}
	remaining := time.Until(app.toast.expires)
	if remaining <= 0 || remaining > defaultToastDuration {
		t.Fatalf("expected the ToastInput default duration (%s) when none is given, got %s remaining", defaultToastDuration, remaining)
	}
}

func TestShowToastVariantFromIsError(t *testing.T) {
	app := &App{width: 100, height: 30}
	app.showToast("ok", false)
	if app.toast.variant != toastInfo {
		t.Fatalf("isError=false should be variant info, got %v", app.toast.variant)
	}
	app.showToast("boom", true)
	if app.toast.variant != toastError {
		t.Fatalf("isError=true should be variant error, got %v", app.toast.variant)
	}
}

func TestToastPanelShowsTitleAndVariantColor(t *testing.T) {
	app := &App{width: 100, height: 30, theme: theme.Dark()}
	app.toast = &toast{title: "Update", text: "context compacted", variant: toastSuccess, expires: time.Now().Add(time.Minute)}
	panel, _ := app.viewToastPanel()
	plain := ansi.Strip(panel)
	if !strings.Contains(plain, "Update") {
		t.Fatalf("title should render, got %q", plain)
	}
	if !strings.Contains(plain, "context compacted") {
		t.Fatalf("message should render, got %q", plain)
	}
	if got, want := app.toastVariantColor(toastSuccess), app.theme.Success; got != want {
		t.Fatalf("success variant should border in theme.Success, got %v want %v", got, want)
	}
	if got, want := app.toastVariantColor(toastInfo), app.theme.Info; got != want {
		t.Fatalf("info variant should border in theme.Info, got %v want %v", got, want)
	}
}

func TestCompositeToastPlacesPanelTopRight(t *testing.T) {
	app := &App{width: 100, height: 20, theme: theme.Dark()}
	app.toast = &toast{text: "hi", variant: toastInfo, expires: time.Now().Add(time.Minute)}
	blankRow := strings.Repeat(" ", app.width)
	base := strings.Repeat(blankRow+"\n", app.height-1) + blankRow

	out := app.compositeToast(base)
	lines := strings.Split(ansi.Strip(out), "\n")

	panel, _ := app.viewToastPanel()
	panelW := lipgloss.Width(panel)
	wantLeft := app.width - panelW - 2

	// TS: position="absolute" top={2} right={2} — the box originates at row
	// 2 (visible there as the bordered-but-empty paddingTop row) with its
	// right edge 2 columns from the screen edge; its message lands at row 3
	// (paddingTop's one row down).
	if len(lines) <= 3 {
		t.Fatalf("expected at least 4 rows, got %d: %q", len(lines), lines)
	}
	if borderCol := strings.IndexRune(lines[2], '┃'); borderCol != wantLeft {
		t.Fatalf("expected the panel's left border at col %d (top=2), got %d in %q", wantLeft, borderCol, lines[2])
	}
	if rightEdge := wantLeft + panelW - 1; app.width-1-rightEdge != 2 {
		t.Fatalf("expected the panel's right edge 2 columns from the screen edge, got %d columns", app.width-1-rightEdge)
	}
	if !strings.Contains(lines[3], "hi") {
		t.Fatalf("expected the toast message on row 3 (top=2 + paddingTop=1), got lines: %q", lines)
	}
}

func TestRenderLinkEmitsOSC8Hyperlink(t *testing.T) {
	href := "https://example.com/docs"
	rendered := renderLink(href, "docs", lipgloss.NewStyle())
	if !strings.Contains(rendered, href) {
		t.Fatalf("rendered link should carry the href in its OSC8 escape, got %q", rendered)
	}
	if plain := ansi.Strip(rendered); plain != "docs" {
		t.Fatalf("visible text should be exactly the label, got %q", plain)
	}
	// children defaults to href when omitted, mirroring `children ?? href`.
	if plain := ansi.Strip(renderLink(href, "", lipgloss.NewStyle())); plain != href {
		t.Fatalf("empty text should default to href, got %q", plain)
	}
}

func TestToastLinkClickOpensURL(t *testing.T) {
	var opened string
	original := openURL
	openURL = func(href string) error { opened = href; return nil }
	defer func() { openURL = original }()

	app := &App{width: 60, height: 20, theme: theme.Dark()}
	app.toast = &toast{text: "details: https://example.com/docs", variant: toastInfo, expires: time.Now().Add(time.Minute)}
	blankRow := strings.Repeat(" ", app.width)
	base := strings.Repeat(blankRow+"\n", app.height-1) + blankRow
	app.compositeToast(base)

	if len(app.linkHits) != 1 {
		t.Fatalf("expected exactly one link hit, got %+v", app.linkHits)
	}
	hit := app.linkHits[0]
	if hit.href != "https://example.com/docs" {
		t.Fatalf("unexpected href %q", hit.href)
	}

	app.handleClick(hit.colStart, hit.row)
	if opened != hit.href {
		t.Fatalf("clicking the link span should open it, got opened=%q", opened)
	}

	// Clicking just outside the span must not trigger it.
	opened = ""
	app.handleClick(hit.colEnd, hit.row)
	if opened != "" {
		t.Fatalf("click past the link's end should not open it, opened %q", opened)
	}
}

func TestToastClearsLinkHitsWhenExpired(t *testing.T) {
	app := &App{width: 60, height: 20, theme: theme.Dark()}
	app.toast = &toast{text: "https://example.com", variant: toastInfo, expires: time.Now().Add(-time.Second)}
	app.linkHits = []linkHit{{row: 1, colStart: 1, colEnd: 2, href: "stale"}}
	base := strings.Repeat(" ", app.width)
	app.compositeToast(base)
	if app.linkHits != nil {
		t.Fatalf("an expired toast should clear stale link hits, got %+v", app.linkHits)
	}
}
