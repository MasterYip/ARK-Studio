//go:build windows

package sessionwatch

import (
	"errors"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// winMessage is FormatMessageW in the user's default language with trailing
// whitespace and dots removed, as CPython builds "[WinError N] ..." text.
func winMessage(code uint32) string {
	buf := make([]uint16, 512)
	flags := uint32(windows.FORMAT_MESSAGE_FROM_SYSTEM | windows.FORMAT_MESSAGE_IGNORE_INSERTS)
	n, err := windows.FormatMessage(flags, 0, code, 0, buf, nil)
	if err != nil || n == 0 {
		return "Unknown error"
	}
	s := syscall.UTF16ToString(buf[:n])
	return strings.TrimRightFunc(s, func(r rune) bool { return r <= ' ' || r == '.' })
}

// socketErrorText is str(OSError) for a socket-level failure.
func socketErrorText(err error) (string, bool) {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return "[WinError " + strconv.Itoa(int(errno)) + "] " + winMessage(uint32(errno)), true
	}
	return "", false
}

// dnsErrorText is str(socket.gaierror) for a failed lookup.
func dnsErrorText(notFound bool) string {
	if notFound {
		return "[Errno 11001] getaddrinfo failed"
	}
	return "[Errno 11002] getaddrinfo failed"
}

// fileErrno maps a Windows error to the CRT errno open() reports (_dosmaperr).
func fileErrno(err error) (int, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0, false
	}
	switch errno {
	case 2, 3, 15, 53, 67, 161, 206: // FILE/PATH_NOT_FOUND, INVALID_DRIVE, BAD_NETPATH, BAD_NET_NAME, BAD_PATHNAME, FILENAME_EXCED_RANGE
		return 2, true
	case 5, 16, 19, 32, 33, 65, 82, 1920: // ACCESS_DENIED, CURRENT_DIRECTORY, WRITE_PROTECT, SHARING/LOCK_VIOLATION, NETWORK_ACCESS_DENIED, CANNOT_MAKE, CANT_ACCESS_FILE
		return 13, true
	case 4:
		return 24, true
	case 8, 14:
		return 12, true
	case 6:
		return 9, true
	}
	return 22, true
}

// dirReadErrno is the errno CPython reports for open(<directory>, "rb").
const dirReadErrno = 13
