package pyjson

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// ErrSyntax is returned for anything Python's json.loads would reject.
var ErrSyntax = errors.New("pyjson: invalid JSON")

// Loads is json.loads(text) for a str: NaN/Infinity literals are accepted,
// control characters inside strings are rejected (strict=True), duplicate keys
// keep the first position and the last value, integers are exact.
func Loads(text string) (any, error) {
	d := decoder{s: text}
	d.ws()
	v, err := d.value()
	if err != nil {
		return nil, err
	}
	d.ws()
	if d.i != len(d.s) {
		return nil, ErrSyntax
	}
	return v, nil
}

// LoadsObject parses text and returns it only when it is an object.
func LoadsObject(text string) (*Object, bool) {
	v, err := Loads(text)
	if err != nil {
		return nil, false
	}
	o, ok := v.(*Object)
	return o, ok
}

type decoder struct {
	s string
	i int
}

func (d *decoder) ws() {
	for d.i < len(d.s) {
		switch d.s[d.i] {
		case ' ', '\t', '\n', '\r':
			d.i++
		default:
			return
		}
	}
}

func (d *decoder) lit(word string) bool {
	if strings.HasPrefix(d.s[d.i:], word) {
		d.i += len(word)
		return true
	}
	return false
}

func (d *decoder) value() (any, error) {
	if d.i >= len(d.s) {
		return nil, ErrSyntax
	}
	switch c := d.s[d.i]; {
	case c == '{':
		return d.object()
	case c == '[':
		return d.array()
	case c == '"':
		return d.str()
	case d.lit("null"):
		return nil, nil
	case d.lit("true"):
		return true, nil
	case d.lit("false"):
		return false, nil
	case d.lit("NaN"):
		return math.NaN(), nil
	case d.lit("Infinity"):
		return math.Inf(1), nil
	case d.lit("-Infinity"):
		return math.Inf(-1), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return d.number()
	}
	return nil, ErrSyntax
}

func (d *decoder) object() (any, error) {
	d.i++ // {
	o := NewObject()
	d.ws()
	if d.i < len(d.s) && d.s[d.i] == '}' {
		d.i++
		return o, nil
	}
	for {
		d.ws()
		if d.i >= len(d.s) || d.s[d.i] != '"' {
			return nil, ErrSyntax
		}
		k, err := d.str()
		if err != nil {
			return nil, err
		}
		d.ws()
		if d.i >= len(d.s) || d.s[d.i] != ':' {
			return nil, ErrSyntax
		}
		d.i++
		d.ws()
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		o.Set(k.(string), v)
		d.ws()
		if d.i >= len(d.s) {
			return nil, ErrSyntax
		}
		if d.s[d.i] == ',' {
			d.i++
			continue
		}
		if d.s[d.i] == '}' {
			d.i++
			return o, nil
		}
		return nil, ErrSyntax
	}
}

func (d *decoder) array() (any, error) {
	d.i++ // [
	out := []any{}
	d.ws()
	if d.i < len(d.s) && d.s[d.i] == ']' {
		d.i++
		return out, nil
	}
	for {
		d.ws()
		v, err := d.value()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		d.ws()
		if d.i >= len(d.s) {
			return nil, ErrSyntax
		}
		if d.s[d.i] == ',' {
			d.i++
			continue
		}
		if d.s[d.i] == ']' {
			d.i++
			return out, nil
		}
		return nil, ErrSyntax
	}
}

func hex4(s string) (rune, bool) {
	if len(s) < 4 {
		return 0, false
	}
	n, err := strconv.ParseUint(s[:4], 16, 32)
	return rune(n), err == nil
}

func (d *decoder) str() (any, error) {
	d.i++ // "
	var b strings.Builder
	for d.i < len(d.s) {
		c := d.s[d.i]
		switch {
		case c == '"':
			d.i++
			return b.String(), nil
		case c < 0x20:
			return nil, ErrSyntax
		case c == '\\':
			if d.i+1 >= len(d.s) {
				return nil, ErrSyntax
			}
			e := d.s[d.i+1]
			d.i += 2
			switch e {
			case '"', '\\', '/':
				b.WriteByte(e)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				r, ok := hex4(d.s[d.i:])
				if !ok {
					return nil, ErrSyntax
				}
				d.i += 4
				if utf16.IsSurrogate(r) && r < 0xdc00 && strings.HasPrefix(d.s[d.i:], "\\u") {
					if r2, ok := hex4(d.s[d.i+2:]); ok && r2 >= 0xdc00 && r2 <= 0xdfff {
						r = utf16.DecodeRune(r, r2)
						d.i += 6
					}
				}
				// A lone surrogate is legal in a Python str but not in UTF-8.
				b.WriteRune(r)
			default:
				return nil, ErrSyntax
			}
		default:
			r, size := utf8.DecodeRuneInString(d.s[d.i:])
			b.WriteRune(r)
			d.i += size
		}
	}
	return nil, ErrSyntax
}

func (d *decoder) number() (any, error) {
	start := d.i
	if d.s[d.i] == '-' {
		d.i++
	}
	digits := func() int {
		n := 0
		for d.i < len(d.s) && d.s[d.i] >= '0' && d.s[d.i] <= '9' {
			d.i++
			n++
		}
		return n
	}
	if d.i < len(d.s) && d.s[d.i] == '0' {
		d.i++
	} else if digits() == 0 {
		return nil, ErrSyntax
	}
	isFloat := false
	if d.i+1 < len(d.s) && d.s[d.i] == '.' && d.s[d.i+1] >= '0' && d.s[d.i+1] <= '9' {
		d.i++
		digits()
		isFloat = true
	}
	if d.i < len(d.s) && (d.s[d.i] == 'e' || d.s[d.i] == 'E') {
		save := d.i
		d.i++
		if d.i < len(d.s) && (d.s[d.i] == '+' || d.s[d.i] == '-') {
			d.i++
		}
		if digits() == 0 {
			d.i = save // Python's NUMBER_RE leaves a bare "e" unconsumed
		} else {
			isFloat = true
		}
	}
	text := d.s[start:d.i]
	if isFloat {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, ErrSyntax
		}
		return f, nil
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return n, nil
	}
	return BigInt(text), nil
}
