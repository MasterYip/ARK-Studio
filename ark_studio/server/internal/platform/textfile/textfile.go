// Package textfile reproduces how Python reads and writes text files:
// open("w") turns "\n" into os.linesep, and read_text() applies universal
// newlines (CRLF and lone CR become LF).
package textfile

import (
	"runtime"
	"strings"
)

// Encode is how Path.write_text / open("w") encode text: "\n" becomes
// os.linesep, which is "\r\n" on Windows.
func Encode(s string) []byte {
	if runtime.GOOS == "windows" {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return []byte(s)
}

// Universal is universal-newline decoding: CRLF and lone CR become LF.
func Universal(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}
