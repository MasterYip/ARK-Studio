// Package pyargparse is a port of the parse loop of Python 3.10's argparse,
// enough for the `python -m arkstudio` command tree: long options with
// unique-prefix abbreviations and `--opt=value`, the -h/--help action,
// store / store_true / append actions, int and float values, choices,
// required options, positionals with nargs None, '?' and '*', and nested
// subparsers.  Error texts, the usage block printed before them and exit
// code 2 match argparse.
//
// The parser tree itself (option strings, dests, nargs, choices, usage and
// help texts) is the snapshot cli_spec.json, generated from the Python
// parser by tests/python/contract/gen_cli.py.  Defaults are runtime values
// (environment, working directory) and are set by the caller with Default.
package pyargparse

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pytext"
)

//go:embed cli_spec.json
var specJSON []byte

// Action mirrors one argparse.Action of the snapshot.
type Action struct {
	Kind          string    `json:"kind"` // help, store, store_true, append, parsers
	Dest          string    `json:"dest"`
	OptionStrings []string  `json:"option_strings"`
	Nargs         any       `json:"nargs"` // nil, float64(0), "?", "*", "A..."
	Required      bool      `json:"required"`
	Type          *string   `json:"type"`
	Metavar       *string   `json:"metavar"`
	Choices       []string  `json:"choices"`
	Parsers       []*Parser `json:"parsers"`
}

// Parser mirrors one argparse.ArgumentParser of the snapshot.
type Parser struct {
	Prog    string    `json:"prog"`
	Usage   string    `json:"usage"`
	Help    string    `json:"help"`
	Actions []*Action `json:"actions"`

	defaults map[string]any
	optMap   map[string]*Action
	optOrder []string
}

// Spec returns a fresh copy of the arkstudio parser tree.
func Spec() *Parser {
	var p Parser
	if err := json.Unmarshal(specJSON, &p); err != nil {
		panic("pyargparse: bad cli_spec.json: " + err.Error())
	}
	p.init()
	return &p
}

func (p *Parser) init() {
	p.defaults = map[string]any{}
	p.optMap = map[string]*Action{}
	for _, a := range p.Actions {
		for _, s := range a.OptionStrings {
			p.optMap[s] = a
			p.optOrder = append(p.optOrder, s)
		}
		for _, sub := range a.Parsers {
			sub.init()
		}
	}
}

// Sub returns the subparser reached by names ("skills", "setup"), or nil.
func (p *Parser) Sub(names ...string) *Parser {
	cur := p
	for _, name := range names {
		var next *Parser
		for _, a := range cur.Actions {
			for i, c := range a.Choices {
				if a.Kind == "parsers" && c == name {
					next = a.Parsers[i]
				}
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// AddExact adds a Go-only option that matches only when spelled in full (or
// as --name=value).  It is left out of prefix matching, so every
// abbreviation keeps the meaning it has in Python, and out of the help text.
// kind is "store" or "store_true"; typ is "", "int" or "duration".
func (p *Parser) AddExact(option, dest, kind, typ string, def any) *Parser {
	a := &Action{Kind: kind, Dest: dest, OptionStrings: []string{option}}
	if kind == "store_true" {
		a.Nargs = float64(0)
	}
	if typ != "" {
		a.Type = &typ
	}
	p.Actions = append(p.Actions, a)
	p.optMap[option] = a
	p.defaults[dest] = def
	return p
}

// Default sets the default of dest (argparse add_argument(default=...) or
// set_defaults).  Values: string, int64, float64, bool, []string, nil.
func (p *Parser) Default(dest string, value any) *Parser {
	p.defaults[dest] = value
	return p
}

// Namespace is the parse result.
type Namespace map[string]any

func (n Namespace) Str(k string) string {
	s, _ := n[k].(string)
	return s
}
func (n Namespace) Int(k string) int64 {
	i, _ := n[k].(int64)
	return i
}
func (n Namespace) Float(k string) float64 {
	f, _ := n[k].(float64)
	return f
}
func (n Namespace) Bool(k string) bool {
	b, _ := n[k].(bool)
	return b
}
func (n Namespace) List(k string) []string {
	l, _ := n[k].([]string)
	return l
}

// Exit is returned when argparse would call sys.exit: --help (code 0) or a
// usage error (code 2).  The text has already been written.
type Exit struct{ Code int }

func (e *Exit) Error() string { return fmt.Sprintf("exit %d", e.Code) }

type argError struct {
	parser  *Parser
	message string
}

func (e *argError) Error() string { return e.parser.Prog + ": error: " + e.message }

// Parse is parser.parse_args(argv) writing help to stdout and errors to stderr.
func (p *Parser) Parse(argv []string, stdout, stderr io.Writer) (Namespace, error) {
	ns := Namespace{}
	extras, err := p.parseKnown(argv, ns, stdout)
	if err != nil {
		return nil, p.report(err, stderr)
	}
	if unrec, _ := ns[unrecognized].([]string); len(unrec) > 0 || len(extras) > 0 {
		extras = append(extras, unrec...)
	}
	delete(ns, unrecognized)
	if len(extras) > 0 {
		return nil, p.report(&argError{p, "unrecognized arguments: " + strings.Join(extras, " ")}, stderr)
	}
	return ns, nil
}

// PrintHelp is parser.print_help().
func (p *Parser) PrintHelp(w io.Writer) { io.WriteString(w, p.Help) } //nolint:errcheck

func (p *Parser) report(err error, stderr io.Writer) error {
	if e, ok := err.(*Exit); ok {
		return e
	}
	ae := err.(*argError)
	io.WriteString(stderr, ae.parser.Usage)                            //nolint:errcheck
	fmt.Fprintf(stderr, "%s: error: %s\n", ae.parser.Prog, ae.message) //nolint:errcheck
	return &Exit{Code: 2}
}

const unrecognized = "_UNRECOGNIZED_ARGS_ATTR"

type optTuple struct {
	action   *Action
	option   string
	explicit *string
}

func (p *Parser) fail(format string, a ...any) error {
	return &argError{p, fmt.Sprintf(format, a...)}
}

func (p *Parser) actionFail(a *Action, msg string) error {
	if name := actionName(a); name != "" {
		msg = "argument " + name + ": " + msg
	}
	return &argError{p, msg}
}

func actionName(a *Action) string {
	if len(a.OptionStrings) > 0 {
		return strings.Join(a.OptionStrings, "/")
	}
	if a.Metavar != nil {
		return *a.Metavar
	}
	return a.Dest
}

var negativeNumber = regexp.MustCompile(`^-\d+$|^-\d*\.\d+$`)

// parseOptional is ArgumentParser._parse_optional.
func (p *Parser) parseOptional(arg string) (*optTuple, bool, error) {
	if arg == "" || arg[0] != '-' {
		return nil, false, nil
	}
	if a, ok := p.optMap[arg]; ok {
		return &optTuple{a, arg, nil}, true, nil
	}
	if len(arg) == 1 {
		return nil, false, nil
	}
	if i := strings.IndexByte(arg, '='); i >= 0 {
		opt, explicit := arg[:i], arg[i+1:]
		if a, ok := p.optMap[opt]; ok {
			return &optTuple{a, opt, &explicit}, true, nil
		}
	}
	tuples := p.optionTuples(arg)
	if len(tuples) > 1 {
		names := make([]string, len(tuples))
		for i, t := range tuples {
			names[i] = t.option
		}
		return nil, false, p.fail("ambiguous option: %s could match %s", arg, strings.Join(names, ", "))
	}
	if len(tuples) == 1 {
		return tuples[0], true, nil
	}
	if negativeNumber.MatchString(arg) {
		return nil, false, nil // no option looks like a negative number
	}
	if strings.Contains(arg, " ") {
		return nil, false, nil
	}
	return &optTuple{nil, arg, nil}, true, nil
}

// optionTuples is ArgumentParser._get_option_tuples (prefix chars "-").
func (p *Parser) optionTuples(arg string) []*optTuple {
	var out []*optTuple
	if strings.HasPrefix(arg, "--") {
		prefix := arg
		var explicit *string
		if i := strings.IndexByte(arg, '='); i >= 0 {
			prefix = arg[:i]
			e := arg[i+1:]
			explicit = &e
		}
		for _, s := range p.optOrder {
			if strings.HasPrefix(s, prefix) {
				out = append(out, &optTuple{p.optMap[s], s, explicit})
			}
		}
		return out
	}
	short, shortExplicit := arg[:2], arg[2:]
	for _, s := range p.optOrder {
		if s == short {
			e := shortExplicit
			out = append(out, &optTuple{p.optMap[s], s, &e})
		} else if strings.HasPrefix(s, arg) {
			out = append(out, &optTuple{p.optMap[s], s, nil})
		}
	}
	return out
}

func nargsPattern(a *Action) string {
	var pat string
	switch n := a.Nargs.(type) {
	case nil:
		pat = "(-*A-*)"
	case float64:
		pat = "(-*" + strings.Repeat("A", int(n)) + "-*)"
		if n == 0 {
			pat = "(-*-*)"
		}
	case string:
		switch n {
		case "?":
			pat = "(-*A?-*)"
		case "*":
			pat = "(-*[A-]*)"
		case "+":
			pat = "(-*A[A-]*)"
		case "A...":
			pat = "(-*A[-AO]*)"
		default:
			panic("pyargparse: nargs " + n)
		}
	}
	if len(a.OptionStrings) > 0 {
		pat = strings.ReplaceAll(strings.ReplaceAll(pat, "-*", ""), "-", "")
	}
	return pat
}

var reCache = map[string]*regexp.Regexp{}

func match(pattern, s string) []string {
	re, ok := reCache[pattern]
	if !ok {
		re = regexp.MustCompile("^(?:" + pattern + ")")
		reCache[pattern] = re
	}
	return re.FindStringSubmatch(s)
}

func (p *Parser) matchArgument(a *Action, pattern string) (int, error) {
	m := match(nargsPattern(a), pattern)
	if m == nil {
		switch n := a.Nargs.(type) {
		case nil:
			return 0, p.actionFail(a, "expected one argument")
		case string:
			if n == "?" {
				return 0, p.actionFail(a, "expected at most one argument")
			}
			if n == "+" {
				return 0, p.actionFail(a, "expected at least one argument")
			}
		case float64:
			if n == 1 {
				return 0, p.actionFail(a, "expected 1 argument")
			}
			return 0, p.actionFail(a, fmt.Sprintf("expected %d arguments", int(n)))
		}
		return 0, p.actionFail(a, "expected one argument")
	}
	return len(m[1]), nil
}

func (p *Parser) matchPartial(actions []*Action, pattern string) []int {
	for i := len(actions); i > 0; i-- {
		var b strings.Builder
		for _, a := range actions[:i] {
			b.WriteString(nargsPattern(a))
		}
		if m := match(b.String(), pattern); m != nil {
			out := make([]int, 0, i)
			for _, g := range m[1:] {
				out = append(out, len(g))
			}
			return out
		}
	}
	return nil
}

// parseKnown is parse_known_args + _parse_known_args.
func (p *Parser) parseKnown(args []string, ns Namespace, stdout io.Writer) ([]string, error) {
	for _, a := range p.Actions {
		if a.Kind == "help" {
			continue
		}
		if _, ok := ns[a.Dest]; !ok {
			ns[a.Dest] = cloneDefault(p.defaults[a.Dest])
		}
	}
	for k, v := range p.defaults {
		if _, ok := ns[k]; !ok {
			ns[k] = cloneDefault(v)
		}
	}
	optIdx := map[int]*optTuple{}
	var pattern strings.Builder
	for i, arg := range args {
		if arg == "--" {
			pattern.WriteByte('-')
			for range args[i+1:] {
				pattern.WriteByte('A')
			}
			break
		}
		t, ok, err := p.parseOptional(arg)
		if err != nil {
			return nil, err
		}
		if ok {
			optIdx[i] = t
			pattern.WriteByte('O')
		} else {
			pattern.WriteByte('A')
		}
	}
	pat := pattern.String()
	seen := map[*Action]bool{}
	var extras []string

	takeAction := func(a *Action, strs []string, option string) error {
		seen[a] = true
		v, err := p.getValues(a, strs)
		if err != nil {
			return err
		}
		switch a.Kind {
		case "help":
			p.PrintHelp(stdout)
			return &Exit{Code: 0}
		case "store":
			ns[a.Dest] = v
		case "store_true":
			ns[a.Dest] = true
		case "store_false":
			ns[a.Dest] = false
		case "append":
			cur, _ := ns[a.Dest].([]string)
			ns[a.Dest] = append(append([]string(nil), cur...), v.(string))
		case "parsers":
			vals := v.([]string)
			name := vals[0]
			ns[a.Dest] = name
			var sub *Parser
			for i, c := range a.Choices {
				if c == name {
					sub = a.Parsers[i]
				}
			}
			subNS := Namespace{}
			rest, err := sub.parseKnown(vals[1:], subNS, stdout)
			if err != nil {
				return err
			}
			for k, val := range subNS {
				if k == unrecognized {
					continue
				}
				ns[k] = val
			}
			if inner, _ := subNS[unrecognized].([]string); len(inner) > 0 {
				rest = append(rest, inner...)
			}
			if len(rest) > 0 {
				cur, _ := ns[unrecognized].([]string)
				ns[unrecognized] = append(cur, rest...)
			}
		}
		return nil
	}

	var positionals []*Action
	for _, a := range p.Actions {
		if len(a.OptionStrings) == 0 {
			positionals = append(positionals, a)
		}
	}

	consumeOptional := func(start int) (int, error) {
		t := optIdx[start]
		a, option, explicit := t.action, t.option, t.explicit
		type pending struct {
			a      *Action
			args   []string
			option string
		}
		var todo []pending
		var stop int
		for {
			if a == nil {
				extras = append(extras, args[start])
				return start + 1, nil
			}
			if explicit != nil {
				count, err := p.matchArgument(a, "A")
				if err != nil {
					return 0, err
				}
				if count == 0 && option[1] != '-' && *explicit != "" {
					todo = append(todo, pending{a, nil, option})
					option = string(option[0]) + (*explicit)[:1]
					rest := (*explicit)[1:]
					if na, ok := p.optMap[option]; ok {
						a = na
						if rest == "" {
							explicit = nil
						} else {
							explicit = &rest
						}
						continue
					}
					return 0, p.actionFail(a, "ignored explicit argument "+Repr(*explicit))
				} else if count == 1 {
					stop = start + 1
					todo = append(todo, pending{a, []string{*explicit}, option})
					break
				}
				return 0, p.actionFail(a, "ignored explicit argument "+Repr(*explicit))
			}
			s := start + 1
			count, err := p.matchArgument(a, pat[s:])
			if err != nil {
				return 0, err
			}
			stop = s + count
			todo = append(todo, pending{a, args[s:stop], option})
			break
		}
		for _, item := range todo {
			if err := takeAction(item.a, item.args, item.option); err != nil {
				return 0, err
			}
		}
		return stop, nil
	}

	consumePositionals := func(start int) (int, error) {
		counts := p.matchPartial(positionals, pat[start:])
		for i, n := range counts {
			if err := takeAction(positionals[i], args[start:start+n], ""); err != nil {
				return 0, err
			}
			start += n
		}
		positionals = positionals[len(counts):]
		return start, nil
	}

	maxOpt := -1
	for i := range optIdx {
		maxOpt = max(maxOpt, i)
	}
	start := 0
	for start <= maxOpt {
		next := -1
		for i := start; i <= maxOpt; i++ {
			if _, ok := optIdx[i]; ok {
				next = i
				break
			}
		}
		if start != next {
			end, err := consumePositionals(start)
			if err != nil {
				return nil, err
			}
			if end > start {
				start = end
				continue
			}
			start = end
		}
		if _, ok := optIdx[start]; !ok {
			extras = append(extras, args[start:next]...)
			start = next
		}
		var err error
		if start, err = consumeOptional(start); err != nil {
			return nil, err
		}
	}
	stop, err := consumePositionals(start)
	if err != nil {
		return nil, err
	}
	extras = append(extras, args[stop:]...)

	var required []string
	for _, a := range p.Actions {
		if !seen[a] && a.Required {
			required = append(required, actionName(a))
		}
		// String defaults run through type conversion (argparse does this
		// for unseen actions whose value is still the default).
		if !seen[a] && a.Type != nil {
			if s, ok := ns[a.Dest].(string); ok {
				v, err := p.getValue(a, s)
				if err != nil {
					return nil, err
				}
				ns[a.Dest] = v
			}
		}
	}
	if len(required) > 0 {
		return nil, p.fail("the following arguments are required: %s", strings.Join(required, ", "))
	}
	return extras, nil
}

func cloneDefault(v any) any {
	if l, ok := v.([]string); ok {
		return append([]string(nil), l...)
	}
	return v
}

// getValues is ArgumentParser._get_values for the nargs in use.
func (p *Parser) getValues(a *Action, strs []string) (any, error) {
	if s, _ := a.Nargs.(string); s != "A..." {
		for i, v := range strs {
			if v == "--" {
				strs = append(append([]string(nil), strs[:i]...), strs[i+1:]...)
				break
			}
		}
	}
	nargs, _ := a.Nargs.(string)
	switch {
	case len(strs) == 0 && nargs == "?":
		v := p.defaults[a.Dest]
		if s, ok := v.(string); ok {
			cv, err := p.getValue(a, s)
			if err != nil {
				return nil, err
			}
			return cv, p.checkValue(a, cv)
		}
		return v, nil
	case len(strs) == 0 && nargs == "*" && len(a.OptionStrings) == 0:
		if v, ok := p.defaults[a.Dest]; ok && v != nil {
			return v, p.checkValue(a, v)
		}
		return []string{}, nil
	case len(strs) == 1 && (a.Nargs == nil || nargs == "?"):
		v, err := p.getValue(a, strs[0])
		if err != nil {
			return nil, err
		}
		return v, p.checkValue(a, v)
	case nargs == "A...":
		if err := p.checkValue(a, strs[0]); err != nil {
			return nil, err
		}
		return strs, nil
	}
	out := make([]string, 0, len(strs))
	for _, s := range strs {
		if err := p.checkValue(a, s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (p *Parser) getValue(a *Action, s string) (any, error) {
	if a.Type == nil {
		return s, nil
	}
	switch *a.Type {
	case "int":
		n, err := pytext.Int(s)
		if err != nil {
			return nil, p.actionFail(a, "invalid int value: "+Repr(s))
		}
		return n, nil
	case "float":
		f, ok := PyFloat(s)
		if !ok {
			return nil, p.actionFail(a, "invalid float value: "+Repr(s))
		}
		return f, nil
	case "duration":
		d, err := time.ParseDuration(s)
		if err != nil {
			return nil, p.actionFail(a, "invalid duration value: "+Repr(s))
		}
		return d, nil
	}
	panic("pyargparse: type " + *a.Type)
}

func (p *Parser) checkValue(a *Action, v any) error {
	if a.Choices == nil {
		return nil
	}
	s, ok := v.(string)
	if ok {
		for _, c := range a.Choices {
			if c == s {
				return nil
			}
		}
	}
	quoted := make([]string, len(a.Choices))
	for i, c := range a.Choices {
		quoted[i] = Repr(c)
	}
	return p.actionFail(a, fmt.Sprintf("invalid choice: %s (choose from %s)", reprAny(v), strings.Join(quoted, ", ")))
}

func reprAny(v any) string {
	switch t := v.(type) {
	case string:
		return Repr(t)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return fmt.Sprint(v)
}

// PyFloat is Python's float(str) for the forms float() accepts.
func PyFloat(s string) (float64, bool) {
	t := pytext.Strip(s)
	low := strings.ToLower(t)
	sign := 1.0
	body := low
	if strings.HasPrefix(body, "+") || strings.HasPrefix(body, "-") {
		if body[0] == '-' {
			sign = -1
		}
		body = body[1:]
	}
	switch body {
	case "inf", "infinity":
		return sign * math.Inf(1), true
	case "nan":
		return math.NaN(), true
	}
	if t == "" || strings.ContainsAny(low, "xp") || strings.HasPrefix(body, "_") || strings.HasSuffix(body, "_") ||
		strings.Contains(body, "__") || strings.Contains(body, "_.") || strings.Contains(body, "._") ||
		strings.Contains(body, "_e") || strings.Contains(body, "e_") {
		return 0, false
	}
	// Python accepts Unicode decimal digits; map them to ASCII first.
	var b strings.Builder
	for _, r := range t {
		if r > 127 {
			if d, err := pytext.Int(string(r)); err == nil && pytext.IsDecimal(string(r)) {
				b.WriteByte(byte('0' + d))
				continue
			}
			return 0, false
		}
		b.WriteRune(r)
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(b.String(), "_", ""), 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true // +-Inf on overflow, 0 on underflow, as float()
		}
		return 0, false
	}
	return f, true
}
