//go:build ignore

// pluginconfig adds or removes a plugin entry in the global gocode config.
//
// Installing a plugin copies it to plugin.InstallRoot(); it does *not* enable
// it. A plugin runs only when the config's `plugin` array names it, matching
// upstream — an installed-but-unlisted plugin is inert. This is what
// `make install-plugin` uses to close that gap, so an install is one step
// rather than a copy plus a hand edit.
//
//	go run tools/pluginconfig.go -add plugin-echo [-options '{"banner":"hi"}']
//	go run tools/pluginconfig.go -remove plugin-echo
//
// The editing itself lives in internal/configedit, which the shipped binary
// also exposes as `gocode plugin enable`. A Homebrew formula cannot `go run`
// anything, so the binary is the real entry point and this script is the
// Makefile's convenience wrapper around the same code.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/langazov/gocode-go/internal/configedit"
)

func main() {
	add := flag.String("add", "", "plugin reference to enable")
	remove := flag.String("remove", "", "plugin reference to disable")
	options := flag.String("options", "", "JSON object of plugin options, for -add")
	flag.Parse()

	if (*add == "") == (*remove == "") {
		fmt.Fprintln(os.Stderr, "usage: pluginconfig -add <ref> [-options '<json>'] | -remove <ref>")
		os.Exit(2)
	}
	if err := run(*add, *remove, *options); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(add, remove, options string) error {
	var result configedit.Result
	var err error

	if remove != "" {
		result, err = configedit.DisablePlugin(remove)
	} else {
		var parsed map[string]any
		if parsed, err = configedit.ParseOptions(options); err == nil {
			result, err = configedit.EnablePlugin(add, parsed)
		}
	}

	// A commented config is refused, not mangled. Print the snippet to add by
	// hand and exit cleanly: the plugin's files are installed either way, so
	// failing the whole `make install-plugin` over a config style choice would
	// be the wrong call.
	var commented *configedit.CommentedError
	if errors.As(err, &commented) {
		fmt.Println(commented.Error())
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s: %s\n", result.Path, result.Summary)
	return nil
}
