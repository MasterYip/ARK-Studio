package pyjson

import (
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Dumps is json.dumps(v, ensure_ascii=False).
func Dumps(v any) []byte {
	var b []byte
	return appendValue(b, v, -1, 0)
}

// DumpsIndent is json.dumps(v, ensure_ascii=False, indent=n).
func DumpsIndent(v any, indent int) []byte {
	var b []byte
	return appendValue(b, v, indent, 0)
}

// Append writes json.dumps(v, ensure_ascii=False) to b.
func Append(b []byte, v any) []byte { return appendValue(b, v, -1, 0) }

func newline(b []byte, indent, level int) []byte {
	b = append(b, '\n')
	for i := 0; i < indent*level; i++ {
		b = append(b, ' ')
	}
	return b
}

func appendValue(b []byte, v any, indent, level int) []byte {
	switch x := v.(type) {
	case nil:
		return append(b, "null"...)
	case bool:
		if x {
			return append(b, "true"...)
		}
		return append(b, "false"...)
	case int:
		return strconv.AppendInt(b, int64(x), 10)
	case int64:
		return strconv.AppendInt(b, x, 10)
	case *big.Int:
		return append(b, x.String()...)
	case float64:
		return append(b, FloatRepr(x, true)...)
	case string:
		return AppendString(b, x)
	case Raw:
		return append(b, x...)
	case *Object:
		if x == nil {
			return append(b, "null"...)
		}
		if x.Len() == 0 {
			return append(b, "{}"...)
		}
		b = append(b, '{')
		for i, k := range x.keys {
			if i > 0 {
				b = append(b, ',')
				if indent < 0 {
					b = append(b, ' ')
				}
			}
			if indent >= 0 {
				b = newline(b, indent, level+1)
			}
			b = AppendString(b, k)
			b = append(b, ':', ' ')
			b = appendValue(b, x.vals[i], indent, level+1)
		}
		if indent >= 0 {
			b = newline(b, indent, level)
		}
		return append(b, '}')
	case []any:
		return appendList(b, len(x), func(b []byte, i int) []byte { return appendValue(b, x[i], indent, level+1) }, indent, level)
	case []*Object:
		return appendList(b, len(x), func(b []byte, i int) []byte { return appendValue(b, x[i], indent, level+1) }, indent, level)
	case []string:
		return appendList(b, len(x), func(b []byte, i int) []byte { return AppendString(b, x[i]) }, indent, level)
	case []int64:
		return appendList(b, len(x), func(b []byte, i int) []byte { return strconv.AppendInt(b, x[i], 10) }, indent, level)
	case map[string]int64:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := NewObject()
		for _, k := range keys {
			o.Set(k, x[k])
		}
		return appendValue(b, o, indent, level)
	}
	panic(fmt.Sprintf("pyjson: cannot encode %T", v))
}

func appendList(b []byte, n int, item func([]byte, int) []byte, indent, level int) []byte {
	if n == 0 {
		return append(b, "[]"...)
	}
	b = append(b, '[')
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
			if indent < 0 {
				b = append(b, ' ')
			}
		}
		if indent >= 0 {
			b = newline(b, indent, level+1)
		}
		b = item(b, i)
	}
	if indent >= 0 {
		b = newline(b, indent, level)
	}
	return append(b, ']')
}

const hexDigits = "0123456789abcdef"

// AppendString writes a JSON string the way Python does with ensure_ascii=False:
// only '"', '\\' and C0 control characters are escaped; everything else,
// including U+2028 and astral characters, is raw UTF-8.  Invalid UTF-8 bytes
// (impossible in a Python str) become U+FFFD.
func AppendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch c {
			case '"':
				b = append(b, '\\', '"')
			case '\\':
				b = append(b, '\\', '\\')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			case '\b':
				b = append(b, '\\', 'b')
			case '\f':
				b = append(b, '\\', 'f')
			default:
				if c < 0x20 {
					b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
				} else {
					b = append(b, c)
				}
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, "�"...)
		} else {
			b = append(b, s[i:i+size]...)
		}
		i += size
	}
	return append(b, '"')
}

// FloatRepr is Python's repr(float).  With jsonNames, non-finite values use
// json.dumps spellings (NaN, Infinity, -Infinity) instead of nan/inf.
func FloatRepr(f float64, jsonNames bool) string {
	switch {
	case math.IsNaN(f):
		if jsonNames {
			return "NaN"
		}
		return "nan"
	case math.IsInf(f, 1):
		if jsonNames {
			return "Infinity"
		}
		return "inf"
	case math.IsInf(f, -1):
		if jsonNames {
			return "-Infinity"
		}
		return "-inf"
	}
	// Shortest round-trip digits, the same digits Python's repr chooses.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	neg := strings.HasPrefix(e, "-")
	if neg {
		e = e[1:]
	}
	mant, expText, _ := strings.Cut(e, "e")
	exp, _ := strconv.Atoi(expText)
	digits := strings.Replace(mant, ".", "", 1)
	decpt := exp + 1 // value = 0.DIGITS * 10**decpt
	var out string
	if decpt <= -4 || decpt > 16 {
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		sign := "+"
		if exp < 0 {
			sign = "-"
			exp = -exp
		}
		out = fmt.Sprintf("%se%s%02d", m, sign, exp)
	} else if decpt <= 0 {
		out = "0." + strings.Repeat("0", -decpt) + digits
	} else if decpt >= len(digits) {
		out = digits + strings.Repeat("0", decpt-len(digits)) + ".0"
	} else {
		out = digits[:decpt] + "." + digits[decpt:]
	}
	if neg {
		return "-" + out
	}
	return out
}

// DumpsASCII is json.dumps(v) with the default ensure_ascii=True: every
// non-ASCII character becomes a \uXXXX escape (surrogate pairs above U+FFFF).
func DumpsASCII(v any) []byte {
	raw := Dumps(v)
	out := make([]byte, 0, len(raw))
	const hexdigits = "0123456789abcdef"
	esc := func(r rune) {
		out = append(out, 0x5c, 'u', hexdigits[r>>12&0xf], hexdigits[r>>8&0xf], hexdigits[r>>4&0xf], hexdigits[r&0xf])
	}
	for _, r := range string(raw) {
		switch {
		case r < 0x80:
			out = append(out, byte(r))
		case r < 0x10000:
			esc(r)
		default:
			r -= 0x10000
			esc(0xd800 + (r >> 10))
			esc(0xdc00 + (r & 0x3ff))
		}
	}
	return out
}
