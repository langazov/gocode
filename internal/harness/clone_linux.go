//go:build linux

package harness

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// cloneTree has no Linux equivalent of clonefile(2) for a directory; the
// copy goes file by file, each reflinked where the filesystem can.
func cloneTree(string, string) error { return errors.ErrUnsupported }

// cloneFile reflinks one file (FICLONE) on filesystems that share extents
// — btrfs, XFS, bcachefs — so the copy costs nothing until it is written.
func cloneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}
