package main

import (
	"context"
	"fmt"
	"os"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/server"
)

// serveCommand mirrors ServeCommand in cli/cmd/serve.ts.
func serveCommand() *clix.Command {
	flags := append([]clix.Flag{}, networkFlags()...)
	flags = append(flags, clix.Flag{Name: "model", Kind: clix.KindString, Describe: "default model (provider/model); overrides the config \"model\" value"})
	return &clix.Command{
		Name:     "serve",
		Describe: "starts a headless opencode server",
		Flags:    flags,
		Run:      runServeCommand,
	}
}

func runServeCommand(a *clix.Args) error {
	if os.Getenv("OPENCODE_SERVER_PASSWORD") == "" {
		fmt.Println("Warning: OPENCODE_SERVER_PASSWORD is not set; server is unsecured.")
	}
	addr := networkAddr(a)
	stack, err := bootStack(context.Background(), a.String("model"))
	if err != nil {
		return err
	}
	listener := listenAddr(addr)
	fmt.Printf("opencode server listening on http://%s\n", listener.Addr().String())
	srv := &server.Server{
		Session:     stack.Service,
		Bus:         stack.Bus,
		Permissions: stack.Permissions,
		Models:      stack.Models,
		Agents:      stack.Agents,
		Config:      stack.Config,
		MCP:         stack.MCP,
	}
	return server.ServeOn(listener, srv.Mux())
}

// networkAddr resolves --hostname/--port (and --mdns's hostname default) the
// way resolveNetworkOptionsNoConfig() does in cli/network.ts. mDNS
// advertisement and CORS are accepted for flag-surface parity but are not
// wired into the Go server yet (see specs/go-port-gaps.md).
func networkAddr(a *clix.Args) string {
	hostname := a.String("hostname")
	if hostname == "" {
		hostname = "127.0.0.1"
	}
	if a.Bool("mdns") && !a.Has("hostname") {
		hostname = "0.0.0.0"
	}
	port := a.IntOr("port", 0)
	return fmt.Sprintf("%s:%d", hostname, port)
}
