// Package search ports arkstudio/search.py: Workbench Search over nodes and
// project files with one matcher (plain text, match case, whole word or a
// Python regular expression), bounded by result, per-item and time limits.
package search

import (
	"math"
	"runtime"
	"strings"
	"time"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pyre"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/workspace"
)

// Limits (search.py).
const (
	MaxQuery        = 500
	MaxLineChars    = 240
	MaxResults      = 2000
	MaxPerItem      = 100
	MaxItems        = 500
	MaxFilesVisited = 8000
	MaxFileBytes    = workspace.MaxFileBytes
	TimeLimit       = 4 * time.Second
	// stepBudget bounds one search's regex backtracking (Python has no such
	// bound; a pathological pattern then reports timed_out instead of hanging).
	stepBudget = 200_000_000
)

// Error is SearchError (400).
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

// InternalError is an exception Python does not catch here (500).
type InternalError struct{ Message string }

func (e *InternalError) Error() string { return e.Message }

// Options are the case / word / regex flags.
type Options struct{ Case, Word, Regex bool }

// reEscape is re.escape: every character outside [A-Za-z0-9_] that is
// special or ASCII punctuation/whitespace gets a backslash (3.7+ rules).
func reEscape(s string) string {
	const special = "()[]{}?*+-|^$\\.&~# \t\n\r\v\f"
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// BuildMatcher is build_matcher.
func BuildMatcher(query string, o Options) (*pyre.Regexp, error) {
	if pytext.Strip(query) == "" {
		return nil, &Error{"query is required"}
	}
	if pytext.Len(query) > MaxQuery {
		return nil, &Error{"query is too long"}
	}
	pattern := query
	if !o.Regex {
		pattern = reEscape(query)
	}
	if o.Word {
		pattern = `(?<!\w)(?:` + pattern + `)(?!\w)`
	}
	flags := pyre.FlagIgnoreCase
	if o.Case {
		flags = 0
	}
	re, err := pyre.Compile(pattern, flags)
	if err != nil {
		if e, ok := err.(*pyre.Error); ok {
			return nil, &Error{"invalid regular expression: " + e.Error()}
		}
		return nil, &InternalError{err.Error()}
	}
	if re.MatchesEmpty() {
		return nil, &Error{"the pattern matches empty text"}
	}
	return re, nil
}

type hit struct {
	line   int
	text   string
	ranges [][2]int
	offset int
}

func (h hit) object() *pyjson.Object {
	ranges := make([]any, len(h.ranges))
	for i, r := range h.ranges {
		ranges[i] = []any{int64(r[0]), int64(r[1])}
	}
	return pyjson.NewObject("line", h.line, "text", h.text, "ranges", ranges, "offset", h.offset)
}

// clip is _clip: a long line around its first match, ranges shifted.
func clip(line []rune, ranges [][2]int) (string, [][2]int, int) {
	if len(line) <= MaxLineChars {
		return string(line), ranges, 0
	}
	start := 0
	if len(ranges) > 0 {
		start = max(0, ranges[0][0]-60)
	}
	end := min(len(line), start+MaxLineChars)
	out := [][2]int{}
	for _, r := range ranges {
		if r[0] < end && r[1] > start {
			out = append(out, [2]int{max(0, r[0]-start), min(end, r[1]) - start})
		}
	}
	return string(line[start:end]), out, start
}

// lineMatches is line_matches; err is pyre.ErrBudget when the step budget
// ran out.
func lineMatches(text string, re *pyre.Regexp, limit int, budget *int) ([]hit, bool, error) {
	var hits []hit
	lines := pytext.SplitLines(text, false)
	if len(lines) == 0 {
		lines = []string{""}
	}
	for number, line := range lines {
		rs := []rune(line)
		found, err := re.FindAll(rs, budget)
		if err != nil {
			return hits, false, err
		}
		var ranges [][2]int
		for _, m := range found {
			if m.End > m.Start {
				ranges = append(ranges, [2]int{m.Start, m.End})
			}
		}
		if len(ranges) == 0 {
			continue
		}
		if len(ranges) > 20 {
			ranges = ranges[:20]
		}
		shown, shifted, offset := clip(rs, ranges)
		hits = append(hits, hit{number + 1, shown, shifted, offset})
		if len(hits) >= limit {
			return hits, true, nil
		}
	}
	return hits, false, nil
}

func patterns(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if t := pytext.Strip(item); t != "" {
			out = append(out, strings.ReplaceAll(t, `\`, "/"))
		}
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// normcase is os.path.normcase (ntpath in 3.10: "/" -> "\", lower()).
func normcase(s string) string {
	if runtime.GOOS == "windows" {
		return pytext.Lower(strings.ReplaceAll(s, "/", `\`))
	}
	return s
}

// fnmatch is fnmatch.fnmatch(name, pat).
func fnmatch(name, pat string) bool {
	return fnmatchcase([]rune(normcase(name)), []rune(normcase(pat)))
}

func fnmatchcase(name, pat []rune) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	switch pat[0] {
	case '*':
		for len(pat) > 0 && pat[0] == '*' {
			pat = pat[1:]
		}
		if len(pat) == 0 {
			return true
		}
		for i := 0; i <= len(name); i++ {
			if fnmatchcase(name[i:], pat) {
				return true
			}
		}
		return false
	case '?':
		return len(name) > 0 && fnmatchcase(name[1:], pat[1:])
	case '[':
		if set, rest, ok := charClass(pat); ok {
			return len(name) > 0 && set(name[0]) && fnmatchcase(name[1:], rest)
		}
		return len(name) > 0 && name[0] == '[' && fnmatchcase(name[1:], pat[1:])
	}
	return len(name) > 0 && name[0] == pat[0] && fnmatchcase(name[1:], pat[1:])
}

// charClass parses "[...]" like fnmatch.translate (no closing "]" = literal).
func charClass(pat []rune) (func(rune) bool, []rune, bool) {
	j := 1
	if j < len(pat) && pat[j] == '!' {
		j++
	}
	if j < len(pat) && pat[j] == ']' {
		j++
	}
	for j < len(pat) && pat[j] != ']' {
		j++
	}
	if j >= len(pat) {
		return nil, nil, false
	}
	body := pat[1:j]
	negate := len(body) > 0 && body[0] == '!'
	if negate {
		body = body[1:]
	}
	type rng struct{ lo, hi rune }
	var ranges []rng
	for k := 0; k < len(body); k++ {
		if k+2 < len(body) && body[k+1] == '-' {
			if body[k] <= body[k+2] {
				ranges = append(ranges, rng{body[k], body[k+2]})
			}
			k += 2
			continue
		}
		ranges = append(ranges, rng{body[k], body[k]})
	}
	match := func(r rune) bool {
		in := false
		for _, x := range ranges {
			if r >= x.lo && r <= x.hi {
				in = true
				break
			}
		}
		return in != negate
	}
	if len(ranges) == 0 && !negate {
		match = func(rune) bool { return false } // "(?!)": never matches
	}
	return match, pat[j+1:], true
}

// globMatch is _glob_match.
func globMatch(path, pattern string) bool {
	pattern = strings.Trim(pattern, "/")
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		for _, part := range strings.Split(path, "/") {
			if fnmatch(part, pattern) {
				return true
			}
		}
		return false
	}
	if fnmatch(path, pattern) || fnmatch(path, strings.ReplaceAll(pattern, "**/", "")) {
		return true
	}
	return strings.HasPrefix(path, strings.TrimRight(strings.TrimRight(pattern, "*"), "/")+"/")
}

func pathFilter(include, exclude string) func(string) bool {
	inc, exc := patterns(include), patterns(exclude)
	return func(path string) bool {
		if len(inc) > 0 {
			ok := false
			for _, p := range inc {
				if globMatch(path, p) {
					ok = true
					break
				}
			}
			if !ok {
				return false
			}
		}
		for _, p := range exc {
			if globMatch(path, p) {
				return false
			}
		}
		return true
	}
}

func elapsedMS(start time.Time) int64 {
	return int64(math.RoundToEven(float64(time.Since(start).Microseconds()) / 1000))
}

func lowerLess(a, b string) bool { return pytext.Lower(a) < pytext.Lower(b) }
