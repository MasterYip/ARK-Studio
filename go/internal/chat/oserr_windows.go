package chat

import (
	"errors"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// osErrorText is str(OSError) of a Windows socket error: "[WinError N]"
// plus FormatMessage in the user's language (LANG_NEUTRAL), trailing
// whitespace and dots removed, as CPython builds it.
func osErrorText(err error) string {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return err.Error()
	}
	buf := make([]uint16, 512)
	n, ferr := windows.FormatMessage(windows.FORMAT_MESSAGE_FROM_SYSTEM|windows.FORMAT_MESSAGE_IGNORE_INSERTS,
		0, uint32(errno), 0, buf, nil)
	msg := ""
	if ferr == nil {
		msg = strings.TrimRight(string(utf16.Decode(buf[:n])), " \t\r\n.")
	}
	return "[WinError " + itoa(int(errno)) + "] " + msg
}
