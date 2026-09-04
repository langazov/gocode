// Command mdlsp is a Language Server Protocol server for markdown documents.
//
// It speaks LSP 3.17 over stdio and works with any editor that speaks the
// protocol: VS Code, Neovim, Helix, Zed. Features:
//
//   - document symbols (the heading outline, nested by level)
//   - folding ranges (heading sections, code blocks, frontmatter)
//   - go-to-definition for [text](#anchor), [text](file.md#anchor) and
//     [[Wiki Link]] references
//   - find-references for headings across the workspace
//   - rename for headings, rewriting every inbound anchor
//   - completion for heading anchors, wiki names and workspace file paths
//   - clickable document links
//   - diagnostics for broken anchors and links to missing files
//   - formatting (trailing whitespace, blank-line runs, final newline)
//   - workspace-wide symbol search over headings
//
// Logs go to stderr only — stdout is the protocol channel. Run with -debug
// for request logging.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/langazov/gocode-go/internal/mdlsp"
)

var version = "dev"

func main() {
	debug := flag.Bool("debug", false, "log protocol activity to stderr")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("mdlsp", version)
		return
	}

	if *debug {
		fmt.Fprintln(os.Stderr, "mdlsp: starting on stdio")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	server := mdlsp.New(os.Stdin, os.Stdout)
	if err := server.Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "mdlsp:", err)
		os.Exit(1)
	}
}
