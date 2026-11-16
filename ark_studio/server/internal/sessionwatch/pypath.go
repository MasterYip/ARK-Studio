package sessionwatch

import (
	"os"
	"os/user"
	"runtime"
	"sort"
	"strings"
	"time"
)

// pathlib emulation: only what session_watch.py touches (str(Path(x)),
// Path / name, .suffix, Path.home(), os.path.expanduser and Path.glob).

var onWindows = runtime.GOOS == "windows"

// pyPath is a parsed pathlib.PurePath (drive, root, relative parts).
type pyPath struct {
	drv, root string
	parts     []string
}

func newPath(s string) pyPath {
	var p pyPath
	if onWindows {
		s = strings.ReplaceAll(s, "/", `\`)
		p.drv, p.root, s = winSplitRoot(s)
		p.parts = splitRel(s, `\`)
		return p
	}
	if strings.HasPrefix(s, "/") {
		p.root = "/"
		if strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "///") {
			p.root = "//"
		}
		s = strings.TrimLeft(s, "/")
	}
	p.parts = splitRel(s, "/")
	return p
}

func splitRel(rel, sep string) []string {
	var out []string
	for _, x := range strings.Split(rel, sep) {
		if x != "" && x != "." {
			out = append(out, x)
		}
	}
	return out
}

// winSplitRoot is pathlib._WindowsFlavour.splitroot (CPython 3.10).
func winSplitRoot(part string) (drv, root, rel string) {
	const sep = `\`
	prefix := ""
	if strings.HasPrefix(part, `\\`) {
		if strings.HasPrefix(part, `\\?\`) {
			prefix, part = part[:4], part[4:]
			if strings.HasPrefix(part, `UNC\`) {
				prefix += part[:3]
				part = sep + part[3:]
			}
		}
	}
	at := func(i int) string {
		if i < len(part) {
			return part[i : i+1]
		}
		return ""
	}
	first, second, third := at(0), at(1), at(2)
	if second == sep && first == sep && third != sep {
		index := strings.Index(part[2:], sep)
		if index != -1 {
			index += 2
			index2 := -1
			if j := strings.Index(part[index+1:], sep); j != -1 {
				index2 = index + 1 + j
			}
			if index2 != index+1 {
				if index2 == -1 {
					index2 = len(part)
				}
				rest := ""
				if index2+1 <= len(part) {
					rest = part[min(index2+1, len(part)):]
				}
				if prefix != "" {
					return prefix + part[1:index2], sep, rest
				}
				return part[:index2], sep, rest
			}
		}
	}
	if second == ":" && isASCIILetter(first) {
		drv = part[:2]
		part = part[2:]
		first = third
	}
	if first == sep {
		root = sep
		part = strings.TrimLeft(part, sep)
	}
	return prefix + drv, root, part
}

func isASCIILetter(s string) bool {
	return len(s) == 1 && (s[0]|0x20) >= 'a' && (s[0]|0x20) <= 'z'
}

func (p pyPath) sep() string {
	if onWindows {
		return `\`
	}
	return "/"
}

func (p pyPath) String() string {
	body := strings.Join(p.parts, p.sep())
	if p.drv != "" || p.root != "" {
		return p.drv + p.root + body
	}
	if body == "" {
		return "."
	}
	return body
}

// join is Path / name for a plain child name (no separators, no drive).
func (p pyPath) join(names ...string) pyPath {
	q := pyPath{drv: p.drv, root: p.root, parts: append([]string(nil), p.parts...)}
	for _, n := range names {
		if n != "" && n != "." {
			q.parts = append(q.parts, n)
		}
	}
	return q
}

// suffix is PurePath.suffix.
func (p pyPath) suffix() string {
	if len(p.parts) == 0 {
		return ""
	}
	name := p.parts[len(p.parts)-1]
	i := strings.LastIndex(name, ".")
	if 0 < i && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

// expandUser is os.path.expanduser (it reads the real process environment,
// not the env passed to run()).
func expandUser(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	if onWindows {
		i := 1
		for i < len(path) && path[i] != '\\' && path[i] != '/' {
			i++
		}
		var userhome string
		if v, ok := os.LookupEnv("USERPROFILE"); ok {
			userhome = v
		} else if hp, ok := os.LookupEnv("HOMEPATH"); !ok {
			return path
		} else {
			userhome = ntJoin(os.Getenv("HOMEDRIVE"), hp)
		}
		if i != 1 {
			target := path[1:i]
			current, _ := os.LookupEnv("USERNAME")
			if target != current {
				dir, base := ntSplit(userhome)
				if current != base {
					return path
				}
				userhome = ntJoin(dir, target)
			}
		}
		return userhome + path[i:]
	}
	i := strings.Index(path[1:], "/")
	if i < 0 {
		i = len(path)
	} else {
		i++
	}
	var userhome string
	if i == 1 {
		if v, ok := os.LookupEnv("HOME"); ok {
			userhome = v
		} else if u, err := user.Current(); err == nil {
			userhome = u.HomeDir
		} else {
			return path
		}
	} else {
		u, err := user.Lookup(path[1:i])
		if err != nil {
			return path
		}
		userhome = u.HomeDir
	}
	userhome = strings.TrimRight(userhome, "/")
	if out := userhome + path[i:]; out != "" {
		return out
	}
	return "/"
}

// ntJoin / ntSplit: the slice of ntpath.join / ntpath.split expanduser needs.
func ntJoin(a, b string) string {
	if b == "" {
		return a
	}
	if strings.HasPrefix(b, `\`) || strings.HasPrefix(b, "/") || (len(b) >= 2 && b[1] == ':') {
		if len(b) >= 2 && b[1] == ':' {
			return b
		}
		if len(a) >= 2 && a[1] == ':' {
			return a[:2] + b
		}
		return b
	}
	if a == "" || strings.HasSuffix(a, `\`) || strings.HasSuffix(a, "/") || (len(a) == 2 && a[1] == ':') {
		return a + b
	}
	return a + `\` + b
}

func ntSplit(p string) (head, tail string) {
	d := ""
	if len(p) >= 2 && p[1] == ':' {
		d, p = p[:2], p[2:]
	}
	i := len(p)
	for i > 0 && p[i-1] != '\\' && p[i-1] != '/' {
		i--
	}
	head, tail = p[:i], p[i:]
	if strings.Trim(head, `\/`) != "" {
		head = strings.TrimRight(head, `\/`)
	}
	return d + head, tail
}

// homePath is Path.home(); ok=false is pathlib's RuntimeError.
func homePath() (pyPath, bool) {
	h := expandUser("~")
	if strings.HasPrefix(h, "~") {
		return pyPath{}, false
	}
	return newPath(h), true
}

// ------------------------------------------------------------------ glob

type candidate struct {
	path  pyPath
	mtime float64
}

func stMtime(fi os.FileInfo) float64 {
	t := fi.ModTime()
	return float64(t.Unix()) + float64(t.Nanosecond())*1e-9
}

// matchSuffix is fnmatch "*<suffix>" (case-insensitive on Windows, like
// pathlib's re.IGNORECASE pattern).
func matchSuffix(name, suffix string) bool {
	if !onWindows {
		return strings.HasSuffix(name, suffix)
	}
	nr, sr := []rune(name), []rune(suffix)
	if len(nr) < len(sr) {
		return false
	}
	return strings.EqualFold(string(nr[len(nr)-len(sr):]), suffix)
}

// readDirRaw lists a directory in OS order (os.scandir), not sorted.
func readDirRaw(dir string) ([]os.DirEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// globRecursive is Path.glob("**/*"+suffix) on CPython 3.10: directories
// pre-order (not following directory symlinks), names matched per directory.
func globRecursive(root pyPath, suffix string) []pyPath {
	var out []pyPath
	var walk func(dir pyPath)
	walk = func(dir pyPath) {
		entries, err := readDirRaw(dir.String())
		if err != nil {
			return
		}
		for _, e := range entries {
			if matchSuffix(e.Name(), suffix) {
				out = append(out, dir.join(e.Name()))
			}
		}
		for _, e := range entries {
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := dir.join(e.Name())
			if fi, err := os.Stat(child.String()); err == nil && fi.IsDir() {
				walk(child)
			}
		}
	}
	walk(root)
	return out
}

// candidates is _candidates(root).
func candidates(root pyPath, now time.Time) []candidate {
	if fi, err := os.Stat(root.String()); err != nil || !fi.IsDir() {
		return nil
	}
	cutoff := float64(now.UnixNano())/1e9 - searchDays*86400
	var found []candidate
	for _, suffix := range []string{".jsonl", ".jsonl.zstd"} {
		for _, p := range globRecursive(root, suffix) {
			fi, err := os.Stat(p.String())
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
			if m := stMtime(fi); m >= cutoff {
				found = append(found, candidate{p, m})
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool { return found[i].mtime > found[j].mtime })
	if len(found) > searchLimit {
		found = found[:searchLimit]
	}
	return found
}
