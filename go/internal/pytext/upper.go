package pytext

import "strings"

// Upper is str.upper() with CPython's full case mappings (upperTable is
// generated from the interpreter by tests/python/contract/gen_skillkit.py).
func Upper(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToUpper(s)
	}
	var b strings.Builder
	for _, r := range s {
		if u, ok := upperTable[r]; ok {
			b.WriteString(u)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
