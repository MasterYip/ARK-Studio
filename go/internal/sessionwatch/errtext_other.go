//go:build !windows

package sessionwatch

import (
	"errors"
	"strconv"
	"syscall"
)

func socketErrorText(err error) (string, bool) {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return "[Errno " + strconv.Itoa(int(errno)) + "] " + strerror(int(errno)), true
	}
	return "", false
}

func dnsErrorText(notFound bool) string {
	if notFound {
		return "[Errno -2] Name or service not known"
	}
	return "[Errno -3] Temporary failure in name resolution"
}

func fileErrno(err error) (int, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0, false
	}
	return int(errno), true
}

const dirReadErrno = 21
