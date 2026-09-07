package session

import (
	"context"
	"errors"
	"net"
	"time"
)

// The machine's own link state, used to shorten a wait rather than to decide
// one.
//
// net.Interfaces is a syscall against the kernel's interface table — no
// packets, no DNS, no cgo, a few microseconds — so it is cheap enough to poll
// every second while a turn is held. What it reports is narrow but exact: this
// machine has, or does not have, an interface capable of carrying traffic off
// the box. Wi-Fi dropped, the cable unplugged, the VPN torn down, the laptop
// asleep — those it sees within a poll.
//
// What it cannot see is everything past the first hop: a captive portal
// answers every packet, a dead upstream router leaves the interface up, and a
// provider that is down looks identical to one that is fine. So link state
// never decides that the network is back — only the request can do that (see
// netretry.go). It decides when it is worth *trying* again: the transition
// from no usable interface to one, which is the moment a laptop rejoining
// Wi-Fi becomes worth a retry, instead of sitting out the rest of a 30-second
// backoff for nothing.
//
// linkIsUp is a variable so the wait loop can be tested without a network
// interface to unplug.
var linkIsUp = linkUp

// linkPollIntervalVar is how often a held turn re-reads the interface table.
// A variable only so a test need not spend a second per poll.
var linkPollIntervalVar = time.Second

// linkUp reports whether the machine has at least one interface that could
// carry traffic: up, running, not loopback, and holding an address that is
// routable off the machine.
//
// It answers true when it cannot tell. A failure to read the interface table
// is not evidence of an outage, and the caller's fallback for "up" is simply
// to wait out its timer — the safe direction to be wrong in.
func linkUp() bool {
	interfaces, err := net.Interfaces()
	if err != nil {
		return true
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		// Up is the administrative state ("enabled"); running is the
		// operational one ("has carrier"). An unplugged ethernet port stays
		// up and stops running, so both are required.
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagRunning == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			// A link-local address (169.254.0.0/16, fe80::/10) is what an
			// interface self-assigns when DHCP found nobody: the interface is
			// alive and the network behind it is not.
			ip := ipNet.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			return true
		}
	}
	return false
}

// errLinkDown is the cause a stream is cancelled with when the machine loses
// its network while the stream is running. It is not context.Canceled by
// design: that is the user's interrupt, and the two must never be confused —
// one settles the turn as aborted, the other holds it for a retry.
var errLinkDown = errors.New("session: network link went down")

// linkLossBackstopInterval is how often watchLinkLoss re-reads the interface
// table when it also has kernel notifications. Those do the real work; this
// only covers a message the kernel dropped or a watcher that died quietly.
const linkLossBackstopInterval = 10 * time.Second

// watchLinkLoss calls onLoss the first time the machine has no usable network
// interface, and returns.
//
// It exists because a stream whose link disappears does not fail — it stalls.
// The socket's peer is unreachable, no RST ever arrives, and the read sits
// there until some timeout fires minutes later, which from the outside looks
// like a model that stopped mid-sentence. Watching the interface table turns
// those minutes into the moment the interface went away.
//
// The caller gets no stop function: ctx ends the watch, and the goroutine is
// bounded by the stream it was started for.
func watchLinkLoss(ctx context.Context, onLoss func()) {
	go func() {
		changes := watchLink(ctx)
		interval := linkPollIntervalVar
		if changes != nil {
			interval = linkLossBackstopInterval
		}
		poll := time.NewTicker(interval)
		defer poll.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-changes:
			case <-poll.C:
			}
			if !linkIsUp() {
				onLoss()
				return
			}
		}
	}()
}
