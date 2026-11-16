//go:build !windows

package chat

import (
	"errors"
	"syscall"
)

// osErrorText is str(OSError) of a socket error: "[Errno N] text".
func osErrorText(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return "[Errno " + itoa(int(errno)) + "] " + errno.Error()
	}
	return err.Error()
}
