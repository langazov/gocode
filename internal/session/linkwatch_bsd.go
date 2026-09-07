//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package session

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
)

// watchLinkChanges subscribes to the kernel's routing socket, which announces
// every interface, address and route change on the machine — a Wi-Fi
// association, a DHCP lease, a VPN coming up, a cable going in.
//
// Nothing here parses the messages. A message means "the network
// configuration changed"; what the configuration now *is* comes from linkUp()
// reading the interface table, which is the same authority the poll uses. That
// keeps the platform-specific code to opening a socket and forwarding a
// wake-up, and leaves one definition of "usable link" for every platform.
//
// AF_ROUTE is readable unprivileged. Failure to open it is not an error worth
// reporting: the caller falls back to polling and loses nothing but a second.
func watchLinkChanges(ctx context.Context) <-chan struct{} {
	// No SOCK_CLOEXEC on this family here (macOS does not define it), so the
	// flag is set on the descriptor afterwards.
	fd, err := unix.Socket(unix.AF_ROUTE, unix.SOCK_RAW, unix.AF_UNSPEC)
	if err != nil {
		return nil
	}
	unix.CloseOnExec(fd)
	// Non-blocking, so os.File registers it with the runtime poller: that is
	// what lets the Close below interrupt a goroutine parked in Read, rather
	// than closing an fd out from under it.
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil
	}
	return forwardLinkChanges(ctx, os.NewFile(uintptr(fd), "route"))
}
