package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/langazov/gocode-go/internal/tui/client"
)

func longCommand(lines int) string {
	parts := make([]string, 0, lines)
	for i := 0; i < lines; i++ {
		parts = append(parts, fmt.Sprintf("echo this is line %d of a long shell script", i))
	}
	return strings.Join(parts, "\n")
}

func permissionApp(t *testing.T, width, height int, request *client.PermissionRequest) *App {
	t.Helper()
	app := newTestApp(t, "http://example.invalid")
	app.width, app.height = width, height
	app.view = viewChat
	app.active = &client.Session{ID: "ses_1", Title: "Test"}
	app.permission = request
	return app
}

// TestPermissionButtonsAlwaysVisible is the regression for "the permission
// dialog is placed too low and the buttons are not visible".
//
// The banner had no height cap, so a long body grew it past the terminal —
// measured at 34 rows in a 30-row terminal — and frame()'s MaxHeight cropped
// the bottom, taking the option bar with it. The one part of the prompt the
// user must reach was the part that disappeared.
func TestPermissionButtonsAlwaysVisible(t *testing.T) {
	sizes := []struct{ width, height int }{
		{120, 40}, {100, 30}, {100, 20}, {80, 14}, {80, 12},
	}
	for _, size := range sizes {
		for _, bodyLines := range []int{1, 25, 200} {
			app := permissionApp(t, size.width, size.height, &client.PermissionRequest{
				ID: "p1", Action: "bash", Resources: []string{longCommand(bodyLines)},
			})
			view := app.View()
			for _, want := range []string{"Allow once", "Allow always", "Reject"} {
				if !strings.Contains(view, want) {
					t.Errorf("%dx%d with a %d-line body: %q is not visible",
						size.width, size.height, bodyLines, want)
				}
			}
		}
	}
}

// TestPermissionBannerRespectsMaxHeight ports `maxHeight: 15` from the
// non-expanded branch of permission.tsx's Prompt.
func TestPermissionBannerRespectsMaxHeight(t *testing.T) {
	app := permissionApp(t, 120, 60, &client.PermissionRequest{
		ID: "p1", Action: "bash", Resources: []string{longCommand(200)},
	})
	height := app.askBannerHeight()
	if height > permissionMaxHeight {
		t.Errorf("banner is %d rows, want at most %d", height, permissionMaxHeight)
	}
	if height == 0 {
		t.Error("expected a banner")
	}
}

// TestPermissionBannerShrinksBelowMaxHeightInShortTerminal: maxHeight is a
// maximum, not a fixed height. The original's flex container still shrinks
// below it when the column is short, which is what keeps the bar on screen —
// capping at a flat 15 put the buttons back off-screen at 14 rows.
func TestPermissionBannerShrinksBelowMaxHeightInShortTerminal(t *testing.T) {
	app := permissionApp(t, 80, 14, &client.PermissionRequest{
		ID: "p1", Action: "bash", Resources: []string{longCommand(200)},
	})
	if height := app.askBannerHeight(); height >= app.height {
		t.Errorf("banner is %d rows in a %d-row terminal", height, app.height)
	}
}

// TestPermissionViewNeverOverflows: the rendered column must fit the terminal,
// or frame() crops it and something below is lost.
func TestPermissionViewNeverOverflows(t *testing.T) {
	for _, height := range []int{12, 14, 20, 30, 40} {
		app := permissionApp(t, 100, height, &client.PermissionRequest{
			ID: "p1", Action: "bash", Resources: []string{longCommand(80)},
		})
		if got := strings.Count(app.View(), "\n") + 1; got != height {
			t.Errorf("rendered %d rows for a %d-row terminal", got, height)
		}
	}
}

// TestPermissionTruncationIsAnnounced: the body is cut, so it has to say so —
// the original scrolls instead and offers a fullscreen toggle, neither of
// which exists here, and silently truncated text reads as complete.
func TestPermissionTruncationIsAnnounced(t *testing.T) {
	app := permissionApp(t, 100, 30, &client.PermissionRequest{
		ID: "p1", Action: "bash", Resources: []string{longCommand(40)},
	})
	if banner := app.permissionBanner(); !strings.Contains(banner, "more lines") {
		t.Errorf("a truncated body must say how much was hidden:\n%s", banner)
	}
}

// TestShortPermissionBodyIsNotTruncated: the cap must not touch a body that
// already fits.
func TestShortPermissionBodyIsNotTruncated(t *testing.T) {
	app := permissionApp(t, 100, 40, &client.PermissionRequest{
		ID: "p1", Action: "bash", Resources: []string{"go test ./..."},
	})
	banner := app.permissionBanner()
	if strings.Contains(banner, "more lines") {
		t.Errorf("a short body must not be truncated:\n%s", banner)
	}
	if !strings.Contains(banner, "go test ./...") {
		t.Errorf("the command is missing:\n%s", banner)
	}
}

// TestTimelineYieldsRowsToPermissionBanner: the banner's rows have to come out
// of the timeline's budget. Before this they did not, which is what made the
// column overflow.
func TestTimelineYieldsRowsToPermissionBanner(t *testing.T) {
	app := permissionApp(t, 100, 30, nil)
	without := app.viewportHeight()

	app.permission = &client.PermissionRequest{
		ID: "p1", Action: "bash", Resources: []string{longCommand(40)},
	}
	with := app.viewportHeight()

	if with >= without {
		t.Errorf("viewportHeight is %d with a banner and %d without; the timeline must yield the rows the banner takes", with, without)
	}
}

// TestExternalDirectoryPromptNamesTheDirectory is the regression for a prompt
// that read, in full, "Call tool external_directory" / "Tool:
// external_directory" — the generic fallback, because the port skipped TS's
// external_directory case in permission.tsx's info().
//
// It told the user something outside the project was being touched but not
// what, and the only safe answer to an unknown is "reject". The resources are
// the globs the grant is saved as, so the directory is recoverable from them.
func TestExternalDirectoryPromptNamesTheDirectory(t *testing.T) {
	app := permissionApp(t, 120, 40, &client.PermissionRequest{
		ID:        "p1",
		Action:    "external_directory",
		Resources: []string{"/srv/data/*", "/srv/logs/*"},
	})
	view := app.View()

	if strings.Contains(view, "Call tool external_directory") {
		t.Error("the prompt fell back to the generic title and never says which directory")
	}
	for _, want := range []string{"Access external directory /srv/data", "/srv/data/*", "/srv/logs/*"} {
		if !strings.Contains(view, want) {
			t.Errorf("prompt does not show %q", want)
		}
	}
}

// TestExternalDirectoryPromptSaysWhatAlwaysMeans: "Allow always" grants the
// directory subtree for the whole project, not just this one command, and a
// prompt that does not say so is asking for uninformed consent.
func TestExternalDirectoryPromptSaysWhatAlwaysMeans(t *testing.T) {
	app := permissionApp(t, 120, 40, &client.PermissionRequest{
		ID: "p1", Action: "external_directory", Resources: []string{"/srv/data/*"},
	})
	if view := app.View(); !strings.Contains(view, "everything under them") {
		t.Error("the prompt does not say that always covers subdirectories and the whole project")
	}
}

// TestWebPromptsNameTheirTarget: webfetch and websearch prompts rendered with
// an empty target ("WebFetch " and the generic fallback) because the runner
// read input["path"] from tools that carry "url" and "query". Approving a
// request that does not say what it is reaching is not consent.
func TestWebPromptsNameTheirTarget(t *testing.T) {
	for _, c := range []struct{ action, resource, want string }{
		{"webfetch", "https://docs.example/page", "WebFetch https://docs.example/page"},
		{"websearch", "how to write go", `Web search "how to write go"`},
	} {
		app := permissionApp(t, 120, 40, &client.PermissionRequest{
			ID: "p1", Action: c.action, Resources: []string{c.resource},
		})
		view := app.View()
		if !strings.Contains(view, c.want) {
			t.Errorf("%s prompt does not show %q", c.action, c.want)
		}
		if strings.Contains(view, "Call tool "+c.action) {
			t.Errorf("%s prompt fell back to the generic title", c.action)
		}
	}
}
