// Package pyre is Python 3.10's “re“ for str patterns: sre_parse.py ported
// line by line (same parse tree, same error messages and positions) plus a
// backtracking matcher with sre's semantics (Unicode \w \d \s \b, IGNORECASE
// with sre_compile's case fixes, lookaround, backreferences, lazy repeats).
// Workbench Search compiles user patterns with it (arkstudio/search.py).
package pyre

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"arkstudio/go/internal/pyjson"
)

// Flags (sre_constants).
const (
	FlagTemplate   = 1
	FlagIgnoreCase = 2
	FlagLocale     = 4
	FlagMultiline  = 8
	FlagDotAll     = 16
	FlagUnicode    = 32
	FlagVerbose    = 64
	FlagASCII      = 256

	typeFlags   = FlagASCII | FlagLocale | FlagUnicode
	globalFlags = FlagTemplate // | DEBUG (128), not reachable inline

	// MaxRepeat is _sre.MAXREPEAT on 64-bit builds.
	MaxRepeat = 4294967295
	maxGroups = 1073741823 // MAXGROUPS (64-bit): 2**31 // 2 - 1
)

var flagChars = map[rune]int{'i': FlagIgnoreCase, 'L': FlagLocale, 'm': FlagMultiline, 's': FlagDotAll,
	'x': FlagVerbose, 'a': FlagASCII, 't': FlagTemplate, 'u': FlagUnicode}

// Error is re.error: str() is "msg at position N" (+ line/column when the
// pattern has a newline), or just msg without a position.
type Error struct {
	Msg     string
	Pattern []rune
	Pos     int // -1: no position
}

func (e *Error) Error() string {
	if e.Pos < 0 {
		return e.Msg
	}
	msg := fmt.Sprintf("%s at position %d", e.Msg, e.Pos)
	nl := false
	line, lastNL := 1, -1
	for i, r := range e.Pattern {
		if r == '\n' {
			nl = true
			if i < e.Pos {
				line++
				lastNL = i
			}
		}
	}
	if nl {
		msg += fmt.Sprintf(" (line %d, column %d)", line, e.Pos-lastNL)
	}
	return msg
}

// OverflowError is Python's OverflowError("the repetition number is too large").
type OverflowError struct{ Msg string }

func (e *OverflowError) Error() string { return e.Msg }

type opcode int

const (
	opLiteral opcode = iota
	opNotLiteral
	opAny
	opIn
	opAt
	opBranch
	opSubpattern
	opMaxRepeat
	opMinRepeat
	opGroupref
	opGroupexists
	opAssert
	opAssertNot
	// set members
	opRange
	opCategory
	opNegate
)

type atKind int

const (
	atBeginning atKind = iota
	atBeginningString
	atBoundary
	atNonBoundary
	atEnd
	atEndString
)

type category int

const (
	catDigit category = iota
	catNotDigit
	catSpace
	catNotSpace
	catWord
	catNotWord
)

type node struct {
	op       opcode
	ch       rune     // literal / not literal
	lo, hi   rune     // range
	cat      category // category
	at       atKind
	set      []node // IN members
	alts     [][]node
	group    int // subpattern group (0 = none), groupref, groupexists
	add, del int
	body     []node
	no       []node // groupexists else branch (nil = none)
	hasNo    bool
	min, max int64
	dir      int  // assert direction (1 ahead, -1 behind)
	cased    bool // IN: the set has a cased member (computed by Compile)
}

// equal is tuple equality of parse items (prefix factoring, _uniq).
func (n node) equal(m node) bool {
	if n.op != m.op || n.ch != m.ch || n.lo != m.lo || n.hi != m.hi || n.cat != m.cat || n.at != m.at ||
		n.group != m.group || n.add != m.add || n.del != m.del || n.min != m.min || n.max != m.max ||
		n.dir != m.dir || n.hasNo != m.hasNo || len(n.set) != len(m.set) || len(n.alts) != len(m.alts) ||
		len(n.body) != len(m.body) || len(n.no) != len(m.no) {
		return false
	}
	eq := func(a, b []node) bool {
		for i := range a {
			if !a[i].equal(b[i]) {
				return false
			}
		}
		return true
	}
	if !eq(n.set, m.set) || !eq(n.body, m.body) || !eq(n.no, m.no) {
		return false
	}
	for i := range n.alts {
		if len(n.alts[i]) != len(m.alts[i]) || !eq(n.alts[i], m.alts[i]) {
			return false
		}
	}
	return true
}

type width struct{ lo, hi int64 }

type state struct {
	flags           int
	groupdict       map[string]int
	groupwidths     []*width
	lookbehindgroup int // -1 = None
}

func (s *state) groups() int { return len(s.groupwidths) }

func (s *state) checkgroup(gid int) bool { return gid < s.groups() && s.groupwidths[gid] != nil }

func (s *state) checklookbehindgroup(gid int, src *tokenizer) error {
	if s.lookbehindgroup >= 0 {
		if !s.checkgroup(gid) {
			return src.error("cannot refer to an open group", 0)
		}
		if gid >= s.lookbehindgroup {
			return src.error("cannot refer to group defined in the same lookbehind subpattern", 0)
		}
	}
	return nil
}

func getwidth(s *state, items []node) width {
	var lo, hi int64
	for _, n := range items {
		switch n.op {
		case opBranch:
			i, j := int64(MaxRepeat-1), int64(0)
			for _, a := range n.alts {
				w := getwidth(s, a)
				i, j = min(i, w.lo), max(j, w.hi)
			}
			lo, hi = lo+i, hi+j
		case opSubpattern:
			w := getwidth(s, n.body)
			lo, hi = lo+w.lo, hi+w.hi
		case opMaxRepeat, opMinRepeat:
			w := getwidth(s, n.body)
			lo, hi = lo+w.lo*n.min, hi+w.hi*n.max
		case opAny, opIn, opLiteral, opNotLiteral:
			lo, hi = lo+1, hi+1
		case opGroupref:
			w := s.groupwidths[n.group]
			lo, hi = lo+w.lo, hi+w.hi
		case opGroupexists:
			w := getwidth(s, n.body)
			i, j := w.lo, w.hi
			if n.hasNo {
				w2 := getwidth(s, n.no)
				i, j = min(i, w2.lo), max(j, w2.hi)
			} else {
				i = 0
			}
			lo, hi = lo+i, hi+j
		}
		// Python ints do not overflow; clamp like the final min() does.
		if hi > MaxRepeat*4 || hi < 0 {
			hi = MaxRepeat * 4
		}
		if lo > MaxRepeat*4 || lo < 0 {
			lo = MaxRepeat * 4
		}
	}
	return width{min(lo, MaxRepeat-1), min(hi, MaxRepeat)}
}

type tokenizer struct {
	s     []rune
	index int
	next  string // "" = None
	err   *Error
}

func newTokenizer(s []rune) (*tokenizer, error) {
	t := &tokenizer{s: s}
	if err := t.advance(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *tokenizer) advance() error {
	index := t.index
	if index >= len(t.s) {
		t.next = ""
		return nil
	}
	char := string(t.s[index])
	if t.s[index] == '\\' {
		index++
		if index >= len(t.s) {
			return &Error{"bad escape (end of pattern)", t.s, len(t.s) - 1}
		}
		char += string(t.s[index])
	}
	t.index = index + 1
	t.next = char
	return nil
}

// must wraps advance for call sites that cannot fail mid-token (the error
// is kept and returned by the parser's next check).
func (t *tokenizer) match(char string) (bool, error) {
	if char == t.next {
		return true, t.advance()
	}
	return false, nil
}

func (t *tokenizer) get() (string, error) {
	this := t.next
	return this, t.advance()
}

func (t *tokenizer) getwhile(n int, charset string) (string, error) {
	var b strings.Builder
	for i := 0; i < n; i++ {
		c := t.next
		if c == "" || len([]rune(c)) != 1 || !strings.Contains(charset, c) {
			break
		}
		b.WriteString(c)
		if err := t.advance(); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func (t *tokenizer) getuntil(terminator, name string) (string, error) {
	var result []rune
	for {
		c := t.next
		if err := t.advance(); err != nil {
			return "", err
		}
		if c == "" {
			if len(result) == 0 {
				return "", t.error("missing "+name, 0)
			}
			return "", t.error(fmt.Sprintf("missing %s, unterminated name", terminator), len(result))
		}
		if c == terminator {
			if len(result) == 0 {
				return "", t.error("missing "+name, 1)
			}
			break
		}
		result = append(result, []rune(c)...)
	}
	return string(result), nil
}

func (t *tokenizer) tell() int { return t.index - len([]rune(t.next)) }

func (t *tokenizer) seek(index int) error {
	t.index = index
	return t.advance()
}

func (t *tokenizer) error(msg string, offset int) *Error {
	return &Error{msg, t.s, t.tell() - offset}
}

func rlen(s string) int { return len([]rune(s)) }

const (
	specialChars = ".\\[{()*+?^$|"
	repeatChars  = "*+?{"
	digits       = "0123456789"
	octdigits    = "01234567"
	hexdigits    = "0123456789abcdefABCDEF"
	whitespace   = " \t\n\r\v\f"
)

func isASCIILetter(c rune) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func one(s string) rune {
	r := []rune(s)
	if len(r) == 0 {
		return 0
	}
	return r[0]
}

var escapes = map[string]rune{`\a`: 7, `\b`: 8, `\f`: 12, `\n`: 10, `\r`: 13, `\t`: 9, `\v`: 11, `\\`: '\\'}

func categoryEscape(escape string) (node, bool) {
	switch escape {
	case `\A`:
		return node{op: opAt, at: atBeginningString}, true
	case `\b`:
		return node{op: opAt, at: atBoundary}, true
	case `\B`:
		return node{op: opAt, at: atNonBoundary}, true
	case `\Z`:
		return node{op: opAt, at: atEndString}, true
	case `\d`:
		return node{op: opIn, set: []node{{op: opCategory, cat: catDigit}}}, true
	case `\D`:
		return node{op: opIn, set: []node{{op: opCategory, cat: catNotDigit}}}, true
	case `\s`:
		return node{op: opIn, set: []node{{op: opCategory, cat: catSpace}}}, true
	case `\S`:
		return node{op: opIn, set: []node{{op: opCategory, cat: catNotSpace}}}, true
	case `\w`:
		return node{op: opIn, set: []node{{op: opCategory, cat: catWord}}}, true
	case `\W`:
		return node{op: opIn, set: []node{{op: opCategory, cat: catNotWord}}}, true
	}
	return node{}, false
}

func lit(c rune) node { return node{op: opLiteral, ch: c} }

// numericEscape handles \x \u \U \N and the octal forms shared by both
// escape parsers; ok=false means "not handled here".
func hexEscape(src *tokenizer, escape string, n int) (node, error) {
	more, err := src.getwhile(n, hexdigits)
	if err != nil {
		return node{}, err
	}
	escape += more
	if rlen(escape) != n+2 {
		return node{}, src.error("incomplete escape "+escape, rlen(escape))
	}
	v, _ := strconv.ParseInt(escape[2:], 16, 64)
	if v > unicode.MaxRune {
		return node{}, src.error("bad escape "+escape, rlen(escape)) // chr() ValueError
	}
	return lit(rune(v)), nil
}

func namedEscape(src *tokenizer) (node, error) {
	ok, err := src.match("{")
	if err != nil {
		return node{}, err
	}
	if !ok {
		return node{}, src.error("missing {", 0)
	}
	name, err := src.getuntil("}", "character name")
	if err != nil {
		return node{}, err
	}
	if r, found := lookupName(name); found {
		return lit(r), nil
	}
	return node{}, src.error("undefined character name "+pyjson.Repr(name), rlen(name)+rlen(`\N{}`))
}

func classEscape(src *tokenizer, escape string) (node, error) {
	if c, ok := escapes[escape]; ok {
		return lit(c), nil
	}
	if n, ok := categoryEscape(escape); ok && n.op == opIn {
		return n, nil
	}
	c := []rune(escape)[1]
	switch {
	case c == 'x':
		return hexEscape(src, escape, 2)
	case c == 'u':
		return hexEscape(src, escape, 4)
	case c == 'U':
		return hexEscape(src, escape, 8)
	case c == 'N':
		return namedEscape(src)
	case strings.ContainsRune(octdigits, c):
		more, err := src.getwhile(2, octdigits)
		if err != nil {
			return node{}, err
		}
		escape += more
		v, _ := strconv.ParseInt(escape[1:], 8, 64)
		if v > 0o377 {
			return node{}, src.error(fmt.Sprintf("octal escape value %s outside of range 0-0o377", escape), rlen(escape))
		}
		return lit(rune(v)), nil
	case strings.ContainsRune(digits, c):
		return node{}, src.error("bad escape "+escape, rlen(escape))
	}
	if rlen(escape) == 2 {
		if isASCIILetter(c) {
			return node{}, src.error("bad escape "+escape, rlen(escape))
		}
		return lit(c), nil
	}
	return node{}, src.error("bad escape "+escape, rlen(escape))
}

func escapeItem(src *tokenizer, escape string, st *state) (node, error) {
	if n, ok := categoryEscape(escape); ok {
		return n, nil
	}
	if c, ok := escapes[escape]; ok {
		return lit(c), nil
	}
	c := []rune(escape)[1]
	switch {
	case c == 'x':
		return hexEscape(src, escape, 2)
	case c == 'u':
		return hexEscape(src, escape, 4)
	case c == 'U':
		return hexEscape(src, escape, 8)
	case c == 'N':
		return namedEscape(src)
	case c == '0':
		more, err := src.getwhile(2, octdigits)
		if err != nil {
			return node{}, err
		}
		v, _ := strconv.ParseInt(escape[1:]+more, 8, 64)
		return lit(rune(v)), nil
	case strings.ContainsRune(digits, c):
		if src.next != "" && strings.Contains(digits, src.next) && rlen(src.next) == 1 {
			nx, err := src.get()
			if err != nil {
				return node{}, err
			}
			escape += nx
			r := []rune(escape)
			if strings.ContainsRune(octdigits, r[1]) && strings.ContainsRune(octdigits, r[2]) &&
				src.next != "" && rlen(src.next) == 1 && strings.Contains(octdigits, src.next) {
				nx, err := src.get()
				if err != nil {
					return node{}, err
				}
				escape += nx
				v, _ := strconv.ParseInt(escape[1:], 8, 64)
				if v > 0o377 {
					return node{}, src.error(fmt.Sprintf("octal escape value %s outside of range 0-0o377", escape), rlen(escape))
				}
				return lit(rune(v)), nil
			}
		}
		group, _ := strconv.Atoi(escape[1:])
		if group < st.groups() {
			if !st.checkgroup(group) {
				return node{}, src.error("cannot refer to an open group", rlen(escape))
			}
			if err := st.checklookbehindgroup(group, src); err != nil {
				return node{}, err
			}
			return node{op: opGroupref, group: group}, nil
		}
		return node{}, src.error(fmt.Sprintf("invalid group reference %d", group), rlen(escape)-1)
	}
	if rlen(escape) == 2 {
		if isASCIILetter(c) {
			return node{}, src.error("bad escape "+escape, rlen(escape))
		}
		return lit(c), nil
	}
	return node{}, src.error("bad escape "+escape, rlen(escape))
}

func uniq(items []node) []node {
	var out []node
	for _, n := range items {
		dup := false
		for _, m := range out {
			if m.equal(n) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, n)
		}
	}
	return out
}

type errVerbose struct{}

func (errVerbose) Error() string { return "verbose" }

func parseSub(src *tokenizer, st *state, verbose bool, nested int) ([]node, error) {
	var items [][]node
	for {
		item, err := parseSeq(src, st, verbose, nested+1, nested == 0 && len(items) == 0)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		ok, err := src.match("|")
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
	}
	if len(items) == 1 {
		return items[0], nil
	}
	var sub []node
	for {
		var prefix *node
		common := true
		for _, item := range items {
			if len(item) == 0 {
				common = false
				break
			}
			if prefix == nil {
				p := item[0]
				prefix = &p
			} else if !item[0].equal(*prefix) {
				common = false
				break
			}
		}
		if !common || prefix == nil {
			break
		}
		for i := range items {
			items[i] = items[i][1:]
		}
		sub = append(sub, *prefix)
	}
	var set []node
	isSet := true
	for _, item := range items {
		if len(item) != 1 {
			isSet = false
			break
		}
		n := item[0]
		if n.op == opLiteral {
			set = append(set, n)
		} else if n.op == opIn && n.set[0].op != opNegate {
			set = append(set, n.set...)
		} else {
			isSet = false
			break
		}
	}
	if isSet {
		return append(sub, node{op: opIn, set: uniq(set)}), nil
	}
	return append(sub, node{op: opBranch, alts: items}), nil
}

func parseSeq(src *tokenizer, st *state, verbose bool, nested int, first bool) ([]node, error) {
	var sub []node
	for {
		this := src.next
		if this == "" || this == "|" || this == ")" {
			break
		}
		if _, err := src.get(); err != nil {
			return nil, err
		}
		if verbose {
			if rlen(this) == 1 && strings.Contains(whitespace, this) {
				continue
			}
			if this == "#" {
				for {
					c, err := src.get()
					if err != nil {
						return nil, err
					}
					if c == "" || c == "\n" {
						break
					}
				}
				continue
			}
		}
		switch {
		case this[0] == '\\':
			n, err := escapeItem(src, this, st)
			if err != nil {
				return nil, err
			}
			sub = append(sub, n)
		case !strings.Contains(specialChars, this):
			sub = append(sub, lit(one(this)))
		case this == "[":
			here := src.tell() - 1
			var set []node
			negate, err := src.match("^")
			if err != nil {
				return nil, err
			}
			for {
				this, err := src.get()
				if err != nil {
					return nil, err
				}
				if this == "" {
					return nil, src.error("unterminated character set", src.tell()-here)
				}
				if this == "]" && len(set) > 0 {
					break
				}
				var code1 node
				if this[0] == '\\' {
					if code1, err = classEscape(src, this); err != nil {
						return nil, err
					}
				} else {
					code1 = lit(one(this))
				}
				dash, err := src.match("-")
				if err != nil {
					return nil, err
				}
				if dash {
					that, err := src.get()
					if err != nil {
						return nil, err
					}
					if that == "" {
						return nil, src.error("unterminated character set", src.tell()-here)
					}
					if that == "]" {
						if code1.op == opIn {
							code1 = code1.set[0]
						}
						set = append(set, code1, lit('-'))
						break
					}
					var code2 node
					if that[0] == '\\' {
						if code2, err = classEscape(src, that); err != nil {
							return nil, err
						}
					} else {
						code2 = lit(one(that))
					}
					msg := fmt.Sprintf("bad character range %s-%s", this, that)
					if code1.op != opLiteral || code2.op != opLiteral {
						return nil, src.error(msg, rlen(this)+1+rlen(that))
					}
					if code2.ch < code1.ch {
						return nil, src.error(msg, rlen(this)+1+rlen(that))
					}
					set = append(set, node{op: opRange, lo: code1.ch, hi: code2.ch})
				} else {
					if code1.op == opIn {
						code1 = code1.set[0]
					}
					set = append(set, code1)
				}
			}
			set = uniq(set)
			if len(set) == 1 && set[0].op == opLiteral {
				if negate {
					sub = append(sub, node{op: opNotLiteral, ch: set[0].ch})
				} else {
					sub = append(sub, set[0])
				}
			} else {
				if negate {
					set = append([]node{{op: opNegate}}, set...)
				}
				sub = append(sub, node{op: opIn, set: set})
			}
		case strings.Contains(repeatChars, this):
			here := src.tell()
			var lo, hi int64
			switch this {
			case "?":
				lo, hi = 0, 1
			case "*":
				lo, hi = 0, MaxRepeat
			case "+":
				lo, hi = 1, MaxRepeat
			case "{":
				if src.next == "}" {
					sub = append(sub, lit('{'))
					continue
				}
				lo, hi = 0, MaxRepeat
				var los, his string
				for src.next != "" && rlen(src.next) == 1 && strings.Contains(digits, src.next) {
					c, err := src.get()
					if err != nil {
						return nil, err
					}
					los += c
				}
				comma, err := src.match(",")
				if err != nil {
					return nil, err
				}
				if comma {
					for src.next != "" && rlen(src.next) == 1 && strings.Contains(digits, src.next) {
						c, err := src.get()
						if err != nil {
							return nil, err
						}
						his += c
					}
				} else {
					his = los
				}
				closed, err := src.match("}")
				if err != nil {
					return nil, err
				}
				if !closed {
					sub = append(sub, lit('{'))
					if err := src.seek(here); err != nil {
						return nil, err
					}
					continue
				}
				if los != "" {
					v, err := strconv.ParseInt(los, 10, 64)
					if err != nil || v >= MaxRepeat {
						return nil, &OverflowError{"the repetition number is too large"}
					}
					lo = v
				}
				if his != "" {
					v, err := strconv.ParseInt(his, 10, 64)
					if err != nil || v >= MaxRepeat {
						return nil, &OverflowError{"the repetition number is too large"}
					}
					hi = v
					if hi < lo {
						return nil, src.error("min repeat greater than max repeat", src.tell()-here)
					}
				}
			}
			if len(sub) == 0 || sub[len(sub)-1].op == opAt {
				return nil, src.error("nothing to repeat", src.tell()-here+rlen(this))
			}
			last := sub[len(sub)-1]
			if last.op == opMaxRepeat || last.op == opMinRepeat {
				return nil, src.error("multiple repeat", src.tell()-here+rlen(this))
			}
			item := []node{last}
			if last.op == opSubpattern && last.group == 0 && last.add == 0 && last.del == 0 {
				item = last.body
			}
			lazy, err := src.match("?")
			if err != nil {
				return nil, err
			}
			op := opMaxRepeat
			if lazy {
				op = opMinRepeat
			}
			sub[len(sub)-1] = node{op: op, min: lo, max: hi, body: item}
		case this == ".":
			sub = append(sub, node{op: opAny})
		case this == "(":
			n, skip, err := parseGroup(src, st, verbose, nested, first, sub)
			if err != nil {
				return nil, err
			}
			if !skip {
				sub = append(sub, n)
			}
		case this == "^":
			sub = append(sub, node{op: opAt, at: atBeginning})
		case this == "$":
			sub = append(sub, node{op: opAt, at: atEnd})
		}
	}
	// unpack non-capturing groups
	for i := len(sub) - 1; i >= 0; i-- {
		n := sub[i]
		if n.op == opSubpattern && n.group == 0 && n.add == 0 && n.del == 0 {
			rest := append(append([]node(nil), n.body...), sub[i+1:]...)
			sub = append(sub[:i], rest...)
		}
	}
	return sub, nil
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		start := r == '_' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r)
		if i == 0 && !start {
			return false
		}
		if i > 0 && !start && !unicode.IsDigit(r) && !unicode.Is(unicode.Mn, r) && !unicode.Is(unicode.Mc, r) &&
			!unicode.Is(unicode.Pc, r) && !unicode.Is(unicode.Nd, r) {
			return false
		}
	}
	return true
}

// parseGroup handles "(" ... ; skip=true when nothing is appended (comment,
// global flags).
func parseGroup(src *tokenizer, st *state, verbose bool, nested int, first bool, sub []node) (node, bool, error) {
	start := src.tell() - 1
	group := true
	name := ""
	hasName := false
	addFlags, delFlags := 0, 0
	q, err := src.match("?")
	if err != nil {
		return node{}, false, err
	}
	if q {
		char, err := src.get()
		if err != nil {
			return node{}, false, err
		}
		switch {
		case char == "":
			return node{}, false, src.error("unexpected end of pattern", 0)
		case char == "P":
			lt, err := src.match("<")
			if err != nil {
				return node{}, false, err
			}
			eq := false
			if !lt {
				if eq, err = src.match("="); err != nil {
					return node{}, false, err
				}
			}
			switch {
			case lt:
				if name, err = src.getuntil(">", "group name"); err != nil {
					return node{}, false, err
				}
				hasName = true
				if !isIdentifier(name) {
					return node{}, false, src.error("bad character in group name "+pyjson.Repr(name), rlen(name)+1)
				}
			case eq:
				name, err := src.getuntil(")", "group name")
				if err != nil {
					return node{}, false, err
				}
				if !isIdentifier(name) {
					return node{}, false, src.error("bad character in group name "+pyjson.Repr(name), rlen(name)+1)
				}
				gid, ok := st.groupdict[name]
				if !ok {
					return node{}, false, src.error("unknown group name "+pyjson.Repr(name), rlen(name)+1)
				}
				if !st.checkgroup(gid) {
					return node{}, false, src.error("cannot refer to an open group", rlen(name)+1)
				}
				if err := st.checklookbehindgroup(gid, src); err != nil {
					return node{}, false, err
				}
				return node{op: opGroupref, group: gid}, false, nil
			default:
				c, err := src.get()
				if err != nil {
					return node{}, false, err
				}
				if c == "" {
					return node{}, false, src.error("unexpected end of pattern", 0)
				}
				return node{}, false, src.error("unknown extension ?P"+c, rlen(c)+2)
			}
		case char == ":":
			group = false
		case char == "#":
			for {
				if src.next == "" {
					return node{}, false, src.error("missing ), unterminated comment", src.tell()-start)
				}
				c, err := src.get()
				if err != nil {
					return node{}, false, err
				}
				if c == ")" {
					break
				}
			}
			return node{}, true, nil
		case char == "=" || char == "!" || char == "<":
			dir := 1
			saved := -2
			if char == "<" {
				c, err := src.get()
				if err != nil {
					return node{}, false, err
				}
				if c == "" {
					return node{}, false, src.error("unexpected end of pattern", 0)
				}
				if c != "=" && c != "!" {
					return node{}, false, src.error("unknown extension ?<"+c, rlen(c)+2)
				}
				char = c
				dir = -1
				saved = st.lookbehindgroup
				if saved < 0 {
					st.lookbehindgroup = st.groups()
				}
			}
			p, err := parseSub(src, st, verbose, nested+1)
			if err != nil {
				return node{}, false, err
			}
			if dir < 0 && saved < 0 {
				st.lookbehindgroup = -1
			}
			closed, err := src.match(")")
			if err != nil {
				return node{}, false, err
			}
			if !closed {
				return node{}, false, src.error("missing ), unterminated subpattern", src.tell()-start)
			}
			op := opAssert
			if char == "!" {
				op = opAssertNot
			}
			return node{op: op, dir: dir, body: p}, false, nil
		case char == "(":
			condname, err := src.getuntil(")", "group name")
			if err != nil {
				return node{}, false, err
			}
			var condgroup int
			if isIdentifier(condname) {
				g, ok := st.groupdict[condname]
				if !ok {
					return node{}, false, src.error("unknown group name "+pyjson.Repr(condname), rlen(condname)+1)
				}
				condgroup = g
			} else {
				v, err := strconv.Atoi(strings.TrimSpace(condname))
				if err != nil || v < 0 || !pyIntText(condname) {
					return node{}, false, src.error("bad character in group name "+pyjson.Repr(condname), rlen(condname)+1)
				}
				if v == 0 {
					return node{}, false, src.error("bad group number", rlen(condname)+1)
				}
				if v >= maxGroups {
					return node{}, false, src.error(fmt.Sprintf("invalid group reference %d", v), rlen(condname)+1)
				}
				condgroup = v
			}
			if err := st.checklookbehindgroup(condgroup, src); err != nil {
				return node{}, false, err
			}
			yes, err := parseSeq(src, st, verbose, nested+1, false)
			if err != nil {
				return node{}, false, err
			}
			var no []node
			hasNo, err := src.match("|")
			if err != nil {
				return node{}, false, err
			}
			if hasNo {
				if no, err = parseSeq(src, st, verbose, nested+1, false); err != nil {
					return node{}, false, err
				}
				if src.next == "|" {
					return node{}, false, src.error("conditional backref with more than two branches", 0)
				}
			}
			closed, err := src.match(")")
			if err != nil {
				return node{}, false, err
			}
			if !closed {
				return node{}, false, src.error("missing ), unterminated subpattern", src.tell()-start)
			}
			return node{op: opGroupexists, group: condgroup, body: yes, no: no, hasNo: hasNo}, false, nil
		case char == "-" || (rlen(char) == 1 && flagChars[one(char)] != 0):
			add, del, global, err := parseFlags(src, st, char)
			if err != nil {
				return node{}, false, err
			}
			if global {
				if st.flags&FlagVerbose != 0 && !verbose {
					return node{}, false, errVerbose{}
				}
				return node{}, true, nil
			}
			addFlags, delFlags = add, del
			group = false
		default:
			return node{}, false, src.error("unknown extension ?"+char, rlen(char)+1)
		}
	}
	gid := 0
	if group {
		gid = st.groups()
		st.groupwidths = append(st.groupwidths, nil)
		if st.groups() > maxGroups {
			return node{}, false, src.error("too many groups", rlen(name)+1)
		}
		if hasName {
			if old, ok := st.groupdict[name]; ok {
				return node{}, false, src.error(fmt.Sprintf("redefinition of group name %s as group %d; was group %d",
					pyjson.Repr(name), gid, old), rlen(name)+1)
			}
			st.groupdict[name] = gid
		}
	}
	subVerbose := (verbose || addFlags&FlagVerbose != 0) && delFlags&FlagVerbose == 0
	p, err := parseSub(src, st, subVerbose, nested+1)
	if err != nil {
		return node{}, false, err
	}
	closed, err := src.match(")")
	if err != nil {
		return node{}, false, err
	}
	if !closed {
		return node{}, false, src.error("missing ), unterminated subpattern", src.tell()-start)
	}
	if group {
		w := getwidth(st, p)
		st.groupwidths[gid] = &w
	}
	return node{op: opSubpattern, group: gid, add: addFlags, del: delFlags, body: p}, false, nil
}

// pyIntText reports whether int(s) accepts s (digits with optional
// surrounding whitespace, optional sign, underscores between digits).
func pyIntText(s string) bool {
	t := strings.TrimSpace(s)
	t = strings.TrimPrefix(strings.TrimPrefix(t, "+"), "-")
	if t == "" || strings.HasPrefix(t, "_") || strings.HasSuffix(t, "_") || strings.Contains(t, "__") {
		return false
	}
	for _, r := range t {
		if r != '_' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func parseFlags(src *tokenizer, st *state, char string) (int, int, bool, error) {
	add, del := 0, 0
	var err error
	if char != "-" {
		for {
			c := one(char)
			flag := flagChars[c]
			if c == 'L' {
				return 0, 0, false, src.error("bad inline flags: cannot use 'L' flag with a str pattern", 0)
			}
			add |= flag
			if flag&typeFlags != 0 && add&typeFlags != flag {
				return 0, 0, false, src.error("bad inline flags: flags 'a', 'u' and 'L' are incompatible", 0)
			}
			if char, err = src.get(); err != nil {
				return 0, 0, false, err
			}
			if char == "" {
				return 0, 0, false, src.error("missing -, : or )", 0)
			}
			if char == ")" || char == "-" || char == ":" {
				break
			}
			if rlen(char) != 1 || flagChars[one(char)] == 0 {
				msg := "missing -, : or )"
				if isAlpha(char) {
					msg = "unknown flag"
				}
				return 0, 0, false, src.error(msg, rlen(char))
			}
		}
	}
	if char == ")" {
		st.flags |= add
		return 0, 0, true, nil
	}
	if add&globalFlags != 0 {
		return 0, 0, false, src.error("bad inline flags: cannot turn on global flag", 1)
	}
	if char == "-" {
		if char, err = src.get(); err != nil {
			return 0, 0, false, err
		}
		if char == "" {
			return 0, 0, false, src.error("missing flag", 0)
		}
		if rlen(char) != 1 || flagChars[one(char)] == 0 {
			msg := "missing flag"
			if isAlpha(char) {
				msg = "unknown flag"
			}
			return 0, 0, false, src.error(msg, rlen(char))
		}
		for {
			flag := flagChars[one(char)]
			if flag&typeFlags != 0 {
				return 0, 0, false, src.error("bad inline flags: cannot turn off flags 'a', 'u' and 'L'", 0)
			}
			del |= flag
			if char, err = src.get(); err != nil {
				return 0, 0, false, err
			}
			if char == "" {
				return 0, 0, false, src.error("missing :", 0)
			}
			if char == ":" {
				break
			}
			if rlen(char) != 1 || flagChars[one(char)] == 0 {
				msg := "missing :"
				if isAlpha(char) {
					msg = "unknown flag"
				}
				return 0, 0, false, src.error(msg, rlen(char))
			}
		}
	}
	if del&globalFlags != 0 {
		return 0, 0, false, src.error("bad inline flags: cannot turn off global flag", 1)
	}
	if add&del != 0 {
		return 0, 0, false, src.error("bad inline flags: flag turned on and off", 1)
	}
	return add, del, false, nil
}

// isAlpha is str.isalpha() (an escape token like `\x` is not alphabetic).
func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// parse is sre_parse.parse(str, flags).
func parse(pattern string, flags int) ([]node, *state, error) {
	rs := []rune(pattern)
	run := func(flags int, verbose bool) ([]node, *state, *tokenizer, error) {
		st := &state{flags: flags, groupdict: map[string]int{}, groupwidths: []*width{nil}, lookbehindgroup: -1}
		src, err := newTokenizer(rs)
		if err != nil {
			return nil, nil, nil, err
		}
		p, err := parseSub(src, st, verbose, 0)
		return p, st, src, err
	}
	p, st, src, err := run(flags, flags&FlagVerbose != 0)
	if _, ok := err.(errVerbose); ok {
		p, st, src, err = run(flags|FlagVerbose, true)
	}
	if err != nil {
		return nil, nil, err
	}
	// fix_flags for a str pattern
	if st.flags&FlagASCII == 0 {
		st.flags |= FlagUnicode
	} else if st.flags&FlagUnicode != 0 {
		return nil, nil, &ValueError{"ASCII and UNICODE flags are incompatible"}
	}
	if src.next != "" {
		return nil, nil, src.error("unbalanced parenthesis", 0)
	}
	return p, st, nil
}

// ValueError is a Python ValueError raised while compiling.
type ValueError struct{ Msg string }

func (e *ValueError) Error() string { return e.Msg }
