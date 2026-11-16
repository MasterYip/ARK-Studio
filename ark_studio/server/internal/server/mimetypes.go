package server

import (
	"bufio"
	"os"
	"strings"
	"sync"

	"arkstudio/go/internal/compat/pyjson"
)

// Port of CPython 3.10 mimetypes.guess_type(name)[0] with strict=True.
//
// mimetypes.init() starts from the built-in tables (snapshotted by
// gen_static.py) and lets the host override them: the Windows registry
// (HKEY_CLASSES_ROOT\.<ext>\Content Type) and the mime.types files below.
// So Content-Type depends on the host exactly as it does for Python (on one
// Windows machine .csv is application/vnd.ms-excel and .webp is "").

var knownFiles = []string{"/etc/mime.types", "/etc/httpd/mime.types", "/etc/httpd/conf/mime.types",
	"/etc/apache/mime.types", "/etc/apache2/mime.types", "/usr/local/etc/httpd/conf/mime.types",
	"/usr/local/lib/netscape/mime.types", "/usr/local/etc/httpd/conf/mime.types", "/usr/local/etc/mime.types"}

type mimeDB struct {
	types, suffix, encodings map[string]string
}

var (
	mimeOnce sync.Once
	mimes    *mimeDB
)

func loadMimes() *mimeDB {
	mimeOnce.Do(func() {
		db := &mimeDB{types: map[string]string{}, suffix: map[string]string{}, encodings: map[string]string{}}
		snap, _ := pyjson.LoadsObject(staticJSON["mimetypes"])
		for key, dst := range map[string]map[string]string{"types": db.types, "suffix": db.suffix, "encodings": db.encodings} {
			o, _ := snap.Value(key).(*pyjson.Object)
			for _, k := range o.Keys() {
				dst[k] = pyjson.Str(o.Value(k))
			}
		}
		readRegistryMimes(func(ctype, ext string) { db.types[ext] = ctype })
		for _, file := range knownFiles {
			db.readFile(file)
		}
		mimes = db
	})
	return mimes
}

// readFile is MimeTypes.read(file) / readfp.
func (db *mimeDB) readFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		words := strings.Fields(sc.Text())
		for i, w := range words {
			if strings.HasPrefix(w, "#") {
				words = words[:i]
				break
			}
		}
		if len(words) == 0 {
			continue
		}
		for _, suff := range words[1:] {
			db.types["."+suff] = words[0]
		}
	}
}

// splitext is posixpath.splitext.
func splitext(p string) (string, string) {
	slash := strings.LastIndex(p, "/")
	dot := strings.LastIndex(p, ".")
	if dot > slash {
		// Leading dots of the file name do not start an extension.
		start := slash + 1
		for start < dot && p[start] == '.' {
			start++
		}
		if start < dot {
			return p[:dot], p[dot:]
		}
	}
	return p, ""
}

// guessType is mimetypes.guess_type(name)[0] or "application/octet-stream".
func guessType(name string) string {
	if t, ok := guessMime(name); ok && t != "" {
		return t
	}
	return "application/octet-stream"
}

// guessMime is mimetypes.guess_type(name)[0]; ok=false for None.  An empty
// registry value is a type of "" (Python then serves "or octet-stream").
func guessMime(name string) (string, bool) {
	db := loadMimes()
	// urllib.parse._splittype: "scheme:rest" for a leading [^/:]+ before ':'.
	if i := strings.IndexByte(name, ':'); i > 0 && !strings.ContainsAny(name[:i], "/") {
		scheme := strings.ToLower(name[:i])
		if scheme == "data" {
			rest := name[i+1:]
			comma := strings.IndexByte(rest, ',')
			if comma < 0 {
				return "", false
			}
			semi := strings.IndexByte(rest[:comma], ';')
			t := rest[:comma]
			if semi >= 0 {
				t = rest[:semi]
			}
			if strings.Contains(t, "=") || !strings.Contains(t, "/") {
				t = "text/plain"
			}
			return t, true
		}
		name = name[i+1:]
	}
	base, ext := splitext(name)
	for {
		s, ok := db.suffix[ext]
		if !ok {
			break
		}
		base, ext = splitext(base + s)
	}
	if _, ok := db.encodings[ext]; ok {
		base, ext = splitext(base)
	}
	if t, ok := db.types[ext]; ok {
		return t, true
	}
	if t, ok := db.types[strings.ToLower(ext)]; ok {
		return t, true
	}
	return "", false
}
