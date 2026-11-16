package chat

import (
	"strings"

	"arkstudio/go/internal/pyre"
)

// Python re flags used by chat.py.
const (
	reI = pyre.FlagIgnoreCase
	reM = pyre.FlagMultiline
	reS = pyre.FlagDotAll
)

// rx is a compiled Python pattern used on str values (code points).
type rx struct{ re *pyre.Regexp }

func compile(pattern string, flags int) rx { return rx{pyre.MustCompile(pattern, flags)} }

// fullCompile is a pattern for re.fullmatch.
func fullCompile(pattern string, flags int) rx {
	return compile(`\A(?:`+pattern+`)\Z`, flags)
}

const budget = 1 << 30

// groups is a match: the text of each group ("" when the group did not
// participate, see has) and the code point span of the match.
type groups struct {
	text       []string
	has        []bool
	start, end int
}

func (g *groups) group(n int) string { return g.text[n] }

func (r rx) at(s []rune, pos int) *groups {
	b := budget
	m, ok, err := r.re.SearchFrom(s, pos, -1, &b)
	if err != nil || !ok {
		return nil
	}
	return r.groupsOf(s, m)
}

func (r rx) groupsOf(s []rune, m pyre.Match) *groups {
	out := &groups{start: m.Start, end: m.End}
	for i := 0; i <= r.re.NumGroups(); i++ {
		start, end, ok := m.Group(i)
		text := ""
		if ok {
			text = string(s[start:end])
		}
		out.text = append(out.text, text)
		out.has = append(out.has, ok)
	}
	return out
}

// search is re.search(pattern, text).
func (r rx) search(text string) *groups { return r.at([]rune(text), 0) }

// finditer is re.finditer(pattern, text).
func (r rx) finditer(text string) []*groups {
	s := []rune(text)
	b := budget
	ms, _ := r.re.FindAll(s, &b)
	out := make([]*groups, len(ms))
	for i, m := range ms {
		out[i] = r.groupsOf(s, m)
	}
	return out
}

// sub is re.sub(pattern, repl, text) for a literal replacement.
func (r rx) sub(repl, text string) string {
	s := []rune(text)
	b := budget
	ms, _ := r.re.FindAll(s, &b)
	if len(ms) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, m := range ms {
		out.WriteString(string(s[last:m.Start]))
		out.WriteString(repl)
		last = m.End
	}
	out.WriteString(string(s[last:]))
	return out.String()
}

// findall is re.findall(pattern, text) for a pattern without groups.
func (r rx) findall(text string) []string {
	s := []rune(text)
	b := budget
	ms, _ := r.re.FindAll(s, &b)
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(s[m.Start:m.End])
	}
	return out
}
