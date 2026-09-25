package mcp

import (
	"github.com/langazov/gocode-go/internal/browser"
)

// openBrowser mirrors McpBrowser.Service.open() (wraps npm `open`): best
// effort, errors are not fatal to the auth flow (the URL is always also
// printed by the caller).
func openBrowser(url string) {
	_ = browser.Open(url)
}
