// Package installation mirrors packages/core/src/installation/version.ts.
// Version and Channel are injected at build time via -ldflags.
package installation

var (
	Version = "local"
	Channel = "local"
)

func Local() bool {
	return Channel == "local"
}
