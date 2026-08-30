package mcp

import (
	"os/exec"
	"runtime"
)

// openBrowser mirrors McpBrowser.Service.open() (wraps npm `open`): best
// effort, errors are not fatal to the auth flow (the URL is always also
// printed by the caller).
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
