package pyjson

import (
	"fmt"
	"strings"
)

// DecodeErrorMessage returns str(json.JSONDecodeError) that CPython's
// json.loads(text) raises (C scanner, strict=True), or "" when the text
// parses.  Positions count code points, as Python does.
func DecodeErrorMessage(text string) string {
	s := []rune(text)
	msg, pos := scanDocument(s)
	if msg == "" {
		return ""
	}
	line := 1
	last := -1
	for i := 0; i < pos && i < len(s); i++ {
		if s[i] == '\n' {
			line++
			last = i
		}
	}
	return fmt.Sprintf("%s: line %d column %d (char %d)", msg, line, pos-last, pos)
}

type stopIteration struct{ idx int }

type decodeErr struct {
	msg string
	pos int
}

func isWS(c rune) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func skipWS(s []rune, i int) int {
	for i < len(s) && isWS(s[i]) {
		i++
	}
	return i
}

func scanDocument(s []rune) (msg string, pos int) {
	defer func() {
		if r := recover(); r != nil {
			switch e := r.(type) {
			case stopIteration:
				msg, pos = "Expecting value", e.idx
			case decodeErr:
				msg, pos = e.msg, e.pos
			default:
				panic(r)
			}
		}
	}()
	end := scanOnce(s, skipWS(s, 0))
	end = skipWS(s, end)
	if end != len(s) {
		return "Extra data", end
	}
	return "", 0
}

func fail(msg string, pos int) { panic(decodeErr{msg, pos}) }

func hasAt(s []rune, i int, word string) bool {
	w := []rune(word)
	if i+len(w) > len(s) {
		return false
	}
	for k, c := range w {
		if s[i+k] != c {
			return false
		}
	}
	return true
}

func isDigit(c rune) bool { return c >= '0' && c <= '9' }

// scanOnce is scan_once_unicode: returns the index after the value.
func scanOnce(s []rune, idx int) int {
	n := len(s)
	if idx < 0 {
		panic(stopIteration{idx})
	}
	if idx >= n {
		panic(stopIteration{idx})
	}
	switch s[idx] {
	case '"':
		return scanString(s, idx+1)
	case '{':
		return parseObject(s, idx+1)
	case '[':
		return parseArray(s, idx+1)
	case 'n':
		if hasAt(s, idx, "null") {
			return idx + 4
		}
	case 't':
		if hasAt(s, idx, "true") {
			return idx + 4
		}
	case 'f':
		if hasAt(s, idx, "false") {
			return idx + 5
		}
	case 'N':
		if hasAt(s, idx, "NaN") {
			return idx + 3
		}
	case 'I':
		if hasAt(s, idx, "Infinity") {
			return idx + 8
		}
	case '-':
		if hasAt(s, idx, "-Infinity") {
			return idx + 9
		}
	}
	return matchNumber(s, idx)
}

func matchNumber(s []rune, start int) int {
	endIdx := len(s) - 1
	idx := start
	if s[idx] == '-' {
		idx++
		if idx > endIdx {
			panic(stopIteration{start})
		}
	}
	switch {
	case s[idx] >= '1' && s[idx] <= '9':
		idx++
		for idx <= endIdx && isDigit(s[idx]) {
			idx++
		}
	case s[idx] == '0':
		idx++
	default:
		panic(stopIteration{start})
	}
	if idx < endIdx && s[idx] == '.' && isDigit(s[idx+1]) {
		idx += 2
		for idx <= endIdx && isDigit(s[idx]) {
			idx++
		}
	}
	if idx < endIdx && (s[idx] == 'e' || s[idx] == 'E') {
		eStart := idx
		idx++
		if idx < endIdx && (s[idx] == '-' || s[idx] == '+') {
			idx++
		}
		for idx <= endIdx && isDigit(s[idx]) {
			idx++
		}
		if !isDigit(s[idx-1]) {
			idx = eStart
		}
	}
	return idx
}

func isHex(c rune) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// scanString is scanstring_unicode(pystr, end, strict=1).
func scanString(s []rune, end int) int {
	n := len(s)
	begin := end - 1
	for {
		var c rune
		next := end
		for ; next < n; next++ {
			c = s[next]
			if c == '"' || c == '\\' {
				break
			}
			if c <= 0x1f {
				fail("Invalid control character at", next)
			}
		}
		if next >= n || !(c == '"' || c == '\\') {
			fail("Unterminated string starting at", begin)
		}
		next++
		if c == '"' {
			return next
		}
		if next == n {
			fail("Unterminated string starting at", begin)
		}
		c = s[next]
		if c != 'u' {
			end = next + 1
			if !strings.ContainsRune(`"\/bfnrt`, c) {
				fail(`Invalid \escape`, end-2)
			}
			continue
		}
		next++
		end = next + 4
		if end >= n {
			fail(`Invalid \uXXXX escape`, next-1)
		}
		var v rune
		for ; next < end; next++ {
			if !isHex(s[next]) {
				fail(`Invalid \uXXXX escape`, end-5)
			}
			v = v*16 + hexVal(s[next])
		}
		if v >= 0xd800 && v <= 0xdbff && end+6 < n && s[next] == '\\' && s[next+1] == 'u' {
			end += 6
			for next += 2; next < end; next++ {
				if !isHex(s[next]) {
					fail(`Invalid \uXXXX escape`, end-5)
				}
			}
		}
	}
}

func hexVal(c rune) rune {
	switch {
	case isDigit(c):
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func parseObject(s []rune, idx int) int {
	endIdx := len(s) - 1
	idx = skipWS(s, idx)
	if idx > endIdx || s[idx] != '}' {
		for {
			if idx > endIdx || s[idx] != '"' {
				fail("Expecting property name enclosed in double quotes", idx)
			}
			idx = scanString(s, idx+1)
			idx = skipWS(s, idx)
			if idx > endIdx || s[idx] != ':' {
				fail("Expecting ':' delimiter", idx)
			}
			idx = skipWS(s, idx+1)
			idx = scanOnce(s, idx)
			idx = skipWS(s, idx)
			if idx <= endIdx && s[idx] == '}' {
				break
			}
			if idx > endIdx || s[idx] != ',' {
				fail("Expecting ',' delimiter", idx)
			}
			idx = skipWS(s, idx+1)
		}
	}
	return idx + 1
}

func parseArray(s []rune, idx int) int {
	endIdx := len(s) - 1
	idx = skipWS(s, idx)
	if idx > endIdx || s[idx] != ']' {
		for idx <= endIdx {
			idx = scanOnce(s, idx)
			idx = skipWS(s, idx)
			if idx <= endIdx && s[idx] == ']' {
				break
			}
			if idx > endIdx || s[idx] != ',' {
				fail("Expecting ',' delimiter", idx)
			}
			idx = skipWS(s, idx+1)
		}
		if idx > endIdx || s[idx] != ']' {
			fail("Expecting value", endIdx+1)
		}
	}
	return idx + 1
}
