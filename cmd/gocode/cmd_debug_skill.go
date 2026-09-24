package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/langazov/gocode-go/internal/clix"
	"github.com/langazov/gocode-go/internal/global"
	"github.com/langazov/gocode-go/internal/skill"
)

// debugSkillCommand lists every skill the session would see, in precedence
// order — the same roots bootStack discovers from, scanned without booting
// the full stack (no DB, no providers), so it answers "what skills are
// available here and where did each come from" even in a broken environment.
func debugSkillCommand() *clix.Command {
	return &clix.Command{Name: "skill", Describe: "list all available skills", Run: func(a *clix.Args) error {
		workdir, err := os.Getwd()
		if err != nil {
			return err
		}
		// Mirrors the roots in bootStack, project-first so the precedence is
		// visible in the output: a project skill shadows a global one, which
		// shadows one compiled into the binary.
		skills := skill.Discover(
			filepath.Join(workdir, ".gocode"),
			filepath.Join(workdir, ".agents"),
			filepath.Join(global.Resolve().Config, "gocode"),
			filepath.Join(global.Resolve().Home, ".agents"),
		)
		infos := skills.List()
		if len(infos) == 0 {
			fmt.Println("no skills found")
			return nil
		}
		for _, info := range infos {
			origin := info.Location
			if skill.IsBuiltin(origin) {
				origin = "built-in (compiled into the binary)"
			}
			fmt.Printf("%s\t%s\t%s\n", info.Name, origin, info.Description)
		}
		return nil
	}}
}
