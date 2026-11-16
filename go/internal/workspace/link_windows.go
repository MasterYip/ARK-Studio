//go:build windows

package workspace

import (
	"os"
	"syscall"
)

// isLinkLike is _is_link_like: symlinks and junctions/reparse points.
func isLinkLike(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return fi.Mode()&os.ModeIrregular != 0
}

// readOnly is `not os.access(path, os.W_OK)`: the read-only attribute.
func readOnly(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return true
	}
	return fi.Mode().Perm()&0o200 == 0
}
