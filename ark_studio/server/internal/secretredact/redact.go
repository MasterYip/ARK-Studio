// Package secretredact ports arkstudio/secret_redact.py: credential shapes
// and caller-supplied literals in text become "[REDACTED:<kind>]".
//
// The patterns run on internal/compat/pyre so \b, \w, \s, \d and (?i) keep
// Python's Unicode semantics (Go's regexp is ASCII for \b and \w).
package secretredact

import (
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyre"
)

func mark(kind string) string { return "[REDACTED:" + kind + "]" }

type pattern struct {
	kind string
	re   *pyre.Regexp
}

var patterns = []pattern{
	{"private_key", pyre.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----`+
		`[\s\S]*?(?:-----END (?:RSA |EC |OPENSSH |DSA |ENCRYPTED )?PRIVATE KEY-----|$)`, 0)},
	{"bearer", pyre.MustCompile(`(?i)(?<=\bbearer )[A-Za-z0-9._~+/=-]{12,}`, 0)},
	{"ark_token", pyre.MustCompile(`\barka_[A-Za-z0-9_-]{16,}`, 0)},
	{"anthropic_key", pyre.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{12,}`, 0)},
	{"openai_key", pyre.MustCompile(`\bsk-(?:proj-)?[A-Za-z0-9_-]{20,}`, 0)},
	{"github_token", pyre.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{30,})`, 0)},
	{"aws_key", pyre.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`, 0)},
	{"slack_token", pyre.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`, 0)},
	{"google_key", pyre.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`, 0)},
	{"jwt", pyre.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`, 0)},
}

var assignment = pyre.MustCompile(`(?ix)
    (?P<key>\b[\w.-]*(?:password|passwd|api[_-]?key|secret|access[_-]?token|refresh[_-]?token|auth[_-]?token)[\w-]*["']?)
    (?P<sep>\s*[:=]\s*)
    (?P<quote>["']?)
    (?P<value>[^\s"'`+"`"+`,;}{]{8,})
    `, 0)

var (
	placeholder = pyre.MustCompile(`^(?:\[REDACTED|<|\$\{?|env:|\*{3,}|x{6,}|\.\.\.)`, pyre.FlagIgnoreCase)
	digitRe     = pyre.MustCompile(`\d`, 0)
	letterRe    = pyre.MustCompile(`[A-Za-z]`, 0)
)

// budget is effectively unbounded: Python never gives up on a pattern.
const budget = 1 << 62

func search(re *pyre.Regexp, s []rune, pos int) (pyre.Match, bool) {
	b := budget
	m, ok, _ := re.SearchFrom(s, pos, -1, &b)
	return m, ok
}

// subFunc is pattern.sub(fn, text) for patterns that never match empty.
func subFunc(re *pyre.Regexp, text string, fn func(s []rune, m pyre.Match) string) (string, int) {
	s := []rune(text)
	var out strings.Builder
	last, n := 0, 0
	for pos := 0; pos <= len(s); {
		m, ok := search(re, s, pos)
		if !ok {
			break
		}
		out.WriteString(string(s[last:m.Start]))
		out.WriteString(fn(s, m))
		n++
		last = m.End
		if m.End == m.Start {
			if m.End < len(s) {
				out.WriteRune(s[m.End])
			}
			last = m.End + 1
			pos = m.End + 1
		} else {
			pos = m.End
		}
	}
	if n == 0 {
		return text, 0
	}
	if last < len(s) {
		out.WriteString(string(s[last:]))
	}
	return out.String(), n
}

// Text is redact_text(text, extra).
func Text(text string, extra []string) (string, int) {
	if text == "" {
		return text, 0
	}
	count := 0
	for _, literal := range extra {
		if len([]rune(literal)) >= 8 && strings.Contains(text, literal) {
			count += strings.Count(text, literal)
			text = strings.ReplaceAll(text, literal, mark("server_token"))
		}
	}
	for _, p := range patterns {
		repl := mark(p.kind)
		var n int
		text, n = subFunc(p.re, text, func([]rune, pyre.Match) string { return repl })
		count += n
	}
	gk, _ := assignment.GroupIndex("key")
	gs, _ := assignment.GroupIndex("sep")
	gq, _ := assignment.GroupIndex("quote")
	gv, _ := assignment.GroupIndex("value")
	group := func(s []rune, m pyre.Match, g int) string {
		a, b, ok := m.Group(g)
		if !ok {
			return ""
		}
		return string(s[a:b])
	}
	text, _ = subFunc(assignment, text, func(s []rune, m pyre.Match) string {
		value := []rune(group(s, m, gv))
		_, isPlaceholder := search(placeholder, value, 0)
		sv := string(value)
		_, hasDigit := search(digitRe, value, 0)
		_, hasLetter := search(letterRe, value, 0)
		if isPlaceholder || strings.Contains(sv, "(") || strings.HasPrefix(sv, "process.") ||
			strings.HasPrefix(sv, "os.") || !(hasDigit && hasLetter) {
			return string(s[m.Start:m.End])
		}
		count++
		return group(s, m, gk) + group(s, m, gs) + group(s, m, gq) + mark("credential")
	})
	return text, count
}

// Value is redact_value(value, extra): every string inside a JSON-like
// value (object keys untouched).
func Value(value any, extra []string) (any, int) {
	switch v := value.(type) {
	case string:
		return Text(v, extra)
	case []any:
		total := 0
		out := make([]any, len(v))
		for i, item := range v {
			var n int
			out[i], n = Value(item, extra)
			total += n
		}
		return out, total
	case *pyjson.Object:
		total := 0
		out := pyjson.NewObject()
		for _, key := range v.Keys() {
			item, n := Value(v.Value(key), extra)
			out.Set(key, item)
			total += n
		}
		return out, total
	}
	return value, 0
}
