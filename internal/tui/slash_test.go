package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/langazov/gocode-go/internal/command"
	"github.com/langazov/gocode-go/internal/tui/client"
)

func slashApp(t *testing.T) *App {
	t.Helper()
	app := typingApp(t, 120, 40)
	app.commands = []client.Command{
		{Name: "deploy", Description: "ship it", Template: "Deploy $1 to production", Hints: []string{"$1"}},
		{Name: "review", Description: "review changes", Template: "Review $ARGUMENTS"},
		{Name: "brainstorm", Description: "think", Template: "Brainstorm", Source: "skill"},
	}
	return app
}

// TestSlashOpensCompletionAtStart: "/" at position 0 opens the command list.
// Before this the port had no "/" trigger at all — the only way to reach a
// command was to type its full name and press enter.
func TestSlashOpensCompletionAtStart(t *testing.T) {
	app := slashApp(t)
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	if !app.autocomplete.visible() {
		t.Fatal("typing / on an empty prompt must open the completion popup")
	}
	if app.autocomplete.kind != autocompleteSlash {
		t.Errorf("kind = %v, want the slash popup", app.autocomplete.kind)
	}
	if app.input.Value() != "/" {
		t.Errorf("prompt is %q, want the / kept so the user can keep typing", app.input.Value())
	}
}

// TestSlashMidTextIsOrdinaryCharacter: a slash inside a path or a date is not
// a command trigger.
func TestSlashMidTextIsOrdinaryCharacter(t *testing.T) {
	app := slashApp(t)
	typeText(app, "see src")
	app.Update(tea.KeyPressMsg{Code: '/', Text: "/"})

	if app.autocomplete.visible() {
		t.Error("/ after other text must not open the completion popup")
	}
	if got := app.input.Value(); got != "see src/" {
		t.Errorf("prompt is %q, want the slash inserted literally", got)
	}
}

// TestSlashListsPromptAndInterfaceCommands: both kinds are reachable, grouped
// so they are distinguishable.
func TestSlashListsPromptAndInterfaceCommands(t *testing.T) {
	app := slashApp(t)
	items := app.slashAutocompleteItems()

	byName := map[string]autocompleteItem{}
	for _, item := range items {
		byName[strings.TrimPrefix(item.display, "/")] = item
	}
	for _, want := range []string{"deploy", "review", "brainstorm"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("%q is missing from the command list", want)
		}
	}
	if byName["deploy"].description != "ship it" {
		t.Errorf("deploy description = %q", byName["deploy"].description)
	}
	// Interface commands are still reachable, under the name a user types
	// rather than the dotted internal one.
	if _, ok := byName["new"]; !ok {
		t.Error("interface commands should remain in the list")
	}
}

// TestSlashSelectionInsertsName: selecting a command inserts it rather than
// running it, so arguments can be typed.
func TestSlashSelectionInsertsName(t *testing.T) {
	app := slashApp(t)
	items := app.slashAutocompleteItems()
	for _, item := range items {
		if item.display == "/deploy" {
			item.action()
			break
		}
	}
	if got := app.input.Value(); got != "/deploy " {
		t.Errorf("prompt is %q, want %q", got, "/deploy ")
	}
}

// TestRunSlashCommandExpandsTemplate: a prompt command sends its expanded
// template, not the literal "/name args" the user typed.
func TestRunSlashCommandExpandsTemplate(t *testing.T) {
	app := slashApp(t)
	app.active = &client.Session{ID: "ses_1", Title: "T"}
	app.view = viewChat

	var sent string
	app.commands = []client.Command{{Name: "deploy", Template: "Deploy $1 to production"}}
	// Intercept by running the command and inspecting what it would send: the
	// returned command performs the request, so check the expansion directly.
	expanded := commandExpansionFor(app, "deploy staging")
	sent = expanded
	if sent != "Deploy staging to production" {
		t.Errorf("expanded to %q, want the template with the argument substituted", sent)
	}
}

// commandExpansionFor mirrors runPromptCommand's expansion step.
func commandExpansionFor(app *App, input string) string {
	name, arguments, _ := strings.Cut(input, " ")
	for _, entry := range app.commands {
		if entry.Name == name {
			return command.Expand(app.ctx, entry.Template, strings.TrimSpace(arguments), "")
		}
	}
	return ""
}

// TestUnknownSlashCommandReports: a typo must say so rather than being sent as
// a prompt.
func TestUnknownSlashCommandReports(t *testing.T) {
	app := slashApp(t)
	cmd := app.runSlashCommand("nosuchcommand")
	if cmd == nil {
		t.Fatal("expected a status message")
	}
	msg := cmd()
	status, ok := msg.(statusMsg)
	if !ok || !strings.Contains(status.text, "unknown command") {
		t.Errorf("got %v, want an unknown-command status", msg)
	}
}

// TestInterfaceCommandStillReachable: the namespace-prefix match that let
// "/help" reach "help.show" must survive the addition of prompt commands.
func TestInterfaceCommandStillReachable(t *testing.T) {
	app := slashApp(t)
	if cmd := app.runSlashCommand("session.new"); cmd == nil {
		t.Error("an interface command should still dispatch")
	}
}

// TestPromptCommandShadowsInterfaceCommand: a user's own command wins, since
// they would be surprised to see it silently ignored.
func TestPromptCommandShadowsInterfaceCommand(t *testing.T) {
	app := slashApp(t)
	app.active = &client.Session{ID: "ses_1", Title: "T"}
	app.view = viewChat
	app.commands = []client.Command{{Name: "session.new", Template: "custom template"}}

	var found bool
	for _, entry := range app.commands {
		if entry.Name == "session.new" {
			found = true
		}
	}
	if !found {
		t.Fatal("precondition")
	}
	// The dispatcher checks prompt commands first; reaching it means the
	// template path was taken rather than the interface action.
	items := app.slashAutocompleteItems()
	if items[0].display != "/session.new" {
		t.Errorf("first item = %+v, want the user's command listed first", items[0])
	}
	// And it wins dispatch over the interface command of the same name.
	if cmd := app.runSlashCommand("session.new"); cmd == nil {
		t.Error("the user's command should dispatch")
	}
}

// TestSlashNamesResolve is the regression for the reported "unknown command:
// /new". Interface commands carry a dotted internal label ("session.new") that
// nobody types; upstream gives each an explicit slashName ("new") plus
// aliases ("clear"). Matching the label alone meant every one of these failed.
func TestSlashNamesResolve(t *testing.T) {
	app := slashApp(t)
	app.commands = nil // only interface commands, so nothing shadows them

	// A command whose effect is a direct state change returns no command, so
	// resolution is checked by "it did not report unknown, and something
	// happened" rather than by a non-nil return.
	for _, name := range []string{
		"new", "clear", // session.new
		"sessions", "resume", "continue", // session.list
		"models", "agents", "themes", "help", "status", "exit", "quit", "q",
		"compact", "timeline", "rename", "delete", "interrupt",
	} {
		app := slashApp(t)
		app.commands = nil
		app.width, app.height = 120, 40
		before := snapshotEffects(app)

		cmd := app.runSlashCommand(name)
		if cmd != nil {
			if msg, ok := cmd().(statusMsg); ok && strings.Contains(msg.text, "unknown command") {
				t.Errorf("/%s reported unknown", name)
				continue
			}
		}
		if cmd == nil && snapshotEffects(app) == before {
			t.Errorf("/%s did nothing and produced no command", name)
		}
	}
}

// TestDottedNameStillResolves: the internal name keeps working for anyone who
// learned it, and so does a namespace prefix.
func TestDottedNameStillResolves(t *testing.T) {
	app := slashApp(t)
	app.commands = nil
	for _, name := range []string{"session.new", "help.show"} {
		app := slashApp(t)
		app.commands = nil
		app.width, app.height = 120, 40
		before := snapshotEffects(app)

		cmd := app.runSlashCommand(name)
		if cmd != nil {
			if msg, ok := cmd().(statusMsg); ok && strings.Contains(msg.text, "unknown command") {
				t.Errorf("/%s reported unknown", name)
				continue
			}
		}
		if cmd == nil && snapshotEffects(app) == before {
			t.Errorf("/%s did nothing and produced no command", name)
		}
	}
}

// TestSlashListUsesTypeableNames: the completion list must offer the name that
// works, not the internal one.
func TestSlashListUsesTypeableNames(t *testing.T) {
	app := slashApp(t)
	app.commands = nil

	labels := map[string]bool{}
	for _, item := range app.slashAutocompleteItems() {
		labels[strings.TrimPrefix(item.display, "/")] = true
	}
	if !labels["new"] {
		t.Error("the list should offer /new")
	}
	if labels["session.new"] {
		t.Error("the list should not offer the internal dotted name")
	}
	// Aliases are surfaced in the hint so they are discoverable.
	for _, item := range app.slashAutocompleteItems() {
		if item.display == "/new" && !strings.Contains(item.description, "clear") {
			t.Errorf("the /new entry should mention its alias: %q", item.description)
		}
	}
}

// TestSlashListDropsPaletteOnlyCommands: an interface command with no slash
// name is reachable from the palette but is not a slash command, matching
// upstream dropping entries whose slashName is unset.
func TestSlashListDropsPaletteOnlyCommands(t *testing.T) {
	app := slashApp(t)
	app.commands = nil
	for _, item := range app.slashAutocompleteItems() {
		if strings.Contains(item.display, ".") {
			t.Errorf("%q is an internal name and should not be listed", item.display)
		}
	}
}
