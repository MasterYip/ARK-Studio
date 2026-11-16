// Package pyurl reproduces the urllib.parse behavior of Python 3.10 that the
// ARK request handler relies on: urlparse() of the raw request target
// (including ";params" splitting and "//netloc" detection), unquote() with
// UTF-8 errors='replace', and parse_qs() which drops blank values and
// splits only on "&".
package pyurl

import (
	"strings"
	"unicode/utf8"
)

// Target is urlparse(request_target) split into the parts the server uses.
type Target struct {
	Scheme, Netloc, Path, Params, Query, Fragment string
}

const schemeChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+-."

// Parse is urllib.parse.urlparse(url).
func Parse(url string) Target { return parse(url, true) }

// Split is urllib.parse.urlsplit(url): like Parse without ";params".
func Split(url string) Target { return parse(url, false) }

func parse(url string, params bool) Target {
	var t Target
	url = strings.NewReplacer("\t", "", "\r", "", "\n", "").Replace(url)
	if i := strings.IndexByte(url, ':'); i > 0 {
		ok := isLetter(url[0])
		for j := 0; j < i && ok; j++ {
			ok = strings.IndexByte(schemeChars, url[j]) >= 0
		}
		if ok {
			t.Scheme = strings.ToLower(url[:i])
			url = url[i+1:]
		}
	}
	if strings.HasPrefix(url, "//") {
		end := len(url)
		for _, c := range "/?#" {
			if k := strings.IndexRune(url[2:], c); k >= 0 && k+2 < end {
				end = k + 2
			}
		}
		t.Netloc, url = url[2:end], url[end:]
	}
	if i := strings.IndexByte(url, '#'); i >= 0 {
		url, t.Fragment = url[:i], url[i+1:]
	}
	if i := strings.IndexByte(url, '?'); i >= 0 {
		url, t.Query = url[:i], url[i+1:]
	}
	if params && usesParams(t.Scheme) && strings.Contains(url, ";") {
		url, t.Params = splitParams(url)
	}
	t.Path = url
	return t
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func usesParams(scheme string) bool {
	switch scheme {
	case "", "ftp", "hdl", "prospero", "http", "imap", "https", "shttp", "rtsp", "rtspu",
		"sip", "sips", "mms", "sftp", "tel":
		return true
	}
	return false
}

func splitParams(url string) (string, string) {
	var i int
	if slash := strings.LastIndexByte(url, '/'); slash >= 0 {
		k := strings.IndexByte(url[slash:], ';')
		if k < 0 {
			return url, ""
		}
		i = slash + k
	} else {
		i = strings.IndexByte(url, ';')
	}
	return url[:i], url[i+1:]
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// Unquote is urllib.parse.unquote(s): %XX escapes become bytes, malformed
// escapes stay literal, and the bytes decode as UTF-8 with U+FFFD for every
// invalid byte.
func Unquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	buf := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if ok1 && ok2 {
				buf = append(buf, hi<<4|lo)
				i += 2
				continue
			}
		}
		buf = append(buf, s[i])
	}
	if utf8.Valid(buf) {
		return string(buf)
	}
	return DecodeReplace(buf)
}

// DecodeReplace is bytes.decode("utf-8", "replace"): each maximal invalid
// subpart (a truncated sequence counts once) becomes one U+FFFD, unlike
// strings.ToValidUTF8 which merges runs and utf8.DecodeRune which splits them.
func DecodeReplace(buf []byte) string {
	var b strings.Builder
	for len(buf) > 0 {
		r, size := utf8.DecodeRune(buf)
		if r != utf8.RuneError || size > 1 {
			b.WriteRune(r)
			buf = buf[size:]
			continue
		}
		b.WriteRune(utf8.RuneError)
		buf = buf[invalidSubpart(buf):]
	}
	return b.String()
}

func invalidSubpart(buf []byte) int {
	c := buf[0]
	var need int
	lo, hi := byte(0x80), byte(0xbf)
	switch {
	case c >= 0xc2 && c <= 0xdf:
		need = 1
	case c >= 0xe0 && c <= 0xef:
		need = 2
		if c == 0xe0 {
			lo = 0xa0
		} else if c == 0xed {
			hi = 0x9f
		}
	case c >= 0xf0 && c <= 0xf4:
		need = 3
		if c == 0xf0 {
			lo = 0x90
		} else if c == 0xf4 {
			hi = 0x8f
		}
	default:
		return 1
	}
	n := 1
	for k := 0; k < need && n < len(buf); k++ {
		if buf[n] < lo || buf[n] > hi {
			break
		}
		lo, hi = 0x80, 0xbf
		n++
	}
	return n
}

// Values is the dict of lists parse_qs returns.
type Values map[string][]string

// First is (query.get(name) or [def])[0].
func (v Values) First(name, def string) string {
	if items := v[name]; len(items) > 0 {
		return items[0]
	}
	return def
}

// Has reports whether the key is present (“name in query“).
func (v Values) Has(name string) bool { _, ok := v[name]; return ok }

// ParseQS is urllib.parse.parse_qs(qs) with keep_blank_values=False.
func ParseQS(qs string) Values { return parseQS(qs, false) }

// ParseQSKeepBlank is parse_qs(qs, keep_blank_values=True).
func ParseQSKeepBlank(qs string) Values { return parseQS(qs, true) }

func parseQS(qs string, keepBlank bool) Values {
	out := Values{}
	for _, nv := range strings.Split(qs, "&") {
		if nv == "" {
			continue
		}
		name, value, ok := strings.Cut(nv, "=")
		if (!ok || value == "") && !keepBlank {
			continue
		}
		name = Unquote(strings.ReplaceAll(name, "+", " "))
		value = Unquote(strings.ReplaceAll(value, "+", " "))
		out[name] = append(out[name], value)
	}
	return out
}

// Quote is urllib.parse.quote(s, safe=”) (used for login ?next=).
func Quote(s string, safe string) string {
	const always = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if strings.IndexByte(always, c) >= 0 || strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte("0123456789ABCDEF"[c>>4])
		b.WriteByte("0123456789ABCDEF"[c&15])
	}
	return b.String()
}

// DecodeStrict is bytes.decode("utf-8") (nil on invalid input).
func DecodeStrict(raw []byte) *string {
	if !utf8.Valid(raw) {
		return nil
	}
	s := string(raw)
	return &s
}

// Decoder is codecs.getincrementaldecoder("utf-8")("replace"): an incomplete
// but so-far valid sequence at the end of a chunk waits for the next chunk.
type Decoder struct{ pending []byte }

// Decode decodes one chunk; final flushes a pending partial sequence.
func (d *Decoder) Decode(chunk []byte, final bool) string {
	buf := append(d.pending, chunk...)
	d.pending = nil
	if !final {
		for i := len(buf) - 1; i >= 0 && i >= len(buf)-3; i-- {
			c := buf[i]
			if c < 0x80 {
				break
			}
			if c >= 0xc0 {
				if c >= 0xc2 && c <= 0xf4 && !utf8.FullRune(buf[i:]) && invalidSubpart(buf[i:]) == len(buf)-i {
					d.pending = append([]byte(nil), buf[i:]...)
					buf = buf[:i]
				}
				break
			}
		}
	}
	if utf8.Valid(buf) {
		return string(buf)
	}
	return DecodeReplace(buf)
}
