package pytext

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Float is float(text) for a str: Unicode whitespace and decimal digits,
// single underscores between digits, inf/infinity/nan (any case, signed).
// ok is false where CPython raises ValueError.
func Float(text string) (float64, bool) {
	fail := func() (float64, bool) { return 0, false }
	s := Strip(text)
	if s == "" {
		return fail()
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < 128:
			b.WriteRune(r)
		case unicode.Is(unicode.Nd, r):
			b.WriteRune('0' + ndValue(r))
		case IsSpace(r):
			b.WriteRune(' ')
		default:
			return fail()
		}
	}
	s = b.String()
	body := strings.TrimLeft(s, "+-")
	if len(s)-len(body) > 1 {
		return fail()
	}
	switch strings.ToLower(body) {
	case "inf", "infinity":
		if strings.HasPrefix(s, "-") {
			return math.Inf(-1), true
		}
		return math.Inf(1), true
	case "nan":
		return math.NaN(), true
	}
	if !decimalLiteral(body) {
		return fail()
	}
	clean := strings.ReplaceAll(s, "_", "")
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		var ne *strconv.NumError
		if errors.As(err, &ne) && ne.Err == strconv.ErrRange {
			return v, true
		}
		return fail()
	}
	return v, true
}

func ndValue(r rune) rune {
	// Unicode decimal digits come in contiguous runs of whole 0..9 blocks.
	start := r
	for start > 0 && unicode.Is(unicode.Nd, start-1) {
		start--
	}
	return (r - start) % 10
}

// decimalLiteral checks digits[.digits][e[+-]digits] with Python's
// underscore rule (only between two digits).
func decimalLiteral(s string) bool {
	i, n := 0, len(s)
	digits := func() int {
		start := i
		for i < n {
			if s[i] >= '0' && s[i] <= '9' {
				i++
			} else if s[i] == '_' && i > start && i+1 < n && s[i+1] >= '0' && s[i+1] <= '9' {
				i++
			} else {
				break
			}
		}
		return i - start
	}
	whole := digits()
	frac := 0
	if i < n && s[i] == '.' {
		i++
		frac = digits()
	}
	if whole == 0 && frac == 0 {
		return false
	}
	if i < n && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < n && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if digits() == 0 {
			return false
		}
	}
	return i == n
}
