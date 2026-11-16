package pyre

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"strconv"
	"strings"
	"sync"
)

// names.txt.gz is unicodedata's name table (gen_names.py); \N{...} uses it.
// Name aliases and named sequences are not included.
//
//go:embed names.txt.gz
var namesGz []byte

var (
	namesOnce sync.Once
	namesMap  map[string]rune
)

// cjkRanges are the unified ideograph blocks of Unicode 13.0.
var cjkRanges = [][2]rune{{0x3400, 0x4DBF}, {0x4E00, 0x9FFC}, {0x20000, 0x2A6DD}, {0x2A700, 0x2B734},
	{0x2B740, 0x2B81D}, {0x2B820, 0x2CEA1}, {0x2CEB0, 0x2EBE0}, {0x30000, 0x3134A}}

// lookupName is unicodedata.lookup (case-insensitive) for one character.
func lookupName(name string) (rune, bool) {
	upper := strings.Map(func(r rune) rune { // Py_TOUPPER: ASCII only
		if r >= 'a' && r <= 'z' {
			return r - 32
		}
		return r
	}, name)
	const cjk = "CJK UNIFIED IDEOGRAPH-"
	if strings.HasPrefix(upper, cjk) {
		hex := name[len(cjk):]
		if len(hex) != 4 && len(hex) != 5 {
			return 0, false
		}
		for _, c := range hex {
			if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'F') {
				return 0, false
			}
		}
		v, _ := strconv.ParseInt(hex, 16, 32)
		for _, r := range cjkRanges {
			if rune(v) >= r[0] && rune(v) <= r[1] {
				return rune(v), true
			}
		}
		return 0, false
	}
	namesOnce.Do(func() {
		namesMap = map[string]rune{}
		zr, err := gzip.NewReader(bytes.NewReader(namesGz))
		if err != nil {
			return
		}
		raw, _ := io.ReadAll(zr)
		for _, line := range strings.Split(string(raw), "\n") {
			code, n, ok := strings.Cut(line, ";")
			if !ok {
				continue
			}
			v, _ := strconv.ParseInt(code, 16, 32)
			namesMap[n] = rune(v)
		}
	})
	r, ok := namesMap[upper]
	return r, ok
}
