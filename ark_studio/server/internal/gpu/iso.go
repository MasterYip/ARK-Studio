package gpu

import (
	"strconv"
	"strings"
	"time"
	"unicode"
)

// fromISO is CPython 3.10's C datetime.fromisoformat: YYYY-MM-DD, any one
// separator character, HH[:MM[:SS[.fff[fff]]]] and an optional
// +HH:MM[:SS[.ffffff]] offset.  aware reports whether an offset was given.
func fromISO(s string) (t time.Time, aware bool, ok bool) {
	b := []byte(s)
	digits := func(p []byte, n int) (int, bool) {
		if len(p) < n {
			return 0, false
		}
		v := 0
		for i := 0; i < n; i++ {
			if p[i] < '0' || p[i] > '9' {
				return 0, false
			}
			v = v*10 + int(p[i]-'0')
		}
		return v, true
	}
	year, ok1 := digits(b, 4)
	if !ok1 || len(b) < 5 || b[4] != '-' {
		return t, false, false
	}
	month, ok2 := digits(b[5:], 2)
	if !ok2 || len(b) < 8 || b[7] != '-' {
		return t, false, false
	}
	day, ok3 := digits(b[8:], 2)
	if !ok3 {
		return t, false, false
	}
	hour, minute, second, micro := 0, 0, 0, 0
	offset, offMicro := 0, 0
	if len(b) > 10 {
		p := 11
		switch {
		case b[10]&0x80 == 0:
		case b[10]&0xf0 == 0xe0:
			p = 13
		case b[10]&0xf0 == 0xf0:
			p = 14
		default:
			p = 12
		}
		if p > len(b) {
			return t, false, false
		}
		rest := b[p:]
		tz := 0
		for tz = 0; tz < len(rest); tz++ {
			if rest[tz] == '+' || rest[tz] == '-' {
				break
			}
		}
		if len(rest) == 0 {
			return t, false, false
		}
		var rc int
		if hour, minute, second, micro, rc = hhmmssff(rest, tz); rc < 0 {
			return t, false, false
		}
		if tz < len(rest) {
			tzlen := len(rest) - tz
			if tzlen != 6 && tzlen != 9 && tzlen != 16 {
				return t, false, false
			}
			sign := 1
			if rest[tz] == '-' {
				sign = -1
			}
			th, tm, ts, tu, rc2 := hhmmssff(rest[tz+1:], len(rest)-tz-1)
			if rc2 != 0 {
				return t, false, false
			}
			offset = sign * (th*3600 + tm*60 + ts)
			offMicro = sign * tu
			aware = true
		}
	}
	if year < 1 || month < 1 || month > 12 || day < 1 || day > daysIn(year, month) ||
		hour > 23 || minute > 59 || second > 59 {
		return t, false, false
	}
	if aware {
		total := time.Duration(offset)*time.Second + time.Duration(offMicro)*time.Microsecond
		if total <= -24*time.Hour || total >= 24*time.Hour {
			return t, false, false
		}
		t = time.Date(year, time.Month(month), day, hour, minute, second, micro*1000, time.UTC).Add(-total)
		return t, true, true
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, micro*1000, time.UTC), false, true
}

// hhmmssff is parse_hh_mm_ss_ff over p[:end]: 0 at the end, 1 when more
// text follows (an offset), negative when malformed.
func hhmmssff(p []byte, end int) (h, m, s, us, rc int) {
	vals := [3]*int{&h, &m, &s}
	i := 0
	at := func(k int) byte {
		if k < len(p) {
			return p[k]
		}
		return 0
	}
	for n := 0; n < 3; n++ {
		if i+2 > len(p) || at(i) < '0' || at(i) > '9' || at(i+1) < '0' || at(i+1) > '9' {
			return 0, 0, 0, 0, -3
		}
		*vals[n] = int(p[i]-'0')*10 + int(p[i+1]-'0')
		i += 2
		c := at(i)
		i++
		if i >= end {
			if c != 0 {
				return h, m, s, us, 1
			}
			return h, m, s, us, 0
		}
		if c == ':' {
			continue
		}
		if c == '.' {
			break
		}
		return 0, 0, 0, 0, -4
	}
	remain := end - i
	if remain != 6 && remain != 3 {
		return 0, 0, 0, 0, -3
	}
	for k := 0; k < remain; k++ {
		c := at(i + k)
		if c < '0' || c > '9' {
			return 0, 0, 0, 0, -3
		}
		us = us*10 + int(c-'0')
	}
	if remain == 3 {
		us *= 1000
	}
	if at(i+remain) != 0 {
		return h, m, s, us, 1
	}
	return h, m, s, us, 0
}

func daysIn(year, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// parseISO is datetime.fromisoformat(text) when the result is aware.
func parseISO(text string) (time.Time, bool) {
	t, aware, ok := fromISO(text)
	return t, ok && aware
}

// sinceStamp is the since filter of GpuQueue.list: Z -> +00:00, a 1..6
// digit fraction padded to 6, fromisoformat, aware only, UTC isoformat().
func sinceStamp(since string) (string, bool) {
	text := strings.ReplaceAll(since, "Z", "+00:00")
	text = padFraction(text)
	t, ok := parseISO(text)
	if !ok {
		return "", false
	}
	u := t.UTC()
	out := u.Format("2006-01-02T15:04:05")
	if u.Year() < 1000 {
		out = strconv.Itoa(10000 + u.Year())[1:] + out[strings.Index(out, "-"):]
	}
	if us := u.Nanosecond() / 1000; us != 0 {
		out += "." + strconv.Itoa(1000000 + us)[1:]
	}
	return out + "+00:00", true
}

// padFraction is re.sub(r"\.(\d{1,6})(?=[+-]\d{2}:\d{2}$)", pad, text):
// Python's \d is Unicode and $ also matches before a final newline.
func padFraction(text string) string {
	runes := []rune(text)
	n := len(runes)
	isDigit := func(r rune) bool { return r >= '0' && r <= '9' || unicodeDigit(r) }
	tzAt := func(k int) bool { // [+-]\d{2}:\d{2}$ at k
		if k+6 > n || (runes[k] != '+' && runes[k] != '-') || !isDigit(runes[k+1]) || !isDigit(runes[k+2]) ||
			runes[k+3] != ':' || !isDigit(runes[k+4]) || !isDigit(runes[k+5]) {
			return false
		}
		return k+6 == n || (k+7 == n && runes[n-1] == '\n')
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if runes[i] == '.' {
			j := i + 1
			for j < n && j-i-1 < 6 && isDigit(runes[j]) {
				j++
			}
			// greedy {1,6} with backtracking to satisfy the lookahead
			for k := j; k > i+1; k-- {
				if tzAt(k) {
					digits := string(runes[i+1 : k])
					b.WriteString("." + digits + strings.Repeat("0", max(0, 6-(k-i-1))))
					i = k - 1
					goto next
				}
			}
		}
		b.WriteRune(runes[i])
	next:
	}
	return b.String()
}

func unicodeDigit(r rune) bool { return r > 127 && unicode.Is(unicode.Nd, r) }
