//go:build linux

package session

import (
	"context"
	"os"

	"golang.org/x/sys/unix"
)

// watchLinkChanges subscribes to the rtnetlink multicast groups that announce
// interface and address changes. See the BSD implementation for why the
// messages themselves are ignored: this socket is a wake-up source, and
// linkUp() remains the single definition of a usable link.
//
// The groups are readable unprivileged; a failure to open or bind leaves the
// caller polling.
func watchLinkChanges(ctx context.Context) <-chan struct{} {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return nil
	}
	err = unix.Bind(fd, &unix.SockaddrNetlink{
		Family: unix.AF_NETLINK,
		Groups: unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR,
	})
	if err != nil {
		unix.Close(fd)
		return nil
	}
	// Non-blocking, so os.File registers it with the runtime poller and the
	// Close below can interrupt a goroutine parked in Read.
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil
	}
	return forwardLinkChanges(ctx, os.NewFile(uintptr(fd), "netlink"))
}
