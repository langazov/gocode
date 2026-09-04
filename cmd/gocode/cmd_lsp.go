package main

import (
	"fmt"
	"strings"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/config"
	"github.com/langazov/gocode-go/internal/configedit"
)

// lspCommand manages the `lsp` config section.
//
// Most servers need nothing here: the built-in registry names them and they
// start as soon as their binary is on PATH. This is for the rest — a server
// installed somewhere PATH does not reach (a Homebrew `libexec`, a versioned
// directory), or one the registry does not know about at all. `gocode debug
// lsp` remains the place to ask what is running and why.
func lspCommand() *clix.Command {
	return &clix.Command{
		Name:     "lsp",
		Describe: "manage language server configuration",
		Demand:   true,
		Sub: []*clix.Command{
			{
				Name:        "enable",
				Describe:    "register a language server in the global config",
				Positionals: []clix.Positional{{Name: "id", Required: true, Describe: "server id, e.g. mdlsp"}},
				Flags: []clix.Flag{
					globalFlag,
					{Name: "command", Kind: clix.KindStringArray, Describe: "command to run, repeated for arguments (e.g. --command mdlsp --command --stdio)"},
					{Name: "extensions", Kind: clix.KindString, Describe: "comma-separated file extensions, e.g. .md,.markdown"},
				},
				Run: runLSPEnable,
			},
			{
				Name:        "disable",
				Describe:    "remove a language server from the global config",
				Positionals: []clix.Positional{{Name: "id", Required: true, Describe: "server id"}},
				Flags:       []clix.Flag{globalFlag},
				Run:         runLSPDisable,
			},
		},
	}
}

func runLSPEnable(a *clix.Args) error {
	if err := requireGlobal(a); err != nil {
		return err
	}
	id := a.PositionalOr("id", "")

	command := a.Array("command")
	if len(command) == 0 {
		// Without a command there is nothing to add that the built-in registry
		// does not already say, and an entry with no command can never start.
		return &usageError{msg: fmt.Sprintf("--command is required: %s needs to know what to run", id)}
	}

	server := config.LSPServer{Command: command, Extensions: parseExtensions(a.String("extensions"))}
	result, err := configedit.EnableLSP(id, server)
	return reportEdit(result, err)
}

func runLSPDisable(a *clix.Args) error {
	if err := requireGlobal(a); err != nil {
		return err
	}
	result, err := configedit.DisableLSP(a.PositionalOr("id", ""))
	return reportEdit(result, err)
}

// parseExtensions splits the comma-separated list, tolerating extensions
// written without their leading dot since that is the common slip.
func parseExtensions(raw string) []string {
	var out []string
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.HasPrefix(item, ".") && strings.Contains(item, ".") {
			// A whole filename like "Dockerfile" is legal, but "foo.md" is a
			// mistake for ".md"; only prefix the bare-extension case.
			out = append(out, item)
			continue
		}
		if !strings.HasPrefix(item, ".") && !isFilename(item) {
			item = "." + item
		}
		out = append(out, item)
	}
	return out
}

// isFilename reports whether an entry names a whole file rather than an
// extension. The registry treats a dotless entry as a filename (Dockerfile),
// so capitalisation is the only signal available.
func isFilename(item string) bool {
	return item != strings.ToLower(item)
}
