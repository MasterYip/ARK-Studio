package setup

// Paths written in unit bodies (Go only, 2026-11-24).
//
//   logical paths   task:<relative path>[?space=<task space id>] names a
//                   place inside a task space; each device resolves it with
//                   its own location of that task space, so moving the task
//                   space is one setup change.
//   RewritePrefix   old absolute prefix -> new one (the relocate tool)
//   ToLogical       absolute paths inside a task space -> task: paths
//
// Both rewrites leave fenced code blocks alone and recognise a path written
// with either separator, as a file: URL (file:///E:/..., file:///data/...)
// or bare, and only where the prefix ends at a path boundary (E:/tasks does
// not match E:/tasks-old).

import (
	"net/url"
	"regexp"
	"strings"
)

// LogicalScheme starts a logical task-space path.
const LogicalScheme = "task:"

// ParseLogical splits "task:<rel>[?space=<id>]".  rel never escapes the
// task space ("..") and uses "/"; "" and "./" name the task space itself.
func ParseLogical(ref string) (rel, space string, ok bool) {
	ref = strings.TrimSpace(ref)
	if !strings.HasPrefix(strings.ToLower(ref), LogicalScheme) {
		return "", "", false
	}
	rest := ref[len(LogicalScheme):]
	if i := strings.Index(rest, "?"); i >= 0 {
		q, _ := url.ParseQuery(rest[i+1:])
		space = strings.TrimSpace(q.Get("space"))
		rest = rest[:i]
	}
	if dec, err := url.PathUnescape(rest); err == nil {
		rest = dec
	}
	rest = strings.ReplaceAll(rest, "\\", "/")
	if strings.HasPrefix(rest, "/") || (len(rest) >= 2 && rest[1] == ':') {
		return "", "", false // absolute: not a task-space path
	}
	for _, part := range strings.Split(rest, "/") {
		if part == ".." {
			return "", "", false
		}
	}
	rest = strings.TrimPrefix(rest, "./")
	return rest, space, true
}

// JoinTaskPath is the absolute path of rel inside root, in root's style
// (backslashes when root uses only backslashes).
func JoinTaskPath(root, rel string) string {
	sep := "/"
	if strings.Contains(root, "\\") && !strings.Contains(root, "/") {
		sep = "\\"
	}
	base := strings.TrimRight(root, "/\\")
	if base == "" || (len(base) == 2 && base[1] == ':') {
		base += sep
	} else if rel != "" {
		base += sep
	}
	if rel == "" {
		return base
	}
	return base + strings.ReplaceAll(rel, "/", sep)
}

// windowsPath reports a drive path (C:\..., C:/...).
func windowsPath(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '/' || p[2] == '\\') &&
		((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z'))
}

// cleanPrefix trims trailing separators; a prefix must name at least one
// folder below a drive or "/" (moving a whole drive is not a relocation).
func cleanPrefix(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(strings.ToLower(p), "file://") {
		p = fileURLPath(p)
	}
	trimmed := strings.TrimRight(p, "/\\")
	switch {
	case windowsPath(p):
		return trimmed, len(trimmed) > 3
	case strings.HasPrefix(p, "/"):
		return trimmed, len(trimmed) > 1
	case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\"):
		return trimmed, len(trimmed) > 2
	}
	return "", false
}

// fileURLPath is the path of a file: URL (file:///E:/a%20b -> E:/a b).
func fileURLPath(u string) string {
	p := u[len("file:"):]
	p = strings.TrimPrefix(p, "//")
	if len(p) >= 4 && p[0] == '/' && windowsPath(p[1:]) {
		p = p[1:]
	}
	if dec, err := url.PathUnescape(p); err == nil {
		p = dec
	}
	return p
}

// pathTerm ends the rest of a path written in text.
const pathTerm = "\\s)\\]>\"'`|,;*?<（）。，；：、"

// pathPrefixRe matches prefix (an optional file: URL in front, the rest of the
// path after it).  Groups: 1 the file: URL scheme part, 2 the rest.
func pathPrefixRe(prefix string) *regexp.Regexp {
	parts := regexp.MustCompile(`[\\/]+`).Split(prefix, -1)
	var b strings.Builder
	if windowsPath(prefix) {
		b.WriteString("(?i)")
	}
	b.WriteString(`(file:///?)?`)
	for i, part := range parts {
		if i > 0 {
			b.WriteString(`[\\/]`)
		}
		q := regexp.QuoteMeta(part)
		b.WriteString(strings.ReplaceAll(q, " ", "(?: |%20)"))
	}
	b.WriteString(`([\\/][^` + pathTerm + `]*)?`)
	return regexp.MustCompile(b.String())
}

func pathChar(c byte) bool {
	return c == '_' || c == '.' || c == '~' || c == '-' || c == '/' || c == '\\' || c == ':' ||
		(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

// replacePaths calls fn for every match of re in text that sits on path
// boundaries; fn gets the file: URL part and the rest and returns the text
// to put in place of the match.
func replacePaths(text string, re *regexp.Regexp, fn func(url, rest string) string) (string, int) {
	var b strings.Builder
	n, pos := 0, 0
	for pos <= len(text) {
		loc := re.FindStringSubmatchIndex(text[pos:])
		if loc == nil {
			break
		}
		start, end := pos+loc[0], pos+loc[1]
		urlPart, rest := "", ""
		if loc[2] >= 0 {
			urlPart = text[pos+loc[2] : pos+loc[3]]
		}
		if loc[4] >= 0 {
			rest = text[pos+loc[4] : pos+loc[5]]
		}
		okStart := start == 0 || !pathChar(text[start-1])
		if urlPart == "" && start >= 1 && text[start-1] == '/' {
			okStart = false
		}
		okEnd := rest != "" || end == len(text) || !pathChar(text[end]) || text[end] == ':'
		if end < len(text) && rest == "" && (text[end] == '/' || text[end] == '\\') {
			okEnd = true
		}
		if !okStart || !okEnd || end == start {
			b.WriteString(text[pos : start+1])
			pos = start + 1
			continue
		}
		b.WriteString(text[pos:start])
		b.WriteString(fn(urlPart, rest))
		n++
		pos = end
	}
	if pos < len(text) {
		b.WriteString(text[pos:])
	}
	return b.String(), n
}

// outsideFences applies fn to the text outside fenced code blocks (``` or
// ~~~); an unclosed fence runs to the end.
func outsideFences(text string, fn func(string) (string, int)) (string, int) {
	var out, chunk strings.Builder
	total := 0
	flush := func() {
		if chunk.Len() > 0 {
			s, n := fn(chunk.String())
			out.WriteString(s)
			total += n
			chunk.Reset()
		}
	}
	fence := ""
	for _, line := range strings.SplitAfter(text, "\n") {
		trim := strings.TrimLeft(line, " \t")
		if fence == "" {
			if strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~") {
				flush()
				fence = trim[:3]
				out.WriteString(line)
				continue
			}
			chunk.WriteString(line)
			continue
		}
		out.WriteString(line)
		if strings.HasPrefix(trim, fence) {
			fence = ""
		}
	}
	flush()
	return out.String(), total
}

// RewritePrefix replaces the absolute path prefix from with to everywhere
// in text outside fenced code blocks.  It returns the new text and the
// number of paths changed (0 and text unchanged when from is not a usable
// prefix).
func RewritePrefix(text, from, to string) (string, int) {
	from, ok := cleanPrefix(from)
	if !ok {
		return text, 0
	}
	to = strings.TrimRight(strings.TrimSpace(to), "/\\")
	if to == "" || from == to {
		return text, 0
	}
	re := pathPrefixRe(from)
	slashOnly := !strings.Contains(to, "\\")
	return outsideFences(text, func(s string) (string, int) {
		return replacePaths(s, re, func(urlPart, rest string) string {
			if slashOnly {
				rest = strings.ReplaceAll(rest, "\\", "/")
			}
			if urlPart != "" {
				urlPart = "file://"
				if !strings.HasPrefix(to, "/") {
					urlPart = "file:///"
				}
				rest = strings.ReplaceAll(rest, " ", "%20")
				return urlPart + strings.ReplaceAll(to, " ", "%20") + rest
			}
			return to + rest
		})
	})
}

// LogicalRoot is one task space location a ToLogical pass recognises.
type LogicalRoot struct {
	Path  string // the absolute task space path on some device
	Space string // "" for the default task space, else its id (?space=)
}

// ToLogical rewrites absolute paths inside any of roots to task: paths,
// outside fenced code blocks.  Longer roots win over shorter ones.
func ToLogical(text string, roots []LogicalRoot) (string, int) {
	type rx struct {
		re    *regexp.Regexp
		space string
		n     int
	}
	var list []rx
	for _, r := range roots {
		p, ok := cleanPrefix(r.Path)
		if !ok {
			continue
		}
		list = append(list, rx{pathPrefixRe(p), r.Space, len(p)})
	}
	for i := 1; i < len(list); i++ { // longest first (stable insertion sort)
		for j := i; j > 0 && list[j].n > list[j-1].n; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	return outsideFences(text, func(s string) (string, int) {
		total := 0
		for _, r := range list {
			var n int
			s, n = replacePaths(s, r.re, func(urlPart, rest string) string {
				rel := strings.TrimLeft(strings.ReplaceAll(rest, "\\", "/"), "/")
				if urlPart != "" {
					if dec, err := url.PathUnescape(rel); err == nil {
						rel = dec
					}
				}
				rel = strings.ReplaceAll(rel, " ", "%20")
				if rel == "" {
					rel = "./"
				}
				out := LogicalScheme + rel
				if r.space != "" {
					out += "?space=" + url.QueryEscape(r.space)
				}
				return out
			})
			total += n
		}
		return s, total
	})
}
