// Package pytext reproduces the Python str and re behavior ARK depends on.
//
// Traps it covers: len()/slicing count code points, not bytes; str.isspace()
// and re's \s include U+001C..U+001F, U+0085, NBSP, U+3000 and friends while
// Go's \s is ASCII only; re's \d and int() accept every Unicode decimal digit
// (e.g. fullwidth "１"); Python's `$` also matches before a trailing newline.
package pytext

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// IsSpace is str.isspace() for one code point.
func IsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// SpaceClass is the regexp character-class body for Python's \s on str.
const SpaceClass = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// Strip is str.strip().
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// RStrip is str.rstrip().
func RStrip(s string) string { return strings.TrimRightFunc(s, IsSpace) }

// LStrip is str.lstrip().
func LStrip(s string) string { return strings.TrimLeftFunc(s, IsSpace) }

// Len is len(s) in code points.
func Len(s string) int { return utf8.RuneCountInString(s) }

// Prefix is s[:n] in code points (n >= 0).
func Prefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// SliceFrom is s[n:] in code points.
func SliceFrom(s string, n int) string {
	i := 0
	for pos := range s {
		if i == n {
			return s[pos:]
		}
		i++
	}
	return ""
}

// IsDecimal is str.isdecimal() for a non-empty string (Unicode Nd).
func IsDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Nd, r) {
			return false
		}
	}
	return true
}

// digitOnly are code points where str.isdigit() is true but isdecimal() is
// false (superscripts, circled digits, ...).  int() rejects them.
var digitOnly = &unicode.RangeTable{R16: []unicode.Range16{
	{0x00b2, 0x00b3, 1}, {0x00b9, 0x00b9, 1}, {0x1369, 0x1371, 1}, {0x19da, 0x19da, 1},
	{0x2070, 0x2070, 1}, {0x2074, 0x2079, 1}, {0x2080, 0x2089, 1}, {0x2460, 0x2468, 1},
	{0x2474, 0x247c, 1}, {0x2488, 0x2490, 1}, {0x24ea, 0x24ea, 1}, {0x24f5, 0x24fd, 1},
	{0x24ff, 0x24ff, 1}, {0x2776, 0x277e, 1}, {0x2780, 0x2788, 1}, {0x278a, 0x2792, 1},
}}

// IsDigit is str.isdigit() for a non-empty string.
func IsDigit(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Nd, r) && !unicode.Is(digitOnly, r) {
			return false
		}
	}
	return true
}

// IntError mirrors CPython's ValueError text for int(s).
type IntError struct{ Text string }

func (e *IntError) Error() string {
	return "invalid literal for int() with base 10: " + reprStr(e.Text)
}

func reprStr(s string) string {
	// Enough of repr() for error messages: Python quotes with ' unless the
	// text contains ' and no ".
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	if q == "'" {
		s = strings.ReplaceAll(s, "'", `\'`)
	}
	return q + s + q
}

// Int is int(s) for a str: surrounding whitespace, a sign, Unicode decimal
// digits and single underscores between digits.
func Int(s string) (int64, error) {
	t := Strip(s)
	neg := false
	if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "+") {
		neg = t[0] == '-'
		t = t[1:]
	}
	var b strings.Builder
	prevDigit := false
	for _, r := range t {
		if r == '_' && prevDigit {
			prevDigit = false
			continue
		}
		if !unicode.Is(unicode.Nd, r) {
			return 0, &IntError{s}
		}
		b.WriteByte(byte('0' + digitValue(r)))
		prevDigit = true
	}
	if b.Len() == 0 || !prevDigit {
		return 0, &IntError{s}
	}
	n, err := strconv.ParseInt(b.String(), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("int out of range: %s", s)
	}
	if neg {
		n = -n
	}
	return n, nil
}

// digitValue of a Unicode Nd code point (every Nd block is a run of ten).
func digitValue(r rune) int {
	for _, rng := range unicode.Nd.R16 {
		if uint16(r) >= rng.Lo && uint16(r) <= rng.Hi && r <= 0xffff {
			return int(uint16(r)-rng.Lo) % 10
		}
	}
	for _, rng := range unicode.Nd.R32 {
		if uint32(r) >= rng.Lo && uint32(r) <= rng.Hi {
			return int(uint32(r)-rng.Lo) % 10
		}
	}
	return 0
}

// MustCompile compiles a pattern written with Python's \s and \d meaning:
// `\s` and `\d` outside character classes are rewritten to the Unicode
// classes.  Patterns must not use lookaround or backreferences (RE2).
func MustCompile(pattern string) *regexp.Regexp {
	pattern = strings.ReplaceAll(pattern, `\s`, "["+SpaceClass+"]")
	pattern = strings.ReplaceAll(pattern, `\d`, `\p{Nd}`)
	return regexp.MustCompile(pattern)
}

// Lower is str.lower(): Unicode lowercase with Python's full mappings for
// U+0130 (İ -> i + combining dot) and the final-sigma rule.
func Lower(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(s)
	}
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		switch r {
		case 0x130:
			b.WriteString("i̇")
		case 0x3A3:
			if finalSigma(rs, i) {
				b.WriteRune(0x3C2)
			} else {
				b.WriteRune(0x3C3)
			}
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

func isCased(r rune) bool { return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) }

func caseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) ||
		strings.ContainsRune("'.:^`·‘’․‧", r)
}

// finalSigma: preceded by a cased letter and not followed by one, skipping
// case-ignorable characters (Unicode Final_Sigma, as CPython applies it).
func finalSigma(rs []rune, i int) bool {
	j := i - 1
	for j >= 0 && caseIgnorable(rs[j]) {
		j--
	}
	if j < 0 || !isCased(rs[j]) {
		return false
	}
	k := i + 1
	for k < len(rs) && caseIgnorable(rs[k]) {
		k++
	}
	return k == len(rs) || !isCased(rs[k])
}

// SplitLines is str.splitlines(keepends).
func SplitLines(s string, keepends bool) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		end := i + size
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			if r == '\r' && end < len(s) && s[end] == '\n' {
				end++
			}
			if keepends {
				out = append(out, s[start:end])
			} else {
				out = append(out, s[start:i])
			}
			start = end
		}
		i = end
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
