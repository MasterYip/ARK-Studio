package pyargparse

import (
	"fmt"
	"strings"
	"unicode"
)

// Repr is Python's repr() of a str.
func Repr(s string) string {
	quote := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		quote = '"'
	}
	var b strings.Builder
	b.WriteRune(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == quote:
			b.WriteByte('\\')
			b.WriteRune(quote)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case isPrintable(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteRune(quote)
	return b.String()
}

// isPrintable is str.isprintable for one code point: letters, marks,
// numbers, punctuation, symbols and the ASCII space (Cc, Cf, Cs, Co, Cn, Zl,
// Zp and the other Zs are not printable).  Go's Unicode tables may be newer
// than Python 3.10's (13.0) for code points assigned since.
func isPrintable(r rune) bool {
	if r == ' ' {
		return true
	}
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S)
}
