//go:build !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !linux

package session

import "context"

// watchLinkChanges has no implementation on this platform (Windows would need
// NotifyIpInterfaceChange; js/wasm has no interface table to watch), so the
// wait falls back to polling linkUp — a second of latency at worst, on the
// path that only shortens a wait it was already going to sit through.
func watchLinkChanges(context.Context) <-chan struct{} { return nil }
