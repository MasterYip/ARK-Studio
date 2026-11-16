package skillkit

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// protocol.py

const ProtocolVersion = 2

var Phases = []string{"proposed", "ready", "active", "review", "evaluation", "accepted", "integrated"}

var Statuses = []string{"running", "waiting", "blocked", "completed", "archived", "superseded"}

var arkStatus = map[string]string{
	"running": "active", "waiting": "open", "blocked": "blocked",
	"completed": "done", "archived": "archived", "superseded": "archived",
}

var arkStatusByPhase = map[string]string{
	"proposed": "open", "ready": "open", "active": "active", "review": "active",
	"evaluation": "active", "accepted": "done", "integrated": "done",
}

type gateDef struct {
	id, title   string
	evidence    []string
	description string
}

var gates = []gateDef{
	{"scope", "Scope", []string{"changed_files", "owned_paths", "clean_diff"}, "declared files, ownership, clean diff, no secrets or bulk artifacts"},
	{"static", "Static", []string{"command", "result"}, "parse, format, lint, syntax, types"},
	{"unit", "Unit", []string{"command", "result", "test_count"}, "focused deterministic tests"},
	{"component", "Component smoke", []string{"command", "result", "artifact"}, "bounded smoke and import-origin checks"},
	{"integration", "Integration", []string{"command", "result", "revision"}, "controlled checkout/environment"},
	{"experiment", "Experiment", []string{"config", "data", "seed", "metrics", "artifact", "terminal_marker"}, "config, data, seed, metrics, artifacts, terminal marker"},
	{"publication", "Publication / deployment", []string{"claim_traceability", "authorized_checks"}, "claim traceability or authorized safety checks"},
}

var GateIDs = func() []string {
	out := make([]string, len(gates))
	for i, g := range gates {
		out[i] = g.id
	}
	return out
}()

var evidenceRetain = []string{"goal manifests", "inspection reports", "decision logs",
	"compact metrics and summaries", "checksums and artifact inventories", "selected figures"}

var evidenceOutside = []string{"bulk logs and raw traces", "datasets and checkpoints",
	"media collections and caches", "PID files and transfer bundles", "credentials and secret values"}

var (
	taskIDRe   = pytext.MustCompile(`\A([A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*)-(\d{3,})\n?\z`)
	serialRe   = pytext.MustCompile(`-(\d{3,})\n?\z`)
	prefixRe   = regexp.MustCompile(`\A[A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*\n?\z`)
	digitsOnly = func(s string) *big.Int {
		n, err := pytext.Int(s)
		if err == nil {
			return big.NewInt(n)
		}
		b := new(big.Int)
		for _, r := range s {
			b.Mul(b, big.NewInt(10))
			b.Add(b, big.NewInt(int64(digitOf(r))))
		}
		return b
	}
)

func digitOf(r rune) int {
	if r >= '0' && r <= '9' {
		return int(r - '0')
	}
	n, _ := pytext.Int(string(r))
	return int(n)
}

// UTCNow is protocol.utc_now() (the replay clock when one is set).
func UTCNow() string { return now() }

var now = clock.UTCNow

// parseTaskID is parse_task_id (only the validation matters to callers).
func parseTaskID(taskID string) error {
	m := taskIDRe.FindStringSubmatch(pytext.Strip(taskID))
	if m == nil {
		return errf("task IDs look like PREFIX-001 (uppercase prefix, dash, 3+ digit serial)")
	}
	if digitsOnly(m[2]).Sign() < 1 {
		return errf("task ID serial must be positive")
	}
	return nil
}

// taskSerial is task_serial(str(task_id or "").strip()).
func taskSerial(v any) *big.Int {
	s := ""
	if pyjson.Truthy(v) {
		s = str(v)
	}
	m := serialRe.FindStringSubmatch(pytext.Strip(s))
	if m == nil {
		return new(big.Int)
	}
	return digitsOnly(m[1])
}

// nextTaskID is next_task_id(existing, prefix).
func nextTaskID(existing []any, prefix string) (string, error) {
	if prefix == "" {
		prefix = "T"
	}
	prefix = pytext.Upper(pytext.Strip(prefix))
	if !prefixRe.MatchString(prefix) {
		return "", errf("prefix must be uppercase letters/digits separated by dashes")
	}
	highest := new(big.Int)
	widest := 0
	for _, id := range existing {
		if s := taskSerial(id); s.Cmp(highest) > 0 {
			highest = s
		}
		if m := serialRe.FindStringSubmatch(str(id)); m != nil {
			if n := utf8.RuneCountInString(m[1]); n > widest {
				widest = n
			}
		}
	}
	digits := 4
	if widest > digits {
		digits = widest
	}
	serial := new(big.Int).Add(highest, big.NewInt(1)).String()
	if len(serial) < digits {
		serial = strings.Repeat("0", digits-len(serial)) + serial
	}
	return prefix + "-" + serial, nil
}

// checkUniqueTaskID is check_unique_task_id.
func checkUniqueTaskID(taskID string, existing []any) error {
	target := taskSerial(taskID)
	if target.Sign() == 0 {
		return errf("task ID has no numeric serial: " + taskID)
	}
	for _, other := range existing {
		if pytext.Strip(str(other)) == pytext.Strip(taskID) {
			return errf("task ID already exists: " + taskID)
		}
		if taskSerial(other).Cmp(target) == 0 {
			return errf("task serial " + target.String() + " is already used by " + str(other) + "; serials are project-global")
		}
	}
	if target.Sign() < 1 {
		return errf("task ID serial must be positive")
	}
	return nil
}

func findGate(id string) (gateDef, error) {
	for _, g := range gates {
		if g.id == id {
			return g, nil
		}
	}
	return gateDef{}, errf("unknown gate: " + id)
}

// orderedGates is ordered_gates(gate_ids).
func orderedGates(ids []any) ([]string, error) {
	wanted := map[string]bool{}
	for _, g := range ids {
		id := pytext.Lower(pytext.Strip(str(g)))
		if _, err := findGate(id); err != nil {
			return nil, err
		}
		wanted[id] = true
	}
	out := []string{}
	for _, id := range GateIDs {
		if wanted[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// gateEvidence is gate_evidence(gate_ids).
func gateEvidence(ids []string) *obj {
	out := pyjson.NewObject()
	for _, id := range ids {
		g, _ := findGate(id)
		out.Set(id, strList(g.evidence))
	}
	return out
}

func bulletLines(items []any, numbered bool, empty string) string {
	lines := []string{}
	n := 0
	for _, item := range items {
		s := pytext.Strip(str(item))
		if s == "" {
			if numbered {
				n++
			}
			continue
		}
		n++
		if numbered {
			lines = append(lines, strconv.Itoa(n)+". "+s)
		} else {
			lines = append(lines, "- "+s)
		}
	}
	if len(lines) == 0 {
		return empty
	}
	return strings.Join(lines, "\n")
}

// bullets is _bullets(items) (iterating the value like Python's for loop).
func bullets(v any) (string, error) {
	items, err := iterate(v)
	if err != nil {
		return "", err
	}
	return bulletLines(items, false, "_None declared._"), nil
}

func numbered(v any) (string, error) {
	items, err := iterate(v)
	if err != nil {
		return "", err
	}
	return bulletLines(items, true, "_None declared._"), nil
}

func renderAcceptance(v any) string {
	lines := []string{}
	for i, raw := range asList(v) {
		var command, threshold, evidence string
		if o, ok := raw.(*obj); ok {
			command = pytext.Strip(str(or(o.Value("command"), "")))
			threshold = pytext.Strip(str(or(o.Value("threshold"), or(o.Value("expected"), ""))))
			evidence = pytext.Strip(str(or(o.Value("evidence"), "")))
		} else {
			command = pytext.Strip(str(or(raw, "")))
		}
		parts := []string{"(command not declared)"}
		if command != "" {
			parts[0] = "`" + command + "`"
		}
		if threshold != "" {
			parts = append(parts, "threshold: "+threshold)
		}
		if evidence != "" {
			parts = append(parts, "evidence: "+evidence)
		}
		lines = append(lines, strconv.Itoa(i+1)+". "+strings.Join(parts, " · "))
	}
	if len(lines) == 0 {
		return "_None declared._"
	}
	return strings.Join(lines, "\n")
}

func renderGates(v any) (string, error) {
	if !pyjson.Truthy(v) {
		return "_None declared._", nil
	}
	ids, err := orderedGates(asList(v))
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "_None declared._", nil
	}
	lines := []string{}
	for _, id := range ids {
		g, _ := findGate(id)
		lines = append(lines, "- **"+g.title+"** (`"+id+"`) — "+g.description)
	}
	return strings.Join(lines, "\n"), nil
}

func renderEvidence(v any) string {
	var retain, external []any
	if o, ok := v.(*obj); ok {
		retain = asList(or(o.Value("retain_in_git"), o.Value("retain")))
		external = asList(or(o.Value("external"), o.Value("outside_git")))
	} else {
		retain, external = asList(v), []any{}
	}
	if len(retain) == 0 {
		retain = strList(evidenceRetain)
	}
	if len(external) == 0 {
		external = strList(evidenceOutside)
	}
	return strings.Join([]string{
		"**Retained in Git**",
		bulletLines(retain, false, "_None declared._"),
		"",
		"**Kept in durable external storage with inventory + checksums**",
		bulletLines(external, false, "_None declared._"),
	}, "\n")
}

func renderMapping(v any) string {
	o, ok := v.(*obj)
	if !ok {
		if v == nil {
			return ""
		}
		return str(v)
	}
	lines := []string{}
	for _, k := range o.Keys() {
		label := strings.ReplaceAll(k, "_", " ")
		item := o.Value(k)
		var rendered string
		switch x := item.(type) {
		case bool:
			rendered = "no"
			if x {
				rendered = "yes"
			}
		case []any:
			parts := make([]string, len(x))
			for i, e := range x {
				parts[i] = str(e)
			}
			rendered = strings.Join(parts, ", ")
			if rendered == "" {
				rendered = "_none_"
			}
		default:
			rendered = str(or(item, "_none_"))
		}
		lines = append(lines, "- **"+label+"**: "+rendered)
	}
	if len(lines) == 0 {
		return "_None declared._"
	}
	return strings.Join(lines, "\n")
}

var bulletKeys = map[string]bool{"deliverables": true, "non_goals": true, "owned_paths": true,
	"stop_conditions": true, "risks": true, "commands": true, "changed_files": true,
	"artifact_inventory": true, "residual_risks": true, "deviations": true, "mediums": true,
	"qualities": true, "anti_goals": true, "shared_checks": true, "mode_checks": true}

// renderContextValue is render_context_value(key, value).
func renderContextValue(key string, v any) (string, error) {
	switch key {
	case "acceptance":
		return renderAcceptance(v), nil
	case "gates":
		return renderGates(v)
	case "evidence":
		return renderEvidence(v), nil
	case "dependencies":
		return bullets(v)
	case "team", "context_plan", "model_policy", "verification", "handoff":
		return renderMapping(v), nil
	case "failure_taxonomy":
		if !pyjson.Truthy(v) {
			return "_None declared._", nil
		}
		return bullets(v)
	}
	if bulletKeys[key] {
		if key == "commands" {
			return numbered(v)
		}
		return bullets(v)
	}
	if v == nil {
		return "", nil
	}
	return str(v), nil
}

func isIDStart(c byte) bool { return c == '_' || (c|0x20 >= 'a' && c|0x20 <= 'z') }
func isIDChar(c byte) bool  { return isIDStart(c) || (c >= '0' && c <= '9') }

// substitute is protocol.substitute(template, context): string.Template
// substitution of render_context_value() of every context value.
func substitute(template string, context *obj) (string, error) {
	mapping := map[string]string{}
	for _, k := range context.Keys() {
		v, err := renderContextValue(k, context.Value(k))
		if err != nil {
			return "", err
		}
		mapping[k] = v
	}
	var b strings.Builder
	for i := 0; i < len(template); {
		c := template[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		rest := template[i+1:]
		switch {
		case strings.HasPrefix(rest, "$"):
			b.WriteByte('$')
			i += 2
			continue
		case len(rest) > 0 && isIDStart(rest[0]):
			j := 1
			for j < len(rest) && isIDChar(rest[j]) {
				j++
			}
			name := rest[:j]
			v, ok := mapping[name]
			if !ok {
				return "", errf("missing template value: " + name)
			}
			b.WriteString(v)
			i += 1 + j
			continue
		case len(rest) > 1 && rest[0] == '{' && isIDStart(rest[1]):
			j := 2
			for j < len(rest) && isIDChar(rest[j]) {
				j++
			}
			if j < len(rest) && rest[j] == '}' {
				name := rest[1:j]
				v, ok := mapping[name]
				if !ok {
					return "", errf("missing template value: " + name)
				}
				b.WriteString(v)
				i += 2 + j
				continue
			}
		}
		// string.Template's "invalid" group: ValueError with line and column.
		// (Template._invalid: lines = template[:i].splitlines(keepends=True)).
		lines := pytext.SplitLines(template[:i+1], true)
		line, col := 1, 1
		if len(lines) > 0 {
			line = len(lines)
			col = utf8.RuneCountInString(template[:i+1]) - utf8.RuneCountInString(strings.Join(lines[:len(lines)-1], ""))
		}
		return "", errf("invalid template: Invalid placeholder in string: line " + strconv.Itoa(line) + ", col " + strconv.Itoa(col))
	}
	return b.String(), nil
}
