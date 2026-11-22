package server

import (
	"io/fs"
	"path"
	"strings"
	"sync"

	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

var (
	htmlAssetRe = pytext.MustCompile(`((?:href|src)=")((?:\./|/)(?:css|js|vendor)/[^"?]+)(")`)
	jsAssetRes  = []*regexpWrap{
		wrap(`(\bfrom\s*["'])(\.{1,2}/[^"'?]+?\.js)(["'])`),
		wrap(`(\bimport\s*\(\s*["'])(\.{1,2}/[^"'?]+?\.js)(["'])`),
		wrap(`(\bimport\s*["'])(\.{1,2}/[^"'?]+?\.js)(["'])`),
	}
)

type regexpWrap struct {
	re interface{ ReplaceAllString(string, string) string }
}

func wrap(p string) *regexpWrap { return &regexpWrap{pytext.MustCompile(p)} }

// VersionedHTML is server._versioned_html.
func VersionedHTML(text, version string) string {
	const meta = `<meta charset="utf-8">`
	if strings.Contains(text, meta) {
		text = strings.Replace(text, meta, meta+"\n  <meta name=\"ark-build\" content=\""+version+"\">", 1)
	}
	return htmlAssetRe.ReplaceAllString(text, "${1}${2}?v="+version+"${3}")
}

// ReleaseMeta adds <meta name="ark-version"> (Settings › About) after the
// ark-build meta when the server knows its release version.
func ReleaseMeta(text, release string) string {
	release = strings.Map(func(r rune) rune {
		if r < 128 && (r == '.' || r == '+' || r == '-' || ('0' <= r && r <= '9') || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')) {
			return r
		}
		return -1
	}, release)
	if len(release) > 40 {
		release = release[:40]
	}
	const build = `<meta name="ark-build" content="`
	at := strings.Index(text, build)
	if release == "" || at < 0 {
		return text
	}
	end := strings.Index(text[at:], ">")
	if end < 0 {
		return text
	}
	at += end + 1
	return text[:at] + "\n  <meta name=\"ark-version\" content=\"" + release + "\">" + text[at:]
}

// VersionedJS is server._versioned_js.
func VersionedJS(source, version string) string {
	for _, re := range jsAssetRes {
		source = re.re.ReplaceAllString(source, "${1}${2}?v="+version+"${3}")
	}
	return source
}

// posixNormpath is posixpath.normpath.
func posixNormpath(p string) string {
	if p == "" {
		return "."
	}
	initial := 0
	if strings.HasPrefix(p, "/") {
		initial = 1
		if strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///") {
			initial = 2
		}
	}
	var out []string
	for _, comp := range strings.Split(p, "/") {
		if comp == "" || comp == "." {
			continue
		}
		if comp != ".." || (initial == 0 && len(out) == 0) || (len(out) > 0 && out[len(out)-1] == "..") {
			out = append(out, comp)
		} else if len(out) > 0 {
			out = out[:len(out)-1]
		}
	}
	res := strings.Repeat("/", initial) + strings.Join(out, "/")
	if res == "" {
		return "."
	}
	return res
}

type staticEntry struct {
	fingerprint string
	data, gz    []byte
	gzChecked   bool
}

type staticCache struct {
	mu      sync.Mutex
	entries map[string]*staticEntry
	bytes   int
}

var assetCache = &staticCache{entries: map[string]*staticEntry{}}

const maxStaticCache = 32 << 20

// get is StaticAssets.get: transformed bytes plus a lazily built gzip copy.
func (sc *staticCache) get(c *reqCtx, rel string, info fs.FileInfo, version string, transform func(string, string) string, useGzip bool) ([]byte, bool, error) {
	fp := rel + "|" + info.ModTime().String() + "|" + version + "|" + pyStrInt(info.Size())
	sc.mu.Lock()
	e := sc.entries[rel]
	sc.mu.Unlock()
	if e == nil || e.fingerprint != fp {
		raw, err := c.app.Assets.Read(rel)
		if err != nil {
			return nil, false, err
		}
		if transform != nil {
			raw = []byte(transform(textfile.Universal(pyurl.DecodeReplace(raw)), version))
		}
		e = &staticEntry{fingerprint: fp, data: raw}
	}
	if useGzip && !e.gzChecked {
		ne := *e
		if len(ne.data) >= 1024 {
			if z := GzipBest(ne.data); len(z) < len(ne.data) {
				ne.gz = z
			}
		}
		ne.gzChecked = true
		e = &ne
	}
	if info.Size() <= 2<<20 {
		sc.mu.Lock()
		if old := sc.entries[rel]; old != nil {
			sc.bytes -= len(old.data) + len(old.gz)
		}
		if sc.bytes+len(e.data)+len(e.gz) > maxStaticCache {
			sc.entries, sc.bytes = map[string]*staticEntry{}, 0
		}
		sc.entries[rel] = e
		sc.bytes += len(e.data) + len(e.gz)
		sc.mu.Unlock()
	}
	if useGzip && e.gz != nil {
		return e.gz, true, nil
	}
	return e.data, false, nil
}

func pyStrInt(n int64) string {
	return strings.TrimSpace(strings.Replace(strings.Repeat(" ", 0)+itoa(n), " ", "", -1))
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// static is Handler._static.
func (c *reqCtx) static() error {
	p := c.path
	if p == "" || p == "/" {
		p = "/index.html"
	}
	rel := strings.TrimLeft(posixNormpath(p), "/")
	if strings.HasPrefix(rel, "..") || rel == "login.html" {
		return apiErr(404, "Not found", "")
	}
	if rel == "." {
		rel = ""
	}
	info, err := c.app.Assets.Stat(rel)
	if rel == "" || err != nil || info.IsDir() || !fs.ValidPath(rel) {
		name := path.Base(strings.TrimRight(p, "/"))
		if strings.TrimRight(p, "/") == "" {
			name = ""
		}
		if strings.Contains(name, ".") {
			return apiErr(404, "Not found", "")
		}
		rel = "index.html"
		if info, err = c.app.Assets.Stat(rel); err != nil {
			return apiErr(404, "Not found", "")
		}
	}
	suffix := path.Ext(rel)
	ctype := guessType(rel)
	switch suffix {
	case ".js":
		ctype = "text/javascript; charset=utf-8"
	case ".css":
		ctype = "text/css; charset=utf-8"
	case ".html":
		ctype = "text/html; charset=utf-8"
	case ".svg":
		ctype = "image/svg+xml"
	case ".webmanifest":
		ctype = "application/manifest+json; charset=utf-8"
	}
	version := c.app.Assets.Version()
	origRel := strings.TrimLeft(posixNormpath(p), "/")
	cache := "no-store"
	if c.query.First("v", "") == version && suffix != ".html" && origRel != "sw.js" {
		cache = "private, max-age=31536000, immutable"
	}
	var transform func(string, string) string
	switch {
	case rel == "index.html":
		transform = indexHTML(c.app.Assets, c.app.Cfg.ReleaseVersion)
	case suffix == ".html":
		transform = VersionedHTML
	case suffix == ".js":
		transform = VersionedJS
	}
	data, gz, err := assetCache.get(c, rel, info, version, transform, AcceptsGzip(c.header("Accept-Encoding")))
	if err != nil {
		return apiErr(404, "Not found", "")
	}
	headers := headerList{{"Cache-Control", cache}, {"Vary", "Accept-Encoding"}}
	if gz {
		headers = append(headers, [2]string{"Content-Encoding", "gzip"})
	}
	if origRel == "viz-frame.html" {
		headers = append(headers, [2]string{"Content-Security-Policy", VizCSP}, [2]string{"X-Frame-Options", "SAMEORIGIN"})
	}
	if origRel == "plugin-frame.html" {
		headers = append(headers, [2]string{"Content-Security-Policy", PluginCSP}, [2]string{"X-Frame-Options", "SAMEORIGIN"})
	}
	c.send(200, data, ctype, headers, false)
	return nil
}
