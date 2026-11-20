//go:build !linux && !windows

package dirsize

import (
	"os"
	"syscall"
)

func device(fi os.FileInfo) (uint64, bool) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), true
	}
	return 0, false
}

func lowerThreadPriority() {}

// IsNetworkPath is not detected on this OS.
func IsNetworkPath(string) bool { return false }
