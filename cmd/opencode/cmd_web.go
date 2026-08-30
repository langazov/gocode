package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"

	"github.com/anomalyco/opencode-go/internal/clix"
	"github.com/anomalyco/opencode-go/internal/server"
)

// webCommand mirrors WebCommand in cli/cmd/web.ts: start the server and open
// the web interface in a browser.
func webCommand() *clix.Command {
	flags := append([]clix.Flag{}, networkFlags()...)
	return &clix.Command{
		Name:     "web",
		Describe: "start opencode server and open web interface",
		Flags:    flags,
		Run:      runWebCommand,
	}
}

func runWebCommand(a *clix.Args) error {
	if os.Getenv("OPENCODE_SERVER_PASSWORD") == "" {
		fmt.Println("!  OPENCODE_SERVER_PASSWORD is not set; server is unsecured.")
	}
	addr := networkAddr(a)
	stack, err := bootStack(context.Background(), "")
	if err != nil {
		return err
	}
	listener := listenAddr(addr)
	srv := &server.Server{Session: stack.Service, Bus: stack.Bus, Permissions: stack.Permissions, Models: stack.Models, Agents: stack.Agents, Config: stack.Config, MCP: stack.MCP}
	go server.ServeOn(listener, srv.Mux())

	host, port, _ := net.SplitHostPort(listener.Addr().String())
	if host == "0.0.0.0" {
		localURL := fmt.Sprintf("http://localhost:%s", port)
		fmt.Println("  Local access:      ", localURL)
		for _, ip := range networkIPs() {
			fmt.Printf("  Network access:     http://%s:%s\n", ip, port)
		}
		if a.Bool("mdns") {
			fmt.Printf("  mDNS:               %s:%s\n", a.String("mdns-domain"), port)
		}
		openBrowser(localURL)
	} else {
		url := fmt.Sprintf("http://%s:%s", host, port)
		fmt.Println("  Web interface:     ", url)
		openBrowser(url)
	}

	select {}
}

func networkIPs() []string {
	var out []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		out = append(out, ipNet.IP.String())
	}
	return out
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
