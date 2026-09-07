package session

import (
	"context"
	"os"
)

// watchLink is the hook the wait loop calls, indirected so a test can supply
// changes without an interface to unplug. The platform implementations are in
// the linkwatch_*.go files. Atomic for the same reason linkIsUpVar is: the
// watcher goroutine reads it as it starts, and a test may be restoring it
// while that goroutine winds down.
var watchLinkVar = hookPtr(watchLinkChanges)

func watchLink(ctx context.Context) <-chan struct{} { return (*watchLinkVar.Load())(ctx) }

// linkChangeBuffer is a message-sized read buffer. The contents are discarded
// — see the platform files for why — so it only has to be large enough that a
// single message rarely needs two reads.
const linkChangeBuffer = 4096

// forwardLinkChanges turns a kernel notification socket into a channel of
// wake-ups, and ties its lifetime to ctx.
//
// The channel has room for one pending wake-up and drops the rest: a Wi-Fi
// association emits a burst of messages, and the reader's response to all of
// them is the same single re-check of linkUp(). It is never closed on error —
// a reader selecting on a closed channel would spin — so a socket that dies
// simply goes quiet and leaves the caller on its poll.
func forwardLinkChanges(ctx context.Context, socket *os.File) <-chan struct{} {
	changes := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, linkChangeBuffer)
		for {
			if _, err := socket.Read(buf); err != nil {
				return // closed by the watchdog below, or unreadable
			}
			select {
			case changes <- struct{}{}:
			default:
			}
		}
	}()
	// Closing from here is what unblocks the Read above: the fd is
	// non-blocking and owned by os.File, so the runtime poller wakes the
	// reader instead of the fd being pulled out from under it.
	go func() {
		<-ctx.Done()
		socket.Close()
	}()
	return changes
}
