package pyre

import (
	"errors"
	"unicode"
)

// ErrBudget is returned when a search exceeds its backtracking step budget
// (Python would keep running; the caller reports a timeout instead).
var ErrBudget = errors.New("pyre: step budget exceeded")

// Regexp is a compiled pattern.
type Regexp struct {
	items  []node
	flags  int
	groups int
	names  map[string]int
	// literal fast path: the whole pattern is one case-sensitive literal run
	literal []rune
}

// Compile is re.compile(pattern, flags) for a str pattern.
func Compile(pattern string, flags int) (*Regexp, error) {
	items, st, err := parse(pattern, flags)
	if err != nil {
		return nil, err
	}
	markCased(items)
	re := &Regexp{items: items, flags: st.flags, groups: st.groups(), names: st.groupdict}
	if err := re.check(items, st, st.flags); err != nil {
		return nil, err
	}
	if st.flags&(FlagIgnoreCase|FlagVerbose) == 0 {
		var run []rune
		simple := true
		for _, n := range items {
			if n.op != opLiteral {
				simple = false
				break
			}
			run = append(run, n.ch)
		}
		if simple && len(run) > 0 {
			re.literal = run
		}
	}
	return re, nil
}

// markCased precomputes _optimize_charset's hascased for every set.
func markCased(items []node) {
	for i := range items {
		n := &items[i]
		if n.op == opIn {
			members := n.set
			if len(members) > 0 && members[0].op == opNegate {
				members = members[1:]
			}
			n.cased = setHasCased(members)
		}
		markCased(n.body)
		markCased(n.no)
		for _, a := range n.alts {
			markCased(a)
		}
	}
}

// check is the part of sre_compile that can fail: fixed-width lookbehind
// and the template flag.
func (re *Regexp) check(items []node, st *state, flags int) error {
	for _, n := range items {
		switch n.op {
		case opMaxRepeat, opMinRepeat:
			if flags&FlagTemplate != 0 {
				name := "MAX_REPEAT"
				if n.op == opMinRepeat {
					name = "MIN_REPEAT"
				}
				return &Error{Msg: "internal: unsupported template operator " + name, Pos: -1}
			}
			if err := re.check(n.body, st, flags); err != nil {
				return err
			}
		case opSubpattern:
			if err := re.check(n.body, st, combineFlags(flags, n.add, n.del)); err != nil {
				return err
			}
		case opAssert, opAssertNot:
			if n.dir < 0 {
				if w := getwidth(st, n.body); w.lo != w.hi {
					return &Error{Msg: "look-behind requires fixed-width pattern", Pos: -1}
				}
			}
			if err := re.check(n.body, st, flags); err != nil {
				return err
			}
		case opBranch:
			for _, a := range n.alts {
				if err := re.check(a, st, flags); err != nil {
					return err
				}
			}
		case opGroupexists:
			if n.group >= st.groups() {
				// sre_compile emits a group index the C validator rejects.
				return &RuntimeError{"invalid SRE code"}
			}
			if err := re.check(n.body, st, flags); err != nil {
				return err
			}
			if err := re.check(n.no, st, flags); err != nil {
				return err
			}
		}
	}
	return nil
}

// RuntimeError is a Python RuntimeError raised by re.compile.
type RuntimeError struct{ Msg string }

func (e *RuntimeError) Error() string { return e.Msg }

func combineFlags(flags, add, del int) int {
	if add&typeFlags != 0 {
		flags &^= typeFlags
	}
	return (flags | add) &^ del
}

// ---------------------------------------------------------------- chars

// ignorecaseFixes is sre_compile._ignorecase_fixes.
var ignorecaseFixes = func() map[rune][]rune {
	eq := [][]rune{{0x69, 0x131}, {0x73, 0x17f}, {0xb5, 0x3bc}, {0x345, 0x3b9, 0x1fbe}, {0x390, 0x1fd3},
		{0x3b0, 0x1fe3}, {0x3b2, 0x3d0}, {0x3b5, 0x3f5}, {0x3b8, 0x3d1}, {0x3ba, 0x3f0}, {0x3c0, 0x3d6},
		{0x3c1, 0x3f1}, {0x3c2, 0x3c3}, {0x3c6, 0x3d5}, {0x1e61, 0x1e9b}, {0xfb05, 0xfb06}}
	out := map[rune][]rune{}
	for _, t := range eq {
		for _, i := range t {
			for _, j := range t {
				if j != i {
					out[i] = append(out[i], j)
				}
			}
		}
	}
	return out
}()

func lower(r rune) rune { return unicode.ToLower(r) }

func iscased(r rune) bool { return r != unicode.ToLower(r) || r != unicode.ToUpper(r) }

// isWord is sre's UNICODE word test (str.isalnum() or "_"), or ASCII.
func isWord(r rune, ascii bool) bool {
	if ascii {
		return r < 128 && (r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

func isDigit(r rune, ascii bool) bool {
	if ascii {
		return r >= '0' && r <= '9'
	}
	return unicode.Is(unicode.Nd, r)
}

func isSpace(r rune, ascii bool) bool {
	if ascii {
		return r == ' ' || r >= 9 && r <= 13
	}
	return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
}

func catMatch(c category, r rune, ascii bool) bool {
	switch c {
	case catDigit:
		return isDigit(r, ascii)
	case catNotDigit:
		return !isDigit(r, ascii)
	case catSpace:
		return isSpace(r, ascii)
	case catNotSpace:
		return !isSpace(r, ascii)
	case catWord:
		return isWord(r, ascii)
	case catNotWord:
		return !isWord(r, ascii)
	}
	return false
}

// literalMatch is LITERAL / LITERAL_UNI_IGNORE (+ the IN form for fixes).
func literalMatch(lit, r rune, ignore, ascii bool) bool {
	if !ignore || !iscased(lit) {
		return r == lit
	}
	if ascii {
		al := func(c rune) rune {
			if c >= 'A' && c <= 'Z' {
				return c + 32
			}
			return c
		}
		return al(r) == al(lit)
	}
	lo := lower(lit)
	lr := lower(r)
	if lr == lo {
		return true
	}
	for _, k := range ignorecaseFixes[lo] {
		if lr == k {
			return true
		}
	}
	return false
}

func setHasCased(set []node) bool {
	for _, m := range set {
		switch m.op {
		case opLiteral:
			if iscased(m.ch) {
				return true
			}
		case opRange:
			for c := m.lo; c <= m.hi; c++ {
				if iscased(c) {
					return true
				}
				if c-m.lo > 0x30000 {
					return true
				}
			}
		}
	}
	return false
}

// loweredMember: is c (already lowercased) in {lower(x) : x in member} ∪ fixes.
func loweredMember(m node, c rune) bool {
	inRange := func(x rune) bool {
		if m.op == opLiteral {
			return x == m.ch
		}
		return x >= m.lo && x <= m.hi
	}
	targets := append([]rune{c}, ignorecaseFixes[c]...)
	for _, t := range targets {
		// candidates x with lower(x) == t
		cands := []rune{t, unicode.ToUpper(t), unicode.ToTitle(t)}
		for f := unicode.SimpleFold(t); f != t; f = unicode.SimpleFold(f) {
			cands = append(cands, f)
		}
		for _, x := range cands {
			if inRange(x) && lower(x) == t {
				return true
			}
		}
		if t != c && m.op == opLiteral && lower(m.ch) == t {
			return true
		}
	}
	return false
}

func inMatch(set []node, cased bool, r rune, ignore, ascii bool) bool {
	negate := len(set) > 0 && set[0].op == opNegate
	members := set
	if negate {
		members = set[1:]
	}
	ok := false
	if ignore && cased {
		c := lower(r)
		if ascii {
			c = r
			if r >= 'A' && r <= 'Z' {
				c = r + 32
			}
		}
		for _, m := range members {
			switch m.op {
			case opCategory:
				ok = catMatch(m.cat, c, ascii)
			case opLiteral, opRange:
				if ascii {
					al := func(x rune) rune {
						if x >= 'A' && x <= 'Z' {
							return x + 32
						}
						return x
					}
					if m.op == opLiteral {
						ok = al(m.ch) == c
					} else {
						for x := m.lo; x <= m.hi && !ok; x++ {
							ok = al(x) == c
							if x-m.lo > 0x1000 {
								ok = c >= m.lo && c <= m.hi
								break
							}
						}
					}
				} else {
					ok = loweredMember(m, c)
				}
			}
			if ok {
				break
			}
		}
	} else {
		for _, m := range members {
			switch m.op {
			case opCategory:
				ok = catMatch(m.cat, r, ascii)
			case opLiteral:
				ok = m.ch == r
			case opRange:
				ok = r >= m.lo && r <= m.hi
			}
			if ok {
				break
			}
		}
	}
	return ok != negate
}

// ---------------------------------------------------------------- matcher

type matcher struct {
	s      []rune
	marks  []int // 2 per group, -1 = unset
	steps  int
	budget int
	depth  int
	err    error
}

// maxDepth bounds the recursion of the backtracking matcher (sre itself is
// not recursive; a pattern that would need more reports ErrBudget).
const maxDepth = 100000

type cont func(pos int) bool

func (m *matcher) tick() bool {
	m.steps++
	if m.steps > m.budget {
		m.err = ErrBudget
		return false
	}
	return true
}

// single reports whether n always consumes exactly one character.
func single(n node) bool {
	switch n.op {
	case opLiteral, opNotLiteral, opAny, opIn:
		return true
	}
	return false
}

func (m *matcher) one(n node, pos int, flags int) bool {
	if pos >= len(m.s) {
		return false
	}
	r := m.s[pos]
	ignore := flags&FlagIgnoreCase != 0
	ascii := flags&FlagASCII != 0
	switch n.op {
	case opLiteral:
		return literalMatch(n.ch, r, ignore, ascii)
	case opNotLiteral:
		return !literalMatch(n.ch, r, ignore, ascii)
	case opAny:
		return flags&FlagDotAll != 0 || r != '\n'
	case opIn:
		return inMatch(n.set, n.cased, r, ignore, ascii)
	}
	return false
}

func (m *matcher) at(kind atKind, pos int, flags int) bool {
	s := m.s
	ascii := flags&FlagASCII != 0
	switch kind {
	case atBeginning:
		if flags&FlagMultiline != 0 {
			return pos == 0 || s[pos-1] == '\n'
		}
		return pos == 0
	case atBeginningString:
		return pos == 0
	case atEnd:
		if flags&FlagMultiline != 0 {
			return pos == len(s) || s[pos] == '\n'
		}
		return pos == len(s) || (pos+1 == len(s) && s[pos] == '\n')
	case atEndString:
		return pos == len(s)
	case atBoundary, atNonBoundary:
		if len(s) == 0 {
			return false
		}
		that := pos > 0 && isWord(s[pos-1], ascii)
		this := pos < len(s) && isWord(s[pos], ascii)
		if kind == atBoundary {
			return this != that
		}
		return this == that
	}
	return false
}

// seq matches items[i:] at pos and then k.
func (m *matcher) seq(items []node, i, pos, flags int, k cont) bool {
	if !m.tick() {
		return false
	}
	m.depth++
	defer func() { m.depth-- }()
	if m.depth > maxDepth {
		m.err = ErrBudget
		return false
	}
	if i == len(items) {
		return k(pos)
	}
	n := items[i]
	next := func(p int) bool { return m.seq(items, i+1, p, flags, k) }
	switch n.op {
	case opLiteral, opNotLiteral, opAny, opIn:
		if !m.one(n, pos, flags) {
			return false
		}
		return next(pos + 1)
	case opAt:
		if !m.at(n.at, pos, flags) {
			return false
		}
		return next(pos)
	case opBranch:
		for _, alt := range n.alts {
			if m.seq(alt, 0, pos, flags, next) {
				return true
			}
			if m.err != nil {
				return false
			}
		}
		return false
	case opSubpattern:
		f := combineFlags(flags, n.add, n.del)
		if n.group == 0 {
			return m.seq(n.body, 0, pos, f, next)
		}
		g := n.group
		oldS, oldE := m.marks[2*g], m.marks[2*g+1]
		ok := m.seq(n.body, 0, pos, f, func(p int) bool {
			ps, pe := m.marks[2*g], m.marks[2*g+1]
			m.marks[2*g], m.marks[2*g+1] = pos, p
			if next(p) {
				return true
			}
			m.marks[2*g], m.marks[2*g+1] = ps, pe
			return false
		})
		if !ok {
			m.marks[2*g], m.marks[2*g+1] = oldS, oldE
		}
		return ok
	case opMaxRepeat, opMinRepeat:
		if len(n.body) == 1 && single(n.body[0]) {
			return m.repeatOne(n, pos, flags, next)
		}
		return m.repeat(n, 0, pos, -1, flags, next)
	case opGroupref:
		s, e := m.marks[2*n.group], m.marks[2*n.group+1]
		if s < 0 || e < 0 {
			return false
		}
		ln := e - s
		if pos+ln > len(m.s) {
			return false
		}
		ignore := flags&FlagIgnoreCase != 0
		for j := 0; j < ln; j++ {
			a, b := m.s[s+j], m.s[pos+j]
			if ignore {
				if !literalMatch(a, b, true, flags&FlagASCII != 0) {
					return false
				}
			} else if a != b {
				return false
			}
		}
		return next(pos + ln)
	case opGroupexists:
		if m.marks[2*n.group] >= 0 && m.marks[2*n.group+1] >= 0 {
			return m.seq(n.body, 0, pos, flags, next)
		}
		return m.seq(n.no, 0, pos, flags, next)
	case opAssert, opAssertNot:
		start := pos
		if n.dir < 0 {
			// fixed width (checked at compile time)
			w := m.fixedWidth(n.body)
			start = pos - w
			if start < 0 {
				return n.op == opAssertNot && next(pos)
			}
		}
		saved := append([]int(nil), m.marks...)
		ok := m.seq(n.body, 0, start, flags, func(p int) bool { return n.dir > 0 || p == pos })
		if m.err != nil {
			return false
		}
		if n.op == opAssertNot {
			copy(m.marks, saved)
			if ok {
				return false
			}
			return next(pos)
		}
		if !ok {
			copy(m.marks, saved)
			return false
		}
		if next(pos) {
			return true
		}
		copy(m.marks, saved)
		return false
	}
	return false
}

func (m *matcher) fixedWidth(items []node) int {
	st := &state{groupwidths: []*width{}}
	_ = st
	var w int
	for _, n := range items {
		switch n.op {
		case opLiteral, opNotLiteral, opAny, opIn:
			w++
		case opSubpattern:
			w += m.fixedWidth(n.body)
		case opMaxRepeat, opMinRepeat:
			w += int(n.min) * m.fixedWidth(n.body)
		case opBranch:
			if len(n.alts) > 0 {
				w += m.fixedWidth(n.alts[0])
			}
		case opGroupref:
			s, e := m.marks[2*n.group], m.marks[2*n.group+1]
			if s >= 0 && e >= s {
				w += e - s
			}
		case opGroupexists:
			w += m.fixedWidth(n.body)
		}
	}
	return w
}

// repeatOne is REPEAT_ONE / MIN_REPEAT_ONE for single-character items.
func (m *matcher) repeatOne(n node, pos, flags int, k cont) bool {
	item := n.body[0]
	limit := len(m.s) - pos
	if n.max < int64(limit) {
		limit = int(n.max)
	}
	if int64(limit) < n.min {
		// may still fail below; count matches
	}
	if n.op == opMaxRepeat {
		count := 0
		for count < limit && m.one(item, pos+count, flags) {
			count++
		}
		if int64(count) < n.min {
			return false
		}
		for c := count; c >= int(n.min); c-- {
			if !m.tick() {
				return false
			}
			if k(pos + c) {
				return true
			}
			if m.err != nil {
				return false
			}
		}
		return false
	}
	c := 0
	for ; int64(c) < n.min; c++ {
		if !m.one(item, pos+c, flags) {
			return false
		}
	}
	for {
		if !m.tick() {
			return false
		}
		if k(pos + c) {
			return true
		}
		if m.err != nil || c >= limit || !m.one(item, pos+c, flags) {
			return false
		}
		c++
	}
}

// repeat is REPEAT + MAX_UNTIL / MIN_UNTIL; last is the position where the
// previous iteration started (an empty iteration past min stops the loop).
func (m *matcher) repeat(n node, count, pos, last, flags int, k cont) bool {
	if !m.tick() {
		return false
	}
	iterate := func() bool {
		if int64(count) >= n.max {
			return false
		}
		if int64(count) >= n.min && pos == last {
			return false
		}
		return m.seq(n.body, 0, pos, flags, func(p int) bool {
			return m.repeat(n, count+1, p, pos, flags, k)
		})
	}
	if int64(count) < n.min {
		return iterate()
	}
	if n.op == opMaxRepeat {
		if iterate() {
			return true
		}
		if m.err != nil {
			return false
		}
		return k(pos)
	}
	if k(pos) {
		return true
	}
	if m.err != nil {
		return false
	}
	return iterate()
}

// Match is one finditer step result in code points.
type Match struct {
	Start, End int
	marks      []int // 2 per group (index 0 unused), -1 = unset; nil on the literal fast path
}

// Group returns the span of group g (Python m.span(g)); ok is false when
// the group did not participate.
func (m Match) Group(g int) (start, end int, ok bool) {
	if g == 0 {
		return m.Start, m.End, true
	}
	if 2*g+1 >= len(m.marks) || m.marks[2*g] < 0 || m.marks[2*g+1] < 0 {
		return -1, -1, false
	}
	return m.marks[2*g], m.marks[2*g+1], true
}

// GroupIndex is pattern.groupindex[name] (0, false when unknown).
func (re *Regexp) GroupIndex(name string) (int, bool) {
	g, ok := re.names[name]
	return g, ok
}

// MustCompile is Compile that panics (for package-level patterns).
func MustCompile(pattern string, flags int) *Regexp {
	re, err := Compile(pattern, flags)
	if err != nil {
		panic(err)
	}
	return re
}

// SearchFrom is pattern.search(s, pos) with the finditer must-advance rule:
// when noEmptyAt >= 0, an empty match at that position is skipped.
func (re *Regexp) SearchFrom(s []rune, pos, noEmptyAt int, budget *int) (Match, bool, error) {
	if re.literal != nil {
		n := len(re.literal)
	outer:
		for i := pos; i+n <= len(s); i++ {
			for j := 0; j < n; j++ {
				if s[i+j] != re.literal[j] {
					continue outer
				}
			}
			return Match{Start: i, End: i + n}, true, nil
		}
		return Match{}, false, nil
	}
	m := &matcher{s: s, marks: make([]int, 2*re.groups+2), budget: *budget}
	defer func() { *budget -= m.steps }()
	var first *node
	if len(re.items) > 0 && single(re.items[0]) {
		first = &re.items[0]
	}
	for start := pos; start <= len(s); start++ {
		if first != nil && !m.one(*first, start, re.flags) {
			continue
		}
		for i := range m.marks {
			m.marks[i] = -1
		}
		end := -1
		ok := m.seq(re.items, 0, start, re.flags, func(p int) bool {
			if p == start && start == noEmptyAt {
				return false
			}
			end = p
			return true
		})
		if m.err != nil {
			return Match{}, false, m.err
		}
		if ok {
			return Match{Start: start, End: end, marks: append([]int(nil), m.marks...)}, true, nil
		}
	}
	return Match{}, false, nil
}

// FindAll is [m.span() for m in pattern.finditer(s)].
func (re *Regexp) FindAll(s []rune, budget *int) ([]Match, error) {
	var out []Match
	pos, noEmpty := 0, -1
	for pos <= len(s) {
		mt, ok, err := re.SearchFrom(s, pos, noEmpty, budget)
		if err != nil {
			return out, err
		}
		if !ok {
			break
		}
		out = append(out, mt)
		pos = mt.End
		noEmpty = -1
		if mt.End == mt.Start {
			noEmpty = mt.End
		}
	}
	return out, nil
}

// MatchesEmpty is bool(pattern.search("")).
func (re *Regexp) MatchesEmpty() bool {
	budget := 1 << 20
	_, ok, _ := re.SearchFrom(nil, 0, -1, &budget)
	return ok
}

// NumGroups is pattern.groups (capturing groups, group 0 not counted).
func (re *Regexp) NumGroups() int { return re.groups - 1 }
