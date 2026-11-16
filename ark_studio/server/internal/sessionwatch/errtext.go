package sessionwatch

import (
	"syscall"
	"unicode"
	"unicode/utf8"
)

// crtStrerror covers the errnos open()/read() realistically produce; the
// texts are the C library's (MSVCRT and glibc agree on these).
var crtStrerror = map[int]string{
	2:  "No such file or directory",
	9:  "Bad file descriptor",
	12: "Cannot allocate memory",
	13: "Permission denied",
	20: "Not a directory",
	21: "Is a directory",
	22: "Invalid argument",
	24: "Too many open files",
	36: "File name too long",
	40: "Too many levels of symbolic links",
}

// strerror is C strerror(errno): the table above, else Go's text with the
// first letter capitalised (Go's Unix tables are glibc's in lower case).
func strerror(errno int) string {
	if onWindows && errno == 12 {
		return "Not enough space"
	}
	if s, ok := crtStrerror[errno]; ok {
		return s
	}
	s := syscall.Errno(errno).Error()
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}
