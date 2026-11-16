package server

import "strings"

// universalNewlines is what Path.read_text() does on every platform: CRLF
// and lone CR become LF before a text asset is transformed.  A Windows
// checkout with core.autocrlf stores web/*.js as CRLF; Python serves the
// rewritten HTML/JS with LF but untransformed files (CSS) byte for byte.
func universalNewlines(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}
