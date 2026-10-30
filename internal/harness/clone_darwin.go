//go:build darwin

package harness

import "golang.org/x/sys/unix"

// cloneTree clones a whole directory tree copy-on-write with clonefile(2),
// which APFS does in constant time: a rollout's workspace copy of a large
// repository costs nothing until the rollout writes. dst must not exist.
func cloneTree(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}

// cloneFile clones one regular file the same way.
func cloneFile(src, dst string) error {
	return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
}
