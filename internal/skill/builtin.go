package skill

import (
	"embed"
	"errors"
	"strings"
	"sync"

	"github.com/langazov/gocode-go/internal/markdown"
)

// Built-in skills are compiled into the binary — the configure-gocode skill
// ships in every archive, bare binary, `go install` and Homebrew install
// without any installer wiring, and cannot drift out of sync with the
// binary's own behavior. Same precedent as internal/modelsdev's baked-in
// catalog snapshot.
//
// Layout mirrors the on-disk layout the scanner understands:
//
//	builtin/<name>/SKILL.md
//
//go:embed builtin
var builtinFS embed.FS

// builtinLocation marks a skill compiled into the binary rather than living
// on disk. Downstream consumers (the skill tool's file listing, the slash
// command template) treat it as "no supporting files, no base directory".
const builtinLocation = "<built-in>"

// Builtins loads the skills compiled into the binary. Each embedded SKILL.md
// is parsed once; the parse is cheap but pointless to repeat at every boot.
// A malformed embedded skill is skipped rather than fatal — the same
// resilience guarantee Scan gives files on disk.
func Builtins() []Info {
	builtinsOnce.Do(loadBuiltins)
	return builtins
}

var (
	builtins     []Info
	builtinsOnce sync.Once
)

func loadBuiltins() {
	entries, err := builtinFS.ReadDir("builtin")
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := "builtin/" + entry.Name() + "/SKILL.md"
		raw, err := builtinFS.ReadFile(path)
		if err != nil {
			// A directory without SKILL.md is skipped, not fatal — matching
			// Scan's treatment of an unreadable file.
			continue
		}
		info, err := parseSkill(string(raw), entry.Name())
		if err != nil {
			continue
		}
		info.Location = builtinLocation
		builtins = append(builtins, info)
	}
}

// IsBuiltin reports whether a skill was compiled into the binary.
func IsBuiltin(location string) bool { return location == builtinLocation }

// parseSkill is the string-taking half of Load, shared with the built-in
// loader. The name falls back to the directory name when the frontmatter
// does not declare one, same rule Load applies to on-disk files.
func parseSkill(raw, name string) (Info, error) {
	doc, err := markdown.Parse(raw)
	if err != nil {
		return Info{}, err
	}
	skillName := doc.String("name")
	if skillName == "" {
		skillName = strings.TrimSpace(name)
	}
	if skillName == "" {
		return Info{}, errors.New("frontmatter has no name")
	}
	return Info{
		Name:        skillName,
		Description: doc.String("description"),
		Slash:       doc.Bool("slash"),
		Content:     doc.Content,
	}, nil
}
