// Package pyos ports the small os/shutil/shlex/subprocess helpers whose
// exact behaviour shows up in API payloads: shutil.which (3.10),
// shlex.split (posix and non-posix), subprocess.list2cmdline,
// str.capitalize and Path.home().
package pyos

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"

	"arkstudio/go/internal/pytext"
)

const windows = runtime.GOOS == "windows"

// PathSep is os.pathsep.
var PathSep = func() string {
	if windows {
		return ";"
	}
	return ":"
}()

// HasDirPart is os.path.isabs(p) or os.sep in p or (os.altsep and os.altsep in p).
func HasDirPart(p string) bool {
	if filepath.IsAbs(p) || strings.Contains(p, string(os.PathSeparator)) {
		return true
	}
	if windows {
		return strings.Contains(p, "/")
	}
	return false
}

// IsFile is os.path.isfile / Path.is_file.
func IsFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// IsDir is os.path.isdir.
func IsDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Exists is os.path.exists.
func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func accessCheck(name string) bool {
	fi, err := os.Stat(name)
	if err != nil || fi.IsDir() {
		return false
	}
	return executable(name)
}

func joinPath(dir, file string) string {
	if !windows {
		if dir == "" || strings.HasSuffix(dir, "/") {
			return dir + file
		}
		return dir + "/" + file
	}
	// ntpath.join(dir, file) for a plain file name
	if dir == "" || strings.HasSuffix(dir, `\`) || strings.HasSuffix(dir, "/") {
		return dir + file
	}
	if len(dir) == 2 && dir[1] == ':' {
		return dir + file
	}
	return dir + `\` + file
}

func normcase(p string) string {
	if !windows {
		return p
	}
	return strings.ToLower(strings.ReplaceAll(p, "/", `\`))
}

// Which is shutil.which(cmd, path=path) of Python 3.10; with usePath false
// it reads $PATH (os.defpath when unset).  "" means not found.
func Which(cmd string, path string, usePath bool) string {
	if d := filepath.Dir(cmd); HasDirPart(cmd) || (d != "." && d != "") {
		if accessCheck(cmd) {
			return cmd
		}
		return ""
	}
	if !usePath {
		var ok bool
		path, ok = os.LookupEnv("PATH")
		if !ok {
			if windows {
				path = `.;C:\bin`
			} else {
				path = "/bin:/usr/bin"
			}
		}
	}
	if path == "" {
		return ""
	}
	dirs := strings.Split(path, PathSep)
	files := []string{cmd}
	if windows {
		hasCur := false
		for _, d := range dirs {
			if d == "." {
				hasCur = true
			}
		}
		if !hasCur {
			dirs = append([]string{"."}, dirs...)
		}
		pathext := strings.Split(os.Getenv("PATHEXT"), PathSep)
		matched := false
		for _, ext := range pathext {
			if strings.HasSuffix(strings.ToLower(cmd), strings.ToLower(ext)) {
				matched = true
				break
			}
		}
		if !matched {
			files = files[:0]
			for _, ext := range pathext {
				files = append(files, cmd+ext)
			}
		}
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		nd := normcase(dir)
		if seen[nd] {
			continue
		}
		seen[nd] = true
		for _, f := range files {
			name := joinPath(dir, f)
			if accessCheck(name) {
				return name
			}
		}
	}
	return ""
}

// shlex's ValueErrors.
var (
	ErrNoClosingQuotation = errors.New("No closing quotation")
	ErrNoEscapedChar      = errors.New("No escaped character")
)

// ShlexSplit is shlex.split(text, posix=posix) (comments disabled).
func ShlexSplit(text string, posix bool) ([]string, error) {
	const whitespace = " \t\r\n"
	const quotes = "'\""
	rs := []rune(text)
	i := 0
	var out []string
	for {
		state := ' '
		escapedState := ' '
		var token []rune
		quoted := false
		end := false // shlex state None: the input is exhausted
	loop:
		for {
			eof := i >= len(rs)
			var next rune
			if !eof {
				next = rs[i]
				i++
			}
			isWS := !eof && strings.ContainsRune(whitespace, next)
			isQuote := !eof && strings.ContainsRune(quotes, next)
			switch {
			case state == ' ':
				switch {
				case eof:
					end = true
					break loop
				case isWS:
					if len(token) > 0 || (posix && quoted) {
						break loop
					}
				case posix && next == '\\':
					escapedState, state = 'a', next
				case isQuote:
					if !posix {
						token = []rune{next}
					}
					state = next
				default:
					token = []rune{next}
					state = 'a'
				}
			case state == '\'' || state == '"':
				quoted = true
				if eof {
					return nil, ErrNoClosingQuotation
				}
				switch {
				case next == state:
					if !posix {
						token = append(token, next)
						state = ' '
						break loop
					}
					state = 'a'
				case posix && next == '\\' && state == '"':
					escapedState, state = state, next
				default:
					token = append(token, next)
				}
			case state == '\\':
				if eof {
					return nil, ErrNoEscapedChar
				}
				if (escapedState == '\'' || escapedState == '"') && next != state && next != escapedState {
					token = append(token, state)
				}
				token = append(token, next)
				state = escapedState
			default: // 'a'
				switch {
				case eof:
					end = true
					break loop
				case isWS:
					state = ' '
					if len(token) > 0 || (posix && quoted) {
						break loop
					}
				case posix && isQuote:
					state = next
				case posix && next == '\\':
					escapedState, state = 'a', next
				default:
					token = append(token, next)
				}
			}
		}
		if len(token) == 0 && !(posix && quoted) {
			return out, nil // eof token ('' or None)
		}
		out = append(out, string(token))
		_ = end
	}
}

// List2Cmdline is subprocess.list2cmdline.
func List2Cmdline(args []string) string {
	var b strings.Builder
	for n, arg := range args {
		if n > 0 {
			b.WriteByte(' ')
		}
		needQuote := strings.ContainsAny(arg, " \t") || arg == ""
		if needQuote {
			b.WriteByte('"')
		}
		bs := 0
		for _, c := range arg {
			switch c {
			case '\\':
				bs++
			case '"':
				b.WriteString(strings.Repeat(`\`, bs*2))
				bs = 0
				b.WriteString(`\"`)
			default:
				if bs > 0 {
					b.WriteString(strings.Repeat(`\`, bs))
					bs = 0
				}
				b.WriteRune(c)
			}
		}
		if bs > 0 {
			b.WriteString(strings.Repeat(`\`, bs))
		}
		if needQuote {
			b.WriteString(strings.Repeat(`\`, bs))
			b.WriteByte('"')
		}
	}
	return b.String()
}

// Capitalize is str.capitalize: the first character title-cased, the rest
// lower-cased.
func Capitalize(s string) string {
	if s == "" {
		return s
	}
	rs := []rune(s)
	first, ok := specialTitle[rs[0]]
	if !ok {
		first = string(unicode.ToTitle(rs[0]))
	}
	return first + pytext.Lower(string(rs[1:]))
}

// specialTitle holds the SpecialCasing.txt title mappings that expand to
// more than one character (str.capitalize applies them; Go's ToTitle cannot).
var specialTitle = map[rune]string{
	'ß': "Ss", 'ﬀ': "Ff", 'ﬁ': "Fi", 'ﬂ': "Fl", 'ﬃ': "Ffi", 'ﬄ': "Ffl",
	'ﬅ': "St", 'ﬆ': "St", 'ŉ': "ʼN", 'ǰ': "J̌", 'և': "Եւ",
	'ẖ': "H̱", 'ẗ': "T̈", 'ẘ': "W̊", 'ẙ': "Y̊", 'ẚ': "Aʾ",
	'ΐ': "Ϊ́", 'ΰ': "Ϋ́",
}

// Home is Path.home(): %USERPROFILE% (then HOMEDRIVE+HOMEPATH) on Windows,
// $HOME on POSIX.
func Home() string {
	if windows {
		if v := os.Getenv("USERPROFILE"); v != "" {
			return v
		}
		if p := os.Getenv("HOMEPATH"); p != "" {
			return os.Getenv("HOMEDRIVE") + p
		}
	}
	if v := os.Getenv("HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return h
}

// ExpandUser is os.path.expanduser for "~" and "~/..." ("~user" is left
// unchanged).
func ExpandUser(p string) string {
	if p == "~" {
		return Home()
	}
	if strings.HasPrefix(p, "~/") || (windows && strings.HasPrefix(p, `~\`)) {
		return Home() + p[1:]
	}
	return p
}

var quoteUnsafe = regexp.MustCompile(`[^\w@%+=:,./-]`)

// Quote is shlex.quote.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	if !quoteUnsafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
