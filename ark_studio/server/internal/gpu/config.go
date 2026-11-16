// Package gpu ports ARK's vendored gpu_server_probe package byte for byte.
//
// config.go ports vendor/gpu_server_probe/config.py: the safe YAML-subset
// parser for servers_rp.yaml (parse_yaml), the strict schema (validate),
// load_config and public_server.  Values use the pyjson model: dict ->
// *pyjson.Object, list -> []any, int -> int64 (*big.Int when it does not fit),
// float -> float64, str -> string, bool, None -> nil.
package gpu

import (
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pyre"
	"arkstudio/go/internal/compat/pytext"
)

// MaxConfigBytes and MaxServers are MAX_CONFIG_BYTES and MAX_SERVERS.
const (
	MaxConfigBytes = 256 * 1024
	MaxServers     = 64
)

// ConfigError is config.ConfigError; NotFound marks the ConfigNotFound
// subclass, whose message is the path.
type ConfigError struct {
	Msg      string
	NotFound bool
}

func (e *ConfigError) Error() string { return e.Msg }

func cfgErr(format string, a ...any) error { return &ConfigError{Msg: fmt.Sprintf(format, a...)} }

// RecursionError stands in for Python's RecursionError, which the parser
// raises (instead of a ConfigError) on absurdly deep nesting.
type RecursionError struct{}

func (*RecursionError) Error() string { return "maximum recursion depth exceeded" }

// Pattern is a compiled Python regular expression used with re.match.
type Pattern struct{ re *pyre.Regexp }

func mustPattern(p string) *Pattern { return &Pattern{pyre.MustCompile(p, 0)} }

// MatchString is bool(pattern.match(s)).
func (p *Pattern) MatchString(s string) bool {
	_, ok := p.match(s)
	return ok
}

func (p *Pattern) match(s string) (pyre.Match, bool) {
	rs := []rune(s)
	budget := 1 << 30
	m, ok, err := p.re.SearchFrom(rs, 0, -1, &budget)
	if err != nil || !ok || m.Start != 0 {
		return pyre.Match{}, false
	}
	return m, true
}

var (
	topFields     = []string{"enabled", "defaults", "servers", "queue"}
	defaultFields = []string{"port", "user", "timeout_seconds"}
	queueFields   = []string{"enabled", "state_dir", "poll_seconds", "default_timeout_minutes",
		"max_jobs_per_gpu", "max_utilization_pct", "min_free_memory_mb",
		"allow_colocation", "setup", "path_map", "env"}
	pathMapFields = []string{"from", "to", "servers"}
	serverFields  = []string{"name", "host", "port", "user", "tags", "note", "enabled", "timeout_seconds"}

	nameRE      = mustPattern(`^[A-Za-z0-9._-]{1,60}$`)
	hostRE      = mustPattern(`^(?=.{1,253}$)[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	hostV6RE    = mustPattern(`^[0-9A-Fa-f:]{2,45}$`)
	userRE      = mustPattern(`^[A-Za-z0-9._-]{1,32}$`)
	tagRE       = mustPattern(`^[A-Za-z0-9._:-]{1,40}$`)
	keyRE       = mustPattern(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	inlineMapRE = mustPattern(`^([A-Za-z_][A-Za-z0-9_-]*):(?:\s+(.*))?$`)
	intTextRE   = mustPattern(`^[+-]?\d+$`)
	floatTextRE = mustPattern(`^[+-]?\d+\.\d+$`)
	envKeyRE    = mustPattern(`^(?:[A-Za-z_][A-Za-z0-9_]{0,63})\Z`) // re.fullmatch

	secretNames = map[string]bool{
		"password": true, "passphrase": true, "passwd": true, "pwd": true, "secret": true, "token": true,
		"key": true, "keyfile": true, "keydata": true, "privatekey": true, "sshkey": true,
		"apikey": true, "identity": true, "identityfile": true, "credential": true, "credentials": true,
	}
	secretSuffixes = []string{"password", "passphrase", "passwd", "secret", "token", "apikey", "privatekey"}
)

func cfgContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func cfgIsAlnumASCII(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func cfgSecretField(name string) bool {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		if cfgIsAlnumASCII(name[i]) {
			b.WriteByte(name[i])
		}
	}
	normalized := strings.ToLower(b.String())
	if secretNames[normalized] {
		return true
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(normalized, s) {
			return true
		}
	}
	return false
}

func cfgHasControl(s string) bool {
	for _, r := range s {
		if r < 32 {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- parsing

type yline struct {
	indent int
	text   string
	number int
}

func cfgTokenize(source string) ([]yline, error) {
	var lines []yline
	for i, raw := range pytext.SplitLines(source, false) {
		number := i + 1
		if strings.Contains(raw, "\t") {
			return nil, cfgErr("line %d: tabs are not supported; indent with spaces", number)
		}
		stripped := pytext.Strip(raw)
		if stripped == "" || strings.HasPrefix(stripped, "#") {
			continue
		}
		if stripped == "---" || stripped == "..." {
			return nil, cfgErr("line %d: document markers are not supported", number)
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		if indent == 0 && strings.HasPrefix(stripped, "- ") {
			return nil, cfgErr("line %d: the top level must be a mapping", number)
		}
		lines = append(lines, yline{indent, stripped, number})
	}
	if len(lines) == 0 {
		return nil, cfgErr("the server list is empty")
	}
	return lines, nil
}

func cfgUnquote(text string, number int) (string, error) {
	rs := []rune(text)
	quote := rs[0]
	body := rs[1:]
	var out []rune
	for i := 0; i < len(body); {
		c := body[i]
		if c == quote {
			if quote == '\'' && i+1 < len(body) && body[i+1] == '\'' {
				out = append(out, '\'')
				i += 2
				continue
			}
			if pytext.Strip(string(body[i+1:])) != "" {
				return "", cfgErr("line %d: unexpected text after a quoted value", number)
			}
			return string(out), nil
		}
		if quote == '"' && c == '\\' && i+1 < len(body) {
			e := body[i+1]
			switch e {
			case 'n':
				e = '\n'
			case 't':
				e = '\t'
			}
			out = append(out, e)
			i += 2
			continue
		}
		out = append(out, c)
		i++
	}
	return "", cfgErr("line %d: unterminated quoted value", number)
}

func cfgSplitInline(text string, number int) ([]string, error) {
	var parts []string
	var current strings.Builder
	var quote rune
	for _, c := range text {
		if quote != 0 {
			current.WriteRune(c)
			if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			current.WriteRune(c)
		case ',':
			parts = append(parts, pytext.Strip(current.String()))
			current.Reset()
		default:
			current.WriteRune(c)
		}
	}
	if quote != 0 {
		return nil, cfgErr("line %d: unterminated quoted inline list", number)
	}
	parts = append(parts, pytext.Strip(current.String()))
	for _, p := range parts {
		if p == "" {
			return nil, cfgErr("line %d: empty item in an inline list", number)
		}
	}
	return parts, nil
}

// decimalDigits maps Unicode decimal digits (re's \d) to ASCII.
func cfgDecimalDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x80 {
			b.WriteRune(r)
			continue
		}
		b.WriteByte(byte('0' + cfgDigitValue(r)))
	}
	return b.String()
}

func cfgDigitValue(r rune) int {
	for _, rng := range unicode.Nd.R16 {
		if r <= 0xffff && uint16(r) >= rng.Lo && uint16(r) <= rng.Hi {
			return int(uint16(r)-rng.Lo) % 10
		}
	}
	for _, rng := range unicode.Nd.R32 {
		if uint32(r) >= rng.Lo && uint32(r) <= rng.Hi {
			return int(uint32(r)-rng.Lo) % 10
		}
	}
	return 0
}

// intValue normalizes an integer to int64 when it fits, else *big.Int.
func cfgIntValue(n *big.Int) any {
	if n.IsInt64() {
		return n.Int64()
	}
	return n
}

// parser tracks an approximation of the Python call depth so absurd nesting
// fails like CPython's recursion limit instead of succeeding.
type parser struct {
	lines []yline
	depth int
}

// callerFrames is the assumed Python stack depth (len(inspect.stack())) at
// the parse_yaml call; the real value depends on the server's call chain.
// The first failing nesting level matches CPython 3.10 for a given depth
// (checked by TestRecursionLimitAgainstCPython); the +3 is CPython overhead.
// recursionLimit is sys.getrecursionlimit().
var callerFrames = 20

const recursionLimit = 1000

func (p *parser) enter(frames int) error {
	if callerFrames+3+p.depth+frames > recursionLimit {
		return &RecursionError{}
	}
	p.depth += frames
	return nil
}

func (p *parser) scalar(text string, number int) (any, error) {
	// _scalar frame plus re.match/_compile (2) at its deepest leaf call.
	if err := p.enter(1); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	if err := p.enter(2); err != nil {
		return nil, err
	}
	p.depth -= 2
	text = pytext.Strip(text)
	if text == "" {
		return nil, nil
	}
	if text[0] == '"' || text[0] == '\'' {
		return cfgUnquote(text, number)
	}
	lowered := pytext.Lower(text)
	switch lowered {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null", "~":
		return nil, nil
	}
	if strings.HasPrefix(text, "[") && strings.HasSuffix(text, "]") {
		inner := pytext.Strip(text[1 : len(text)-1])
		if inner == "" {
			return []any{}, nil
		}
		parts, err := cfgSplitInline(inner, number)
		if err != nil {
			return nil, err
		}
		if err := p.enter(1); err != nil { // the list comprehension frame
			return nil, err
		}
		defer func() { p.depth-- }()
		out := make([]any, 0, len(parts))
		for _, part := range parts {
			v, err := p.scalar(part, number)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	}
	if text[0] == '&' || text[0] == '*' || text[0] == '|' || text[0] == '>' || text[0] == '{' ||
		text[0] == '@' || text[0] == '`' {
		return nil, cfgErr("line %d: unsupported YAML construct %s", number, pyjson.Repr(pytext.Prefix(text, 24)))
	}
	if intTextRE.MatchString(text) {
		n, _ := new(big.Int).SetString(cfgDecimalDigits(text), 10)
		return cfgIntValue(n), nil
	}
	if floatTextRE.MatchString(text) {
		f, _ := strconv.ParseFloat(cfgDecimalDigits(text), 64)
		return f, nil
	}
	if cfgHasControl(text) {
		return nil, cfgErr("line %d: control characters are not allowed", number)
	}
	return text, nil
}

func (p *parser) block(index, indent int) (any, int, error) {
	if err := p.enter(1); err != nil {
		return nil, index, err
	}
	defer func() { p.depth-- }()
	if strings.HasPrefix(p.lines[index].text, "- ") {
		return p.list(index, indent)
	}
	return p.mapping(index, indent)
}

func cfgCredentialErr(number int, key string) error {
	return cfgErr("line %d: credential field %s is not supported; use your SSH agent or ~/.ssh/config instead",
		number, pyjson.Repr(key))
}

func (p *parser) mapping(index, indent int) (*pyjson.Object, int, error) {
	if err := p.enter(1); err != nil {
		return nil, index, err
	}
	defer func() { p.depth-- }()
	result := pyjson.NewObject()
	for index < len(p.lines) {
		line := p.lines[index]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, index, cfgErr("line %d: unexpected indentation", line.number)
		}
		if strings.HasPrefix(line.text, "- ") {
			break
		}
		key, rest, found := strings.Cut(line.text, ":")
		key = pytext.Strip(key)
		if !found || !keyRE.MatchString(key) {
			return nil, index, cfgErr("line %d: expected 'key: value'", line.number)
		}
		if err := p.enter(3); err != nil { // _secret_field -> re.sub -> _compile
			return nil, index, err
		}
		p.depth -= 3
		if cfgSecretField(key) {
			return nil, index, cfgCredentialErr(line.number, key)
		}
		if result.Has(key) {
			return nil, index, cfgErr("line %d: duplicate key %s", line.number, pyjson.Repr(key))
		}
		index++
		rest = pytext.Strip(rest)
		switch {
		case rest != "":
			v, err := p.scalar(rest, line.number)
			if err != nil {
				return nil, index, err
			}
			result.Set(key, v)
		case index < len(p.lines) && p.lines[index].indent > indent:
			v, next, err := p.block(index, p.lines[index].indent)
			if err != nil {
				return nil, next, err
			}
			result.Set(key, v)
			index = next
		default:
			result.Set(key, nil)
		}
	}
	return result, index, nil
}

func (p *parser) list(index, indent int) ([]any, int, error) {
	if err := p.enter(1); err != nil {
		return nil, index, err
	}
	defer func() { p.depth-- }()
	result := []any{}
	for index < len(p.lines) {
		line := p.lines[index]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, index, cfgErr("line %d: unexpected indentation", line.number)
		}
		if !strings.HasPrefix(line.text, "- ") {
			break
		}
		item := pytext.Strip(line.text[2:])
		index++
		if item == "" {
			if index < len(p.lines) && p.lines[index].indent > indent {
				child, next, err := p.block(index, p.lines[index].indent)
				if err != nil {
					return nil, next, err
				}
				result = append(result, child)
				index = next
			} else {
				result = append(result, nil)
			}
			continue
		}
		if m, ok := inlineMapRE.match(item); ok {
			rs := []rune(item)
			s, e, _ := m.Group(1)
			key := string(rs[s:e])
			entry := pyjson.NewObject()
			if err := p.enter(3); err != nil {
				return nil, index, err
			}
			p.depth -= 3
			if cfgSecretField(key) {
				return nil, index, cfgCredentialErr(line.number, key)
			}
			value := ""
			if s, e, ok := m.Group(2); ok {
				value = pytext.Strip(string(rs[s:e]))
			}
			if value != "" {
				v, err := p.scalar(value, line.number)
				if err != nil {
					return nil, index, err
				}
				entry.Set(key, v)
			} else {
				entry.Set(key, nil)
			}
			for index < len(p.lines) && p.lines[index].indent > indent {
				extra, next, err := p.mapping(index, p.lines[index].indent)
				if err != nil {
					return nil, next, err
				}
				if next == index {
					// CPython loops forever here (a "- " line nested under an
					// inline mapping item); report it instead of hanging.
					return nil, index, cfgErr("line %d: unexpected indentation", p.lines[index].number)
				}
				index = next
				for _, k := range extra.Keys() {
					if entry.Has(k) {
						return nil, index, cfgErr("line %d: duplicate key %s", line.number, pyjson.Repr(k))
					}
					entry.Set(k, extra.Value(k))
				}
			}
			result = append(result, entry)
			continue
		}
		v, err := p.scalar(item, line.number)
		if err != nil {
			return nil, index, err
		}
		result = append(result, v)
		if index < len(p.lines) && p.lines[index].indent > indent {
			return nil, index, cfgErr("line %d: unexpected indentation", p.lines[index].number)
		}
	}
	return result, index, nil
}

// ParseYAML is parse_yaml(source): the documented YAML subset.
func ParseYAML(source string) (*pyjson.Object, error) {
	lines, err := cfgTokenize(source)
	if err != nil {
		return nil, err
	}
	p := &parser{lines: lines, depth: 1}
	value, index, err := p.block(0, lines[0].indent)
	if err != nil {
		return nil, err
	}
	if index != len(lines) {
		return nil, cfgErr("line %d: unexpected content", lines[index].number)
	}
	obj, ok := value.(*pyjson.Object)
	if !ok {
		return nil, cfgErr("the top level must be a mapping")
	}
	return obj, nil
}

// ---------------------------------------------------------------- schema

func cfgIsInt(v any) bool {
	switch v.(type) {
	case int, int64, *big.Int:
		return true
	}
	return false
}

func cfgIsNumber(v any) bool {
	if _, ok := v.(float64); ok {
		return true
	}
	return cfgIsInt(v)
}

// get is dict.get(key, def).
func cfgGet(o *pyjson.Object, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

func cfgRequireMapping(v any, label string) (*pyjson.Object, error) {
	o, ok := v.(*pyjson.Object)
	if !ok || o == nil {
		return nil, cfgErr("%s must be a mapping", label)
	}
	return o, nil
}

func cfgCheckFields(v *pyjson.Object, allowed []string, label string) error {
	for _, k := range v.Keys() {
		if !cfgContains(allowed, k) {
			if label != "config" {
				return cfgErr("unknown field %s.%s", label, k)
			}
			return cfgErr("unknown field %s", k)
		}
	}
	return nil
}

func cfgBoolean(v any, label string) (bool, error) {
	if b, ok := v.(bool); ok {
		return b, nil
	}
	return false, cfgErr("%s must be true or false", label)
}

func cfgInteger(v any, label string, low, high int64) (int64, error) {
	if !cfgIsInt(v) {
		return 0, cfgErr("%s must be an integer", label)
	}
	var n int64
	inRange := false
	switch x := v.(type) {
	case int:
		n, inRange = int64(x), true
	case int64:
		n, inRange = x, true
	case *big.Int:
		if x.IsInt64() {
			n, inRange = x.Int64(), true
		}
	}
	if !inRange || n < low || n > high {
		return 0, cfgErr("%s must be between %d and %d", label, low, high)
	}
	return n, nil
}

func cfgText(v any, label string, limit int, allowEmpty bool) (string, error) {
	if v == nil && allowEmpty {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", cfgErr("%s must be a string", label)
	}
	s = pytext.Strip(s)
	if s == "" && !allowEmpty {
		return "", cfgErr("%s is required", label)
	}
	if pytext.Len(s) > limit {
		return "", cfgErr("%s must be at most %d characters", label, limit)
	}
	if cfgHasControl(s) {
		return "", cfgErr("%s must not contain control characters", label)
	}
	return s, nil
}

func cfgHost(v any, label string) (string, error) {
	h, err := cfgText(v, label, 253, false)
	if err != nil {
		return "", err
	}
	if !(hostRE.MatchString(h) || hostV6RE.MatchString(h)) {
		return "", cfgErr("%s must be a plain hostname or IP address (no spaces, user@, URLs or shell characters)", label)
	}
	return h, nil
}

func cfgUser(v any, label string) (string, error) {
	u, err := cfgText(v, label, 32, true)
	if err != nil {
		return "", err
	}
	if u != "" && !userRE.MatchString(u) {
		return "", cfgErr("%s must contain only letters, digits, dot, dash or underscore", label)
	}
	return u, nil
}

func cfgName(v any, label string) (string, error) {
	n, err := cfgText(v, label, 60, false)
	if err != nil {
		return "", err
	}
	if !nameRE.MatchString(n) {
		return "", cfgErr("%s must use letters, digits, dot, dash or underscore", label)
	}
	return n, nil
}

func cfgTags(v any, label string) ([]any, error) {
	if v == nil {
		return []any{}, nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, cfgErr("%s must be a list of short strings", label)
	}
	if len(list) > 12 {
		return nil, cfgErr("%s can have at most 12 tags", label)
	}
	out := []any{}
	seen := map[string]bool{}
	for _, item := range list {
		if cfgIsNumber(item) {
			item = pyjson.Str(item)
		}
		tag, err := cfgText(item, label, 40, false)
		if err != nil {
			return nil, err
		}
		if !tagRE.MatchString(tag) {
			return nil, cfgErr("%s tag %s may only use letters, digits, '.', ':', '-' or '_'", label, pyjson.Repr(tag))
		}
		if !seen[tag] {
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out, nil
}

func cfgAbsNoDotDot(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	return !cfgContains(strings.Split(p, "/"), "..")
}

func cfgQueue(value any) (*pyjson.Object, error) {
	if !pyjson.Truthy(value) {
		value = pyjson.NewObject()
	}
	raw, err := cfgRequireMapping(value, "queue")
	if err != nil {
		return nil, err
	}
	if err := cfgCheckFields(raw, queueFields, "queue"); err != nil {
		return nil, err
	}
	stateDir, err := cfgText(cfgGet(raw, "state_dir", "/tmp/ark-gpu-queue"), "queue.state_dir", 240, false)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(stateDir, "/") {
		return nil, cfgErr("queue.state_dir must be an absolute POSIX path")
	}
	if cfgContains(strings.Split(stateDir, "/"), "..") {
		return nil, cfgErr("queue.state_dir must not contain '..'")
	}
	setup, err := cfgText(cfgGet(raw, "setup", ""), "queue.setup", 2000, true)
	if err != nil {
		return nil, err
	}
	envRaw := raw.Value("env")
	if !pyjson.Truthy(envRaw) {
		envRaw = pyjson.NewObject()
	}
	envMap, err := cfgRequireMapping(envRaw, "queue.env")
	if err != nil {
		return nil, err
	}
	env := pyjson.NewObject()
	for _, key := range envMap.Keys() {
		value := envMap.Value(key)
		if !envKeyRE.MatchString(key) {
			return nil, cfgErr("queue.env keys must be environment variable names")
		}
		var s string
		switch x := value.(type) {
		case bool:
			s = "0"
			if x {
				s = "1"
			}
		case string:
			s = x
		default:
			if !cfgIsNumber(value) {
				return nil, cfgErr("queue.env.%s must be a string", key)
			}
			s = pyjson.Str(value)
		}
		if cfgHasControl(s) || pytext.Len(s) > 500 {
			return nil, cfgErr("queue.env.%s must be a short single-line value", key)
		}
		env.Set(key, s)
	}
	mapsRaw := raw.Value("path_map")
	if !pyjson.Truthy(mapsRaw) {
		mapsRaw = []any{}
	}
	maps, ok := mapsRaw.([]any)
	if !ok {
		return nil, cfgErr("queue.path_map must be a list")
	}
	pathMap := []any{}
	for i, itemRaw := range maps {
		index := i + 1
		label := fmt.Sprintf("queue.path_map[%d]", index)
		item, err := cfgRequireMapping(itemRaw, label)
		if err != nil {
			return nil, err
		}
		if err := cfgCheckFields(item, pathMapFields, label); err != nil {
			return nil, err
		}
		source, err := cfgText(item.Value("from"), label+".from", 240, false)
		if err != nil {
			return nil, err
		}
		target, err := cfgText(item.Value("to"), label+".to", 240, false)
		if err != nil {
			return nil, err
		}
		for _, pair := range [][2]string{{"from", source}, {"to", target}} {
			if !cfgAbsNoDotDot(pair[1]) {
				return nil, cfgErr("%s.%s must be an absolute path without '..'", label, pair[0])
			}
		}
		serversRaw := item.Value("servers")
		if !pyjson.Truthy(serversRaw) {
			serversRaw = []any{}
		}
		list, ok := serversRaw.([]any)
		if !ok {
			return nil, cfgErr("%s.servers must be a list", label)
		}
		servers := []any{}
		for _, n := range list {
			if s := pytext.Strip(pyjson.Str(n)); s != "" {
				servers = append(servers, s)
			}
		}
		pathMap = append(pathMap, pyjson.NewObject(
			"from", strings.TrimRight(source, "/"),
			"to", strings.TrimRight(target, "/"),
			"servers", servers))
	}
	enabled, err := cfgBoolean(cfgGet(raw, "enabled", false), "queue.enabled")
	if err != nil {
		return nil, err
	}
	ints := []struct {
		key       string
		def       int64
		low, high int64
	}{
		{"poll_seconds", 5, 1, 300},
		{"default_timeout_minutes", 240, 1, 1440},
		{"max_jobs_per_gpu", 0, 0, 16},
		{"max_utilization_pct", 10, 0, 100},
		{"min_free_memory_mb", 1024, 0, 1_000_000},
	}
	out := pyjson.NewObject("enabled", enabled, "state_dir", stateDir)
	for _, spec := range ints {
		n, err := cfgInteger(cfgGet(raw, spec.key, spec.def), "queue."+spec.key, spec.low, spec.high)
		if err != nil {
			return nil, err
		}
		out.Set(spec.key, n)
	}
	colocation, err := cfgBoolean(cfgGet(raw, "allow_colocation", true), "queue.allow_colocation")
	if err != nil {
		return nil, err
	}
	out.Set("allow_colocation", colocation)
	out.Set("setup", setup)
	out.Set("path_map", pathMap)
	out.Set("env", env)
	return out, nil
}

// Validate is validate(data): the normalized server list.
func Validate(data any) (*pyjson.Object, error) {
	cfg, err := cfgRequireMapping(data, "config")
	if err != nil {
		return nil, err
	}
	if err := cfgCheckFields(cfg, topFields, "config"); err != nil {
		return nil, err
	}
	enabled, err := cfgBoolean(cfgGet(cfg, "enabled", false), "enabled")
	if err != nil {
		return nil, err
	}
	defaultsRaw := cfg.Value("defaults")
	if !pyjson.Truthy(defaultsRaw) {
		defaultsRaw = pyjson.NewObject()
	}
	defaults, err := cfgRequireMapping(defaultsRaw, "defaults")
	if err != nil {
		return nil, err
	}
	if err := cfgCheckFields(defaults, defaultFields, "defaults"); err != nil {
		return nil, err
	}
	defaultPort, err := cfgInteger(cfgGet(defaults, "port", int64(22)), "defaults.port", 1, 65535)
	if err != nil {
		return nil, err
	}
	defaultUser, err := cfgUser(cfgGet(defaults, "user", ""), "defaults.user")
	if err != nil {
		return nil, err
	}
	defaultTimeout, err := cfgInteger(cfgGet(defaults, "timeout_seconds", int64(8)), "defaults.timeout_seconds", 1, 60)
	if err != nil {
		return nil, err
	}
	serversRaw := cfg.Value("servers")
	if serversRaw == nil {
		serversRaw = []any{}
	}
	list, ok := serversRaw.([]any)
	if !ok {
		return nil, cfgErr("servers must be a list")
	}
	if len(list) > MaxServers {
		return nil, cfgErr("at most %d servers can be configured", MaxServers)
	}
	servers := []any{}
	seen := map[string]bool{}
	for i, raw := range list {
		label := fmt.Sprintf("servers[%d]", i+1)
		item, err := cfgRequireMapping(raw, label)
		if err != nil {
			return nil, err
		}
		if err := cfgCheckFields(item, serverFields, label); err != nil {
			return nil, err
		}
		n, err := cfgName(item.Value("name"), label+".name")
		if err != nil {
			return nil, err
		}
		if seen[n] {
			return nil, cfgErr("duplicate server name %s", pyjson.Repr(n))
		}
		seen[n] = true
		h, err := cfgHost(item.Value("host"), label+".host")
		if err != nil {
			return nil, err
		}
		port, err := cfgInteger(cfgGet(item, "port", defaultPort), label+".port", 1, 65535)
		if err != nil {
			return nil, err
		}
		u, err := cfgUser(cfgGet(item, "user", defaultUser), label+".user")
		if err != nil {
			return nil, err
		}
		timeout, err := cfgInteger(cfgGet(item, "timeout_seconds", defaultTimeout), label+".timeout_seconds", 1, 60)
		if err != nil {
			return nil, err
		}
		tg, err := cfgTags(item.Value("tags"), label+".tags")
		if err != nil {
			return nil, err
		}
		note, err := cfgText(cfgGet(item, "note", ""), label+".note", 200, true)
		if err != nil {
			return nil, err
		}
		en, err := cfgBoolean(cfgGet(item, "enabled", true), label+".enabled")
		if err != nil {
			return nil, err
		}
		servers = append(servers, pyjson.NewObject(
			"name", n, "host", h, "port", port, "user", u, "tags", tg, "note", note,
			"enabled", en, "timeout_seconds", timeout))
	}
	q, err := cfgQueue(cfg.Value("queue"))
	if err != nil {
		return nil, err
	}
	return pyjson.NewObject(
		"enabled", enabled,
		"defaults", pyjson.NewObject("port", defaultPort, "user", defaultUser, "timeout_seconds", defaultTimeout),
		"servers", servers,
		"queue", q,
	), nil
}

// ---------------------------------------------------------------- loading

const cfgWindows = runtime.GOOS == "windows"

// purePath is str(Path(p)): separators collapsed, "." parts dropped, no
// trailing separator ("\" separators and drives on Windows).
func cfgPurePath(p string) string {
	sep := "/"
	drive, root := "", ""
	rest := p
	if cfgWindows {
		sep = `\`
		rest = strings.ReplaceAll(p, "/", `\`)
		if strings.HasPrefix(rest, `\\`) && !strings.HasPrefix(rest, `\\\`) {
			// UNC: \\server\share is the drive.
			parts := strings.SplitN(rest[2:], `\`, 3)
			if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
				drive = `\\` + parts[0] + `\` + parts[1]
				rest = ""
				if len(parts) == 3 {
					rest = `\` + parts[2]
				}
			}
		} else if len(rest) >= 2 && rest[1] == ':' {
			drive, rest = rest[:2], rest[2:]
		}
		if strings.HasPrefix(rest, `\`) {
			root = `\`
		}
		if drive != "" && strings.HasPrefix(drive, `\\`) {
			root = `\`
		}
	} else if strings.HasPrefix(rest, "/") {
		root = "/"
		if strings.HasPrefix(rest, "//") && !strings.HasPrefix(rest, "///") {
			root = "//"
		}
	}
	var parts []string
	for _, part := range strings.Split(rest, sep) {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	out := drive + root + strings.Join(parts, sep)
	if drive != "" && strings.HasPrefix(drive, `\\`) && len(parts) == 0 {
		out = drive + `\`
	}
	if out == "" {
		return "."
	}
	return out
}

// Windows: the MSVC CRT's _dosmaperr table (what open() reports); POSIX
// errnos pass through.
var winErrno = map[int]int{
	1: 22, 2: 2, 3: 2, 4: 24, 5: 13, 6: 9, 7: 12, 8: 12, 9: 12, 10: 7, 11: 8, 12: 22, 13: 22,
	15: 2, 16: 13, 17: 18, 18: 2, 33: 13, 53: 2, 65: 13, 67: 2, 80: 17, 82: 13, 83: 13, 87: 22,
	89: 11, 108: 13, 109: 32, 112: 28, 114: 9, 128: 10, 129: 10, 130: 9, 131: 22, 132: 13,
	145: 41, 158: 13, 161: 2, 164: 11, 167: 13, 183: 17, 206: 2, 215: 11, 1816: 12,
}

var strerrorWin = map[int]string{
	1: "Operation not permitted", 2: "No such file or directory", 5: "Input/output error",
	7: "Arg list too long", 8: "Exec format error", 9: "Bad file descriptor", 10: "No child processes",
	11: "Resource temporarily unavailable", 12: "Not enough space", 13: "Permission denied",
	17: "File exists", 18: "Improper link", 22: "Invalid argument", 24: "Too many open files",
	28: "No space left on device", 32: "Broken pipe", 38: "Filename too long", 41: "Directory not empty",
}

var strerrorPosix = map[int]string{
	1: "Operation not permitted", 2: "No such file or directory", 4: "Interrupted system call",
	5: "Input/output error", 6: "No such device or address", 9: "Bad file descriptor",
	11: "Resource temporarily unavailable", 12: "Cannot allocate memory", 13: "Permission denied",
	16: "Device or resource busy", 19: "No such device", 20: "Not a directory", 21: "Is a directory",
	22: "Invalid argument", 23: "Too many open files in system", 24: "Too many open files",
	26: "Text file busy", 36: "File name too long", 40: "Too many levels of symbolic links",
	75: "Value too large for defined data type", 95: "Operation not supported",
	116: "Stale file handle", 121: "Remote I/O error",
}

func cfgPyErrno(err error) (int, string, bool) {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return 0, "", false
	}
	code := int(errno)
	if cfgWindows {
		mapped, ok := winErrno[code]
		switch {
		case ok:
			code = mapped
		case code >= 19 && code <= 36:
			code = 13
		case code >= 188 && code <= 202:
			code = 8
		default:
			code = 22
		}
		if msg, ok := strerrorWin[code]; ok {
			return code, msg, true
		}
		return code, "Unknown error", true
	}
	if msg, ok := strerrorPosix[code]; ok {
		return code, msg, true
	}
	return code, errno.Error(), true
}

// osErrorText is str(OSError) for a failed operation on filename.
func cfgOsErrorText(err error, filename string) string {
	code, msg, ok := cfgPyErrno(err)
	if !ok {
		return err.Error()
	}
	if filename == "" {
		return fmt.Sprintf("[Errno %d] %s", code, msg)
	}
	return fmt.Sprintf("[Errno %d] %s: %s", code, msg, pyjson.Repr(filename))
}

// PythonError is a non-ConfigError exception load_config lets escape:
// ValueError for a path with a NUL, RuntimeError when "~user" cannot be
// expanded.
type PythonError struct{ Type, Msg string }

func (e *PythonError) Error() string { return e.Msg }

// cfgExpandUser is Path(p).expanduser() (Python 3.10) on str(Path(p)).
func cfgExpandUser(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	sep := "/"
	if cfgWindows {
		sep = `\`
	}
	first, rest, _ := strings.Cut(p, sep)
	if first == "~" {
		return pyos.ExpandUser(p), nil
	}
	fail := &PythonError{"RuntimeError", "Could not determine home directory."}
	username := first[1:]
	var home string
	if cfgWindows {
		// ntpath.expanduser: swap the last part of the current home.
		home = pyos.Home()
		if home == "" {
			return "", fail
		}
		if username != os.Getenv("USERNAME") {
			i := strings.LastIndexAny(home, `\/`)
			if os.Getenv("USERNAME") != home[i+1:] {
				return "", fail
			}
			home = home[:i+1] + username
		}
	} else {
		u, err := user.Lookup(username)
		if err != nil {
			return "", fail
		}
		home = strings.TrimRight(u.HomeDir, "/")
		if home == "" {
			home = "/"
		}
	}
	if !strings.Contains(p, sep) {
		return home, nil
	}
	return home + sep + rest, nil
}

// LoadConfig is load_config(path).  A missing file is a ConfigError with
// NotFound set and the path as message; a NUL in the path or an unknown
// "~user" gives a *PythonError (not a ConfigError, as in CPython).
func LoadConfig(path string) (*pyjson.Object, error) {
	expanded, err := cfgExpandUser(cfgPurePath(path))
	if err != nil {
		return nil, err
	}
	target := cfgPurePath(expanded)
	if strings.ContainsRune(target, 0) {
		return nil, &PythonError{"ValueError", "embedded null character"}
	}
	f, err := os.Open(target)
	if err != nil {
		if code, _, ok := cfgPyErrno(err); ok && code == 2 {
			return nil, &ConfigError{Msg: target, NotFound: true}
		}
		return nil, cfgErr("cannot read %s: %s", target, cfgOsErrorText(err, target))
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil && fi.IsDir() {
		if cfgWindows {
			return nil, cfgErr("cannot read %s: [Errno 13] Permission denied: %s", target, pyjson.Repr(target))
		}
		return nil, cfgErr("cannot read %s: [Errno 21] Is a directory: %s", target, pyjson.Repr(target))
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxConfigBytes+1))
	if err != nil {
		return nil, cfgErr("cannot read %s: %s", target, cfgOsErrorText(err, ""))
	}
	if len(raw) > MaxConfigBytes {
		return nil, cfgErr("%s is larger than %d KiB", target, MaxConfigBytes/1024)
	}
	raw = []byte(strings.TrimPrefix(string(raw), "\ufeff"))
	if !utf8.Valid(raw) {
		return nil, cfgErr("%s is not valid UTF-8", target)
	}
	parsed, err := ParseYAML(string(raw))
	if err != nil {
		return nil, err
	}
	return Validate(parsed)
}

var publicServerFields = []string{"name", "host", "port", "user", "tags", "note", "enabled", "timeout_seconds"}

// PublicServer is public_server(server): only the documented fields.
func PublicServer(server *pyjson.Object) *pyjson.Object {
	out := pyjson.NewObject()
	for _, k := range publicServerFields {
		if v, ok := server.Get(k); ok {
			out.Set(k, v)
		}
	}
	return out
}
