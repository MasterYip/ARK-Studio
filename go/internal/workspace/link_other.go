//go:build !windows

package workspace

import (
	"os"

	"golang.org/x/sys/unix"
)

// isLinkLike is _is_link_like.
func isLinkLike(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// readOnly is `not os.access(path, os.W_OK)`.
func readOnly(path string) bool {
	return unix.Access(path, unix.W_OK) != nil
}
