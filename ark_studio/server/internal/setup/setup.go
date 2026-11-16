// Package setup ports arkstudio/skills/setup.py (the 1.0 environment
// profile: normalisation, digest, questions, readiness, read-only discovery
// and verification, the confirmation summary) and setup_layers.py (the 2.0
// per-user resource register behind /api/agent/v1/setup and the setup
// block of the project overview).
//
// Values are the pyjson value model (dicts are *pyjson.Object in Python
// insertion order), so every payload and every file is built in the same key
// order as CPython.
package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/platform/clock"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyre"
	"arkstudio/go/internal/compat/pytext"
)

type obj = pyjson.Object

const (
	SkillID       = "setup"
	SetupVersion  = "1.0"
	ProfileFormat = "ark-environment"
	ProfileDir    = "setup"
	ProfileFile   = "environment.json"
	HistoryFile   = "history.jsonl"
)

var (
	States          = []string{"unset", "draft", "proposed", "confirmed", "verified", "stale"}
	ReadyStates     = []string{"confirmed", "verified"}
	repositoryRoles = []string{"primary", "code", "paper", "website", "data", "reference"}
	access          = []string{"read-write", "read-only"}
	taskSpaceKinds  = []string{"ark", "agent-team-folder", "issue-tracker", "other"}
	computeKinds    = []string{"local", "ssh", "slurm", "cloud"}
	computeUse      = []string{"any", "reserved", "ask"}
	envKinds        = []string{"conda", "venv", "uv", "poetry", "docker", "system", "other"}
	storageKinds    = []string{"artifacts", "datasets", "checkpoints", "logs", "media", "other"}
	policyValues    = []string{"allow", "ask", "deny"}
	// PolicyKeys / PolicyDefaults is POLICY_DEFAULTS in order.
	PolicyKeys     = []string{"commit", "push", "merge", "destructive", "process_termination", "gpu_dispatch", "external_message", "deployment", "data_transfer", "purchase"}
	PolicyDefaults = map[string]string{"commit": "allow", "push": "ask", "merge": "ask", "destructive": "ask",
		"process_termination": "ask", "gpu_dispatch": "ask", "external_message": "ask", "deployment": "ask",
		"data_transfer": "ask", "purchase": "deny"}
	policyNotes = map[string]string{
		"commit":              "local commits of reviewed, task-scoped changes",
		"push":                "pushing any branch to a remote",
		"merge":               "integrating a reviewed task branch into the base",
		"destructive":         "deleting or overwriting durable files, branches or worktrees",
		"process_termination": "killing agents, terminals or external jobs",
		"gpu_dispatch":        "starting jobs on configured compute",
		"external_message":    "email, chat, issue or review comments to other people",
		"deployment":          "publishing websites, packages or services",
		"data_transfer":       "uploading or copying data off this machine",
		"purchase":            "spending money (cloud, APIs, services)",
	}
	gateIDs = []string{"scope", "static", "unit", "component", "integration", "experiment", "publication"}

	secretRefRe    = pyre.MustCompile(`^(env|alias|keychain|ssh-config):[A-Za-z0-9_.\-/]{1,120}$`, 0)
	idRe           = pyre.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`, 0)
	secretKeyRe    = pyre.MustCompile(`(pass(word)?|secret|token|api[_-]?key|private[_-]?key|credential)`, pyre.FlagIgnoreCase)
	secretValueRes = []*pyre.Regexp{
		pyre.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`, 0),
		pyre.MustCompile(`\b(sk|rk|pk)-[A-Za-z0-9_\-]{16,}`, 0),
		pyre.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`, 0),
		pyre.MustCompile(`\bAKIA[0-9A-Z]{16}\b`, 0),
		pyre.MustCompile(`\bxox[abpr]-[A-Za-z0-9\-]{10,}`, 0),
		pyre.MustCompile(`[a-z][a-z0-9+.\-]*://[^/\s:@]+:[^/\s@]+@`, pyre.FlagIgnoreCase),
	}
	localPathRe    = pyre.MustCompile(`^([A-Za-z]:[\\/]|/|~)`, 0)
	remoteUserRe   = pyre.MustCompile(`(://)[^/@\s]+@`, 0)
	prefixRe       = pyre.MustCompile(`^[A-Z][A-Z0-9]*(?:-[A-Z][A-Z0-9]*)*$`, 0)
	projectMarkers = []string{"pyproject.toml", "setup.py", "requirements.txt", "environment.yml",
		"environment.yaml", "package.json", "Makefile", "Cargo.toml", "CMakeLists.txt",
		"go.mod", "AGENTS.md", "CLAUDE.md", "README.md"}
	taskSpaceMarkers = []string{"agent_team_init.md", "setup_answers.yaml", "task_status.md"}
)

// Error is SetupError (a ValueError with an HTTP status and code).
type Error struct {
	Message string
	Status  int
	Code    string
}

func (e *Error) Error() string { return e.Message }

func setupErr(msg string, status int, code string) *Error {
	if status == 0 {
		status = 400
	}
	if code == "" {
		code = "SETUP_INVALID"
	}
	return &Error{msg, status, code}
}

// ------------------------------------------------------------- helpers

func in(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// inAny is `value in (tuple of str)` for any decoded JSON value.
func inAny(v any, list []string) bool {
	s, ok := v.(string)
	return ok && in(s, list)
}

func search(re *pyre.Regexp, s string) bool {
	budget := 1 << 40
	_, ok, _ := re.SearchFrom([]rune(s), 0, -1, &budget)
	return ok
}

// sub is re.sub(pattern, repl, s) for a literal replacement.
func sub(re *pyre.Regexp, repl, s string) string {
	budget := 1 << 40
	rs := []rune(s)
	matches, _ := re.FindAll(rs, &budget)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(string(rs[last:m.Start]))
		b.WriteString(repl)
		last = m.End
	}
	b.WriteString(string(rs[last:]))
	return b.String()
}

// get is d.get(key) for a value that may not be a dict.
func get(o *obj, key string) any { return o.Value(key) }

// mapOf is the value as a Mapping (nil when it is not one).
func mapOf(v any) *obj {
	o, _ := v.(*obj)
	return o
}

// orMap is `(v or {})` for a dict-or-None value.
func orMap(v any) *obj {
	if o, ok := v.(*obj); ok && o != nil {
		return o
	}
	return pyjson.NewObject()
}

// str is str(v); strNone is str(v) with None -> "".
func str(v any) string { return pyjson.Str(v) }

// text is _text(value, limit): str(value or "" for None).strip()[:limit].
func text(v any, limit int) string {
	if v == nil {
		return ""
	}
	return pytext.Prefix(pytext.Strip(pyjson.Str(v)), limit)
}

// orStr is `str(v or "")`.
func orStr(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// or is Python's `a or b` for values.
func or(a, b any) any {
	if pyjson.Truthy(a) {
		return a
	}
	return b
}

// list is _list(value).
func list(v any) []any {
	if v == nil {
		return []any{}
	}
	if s, ok := v.(string); ok && s == "" {
		return []any{}
	}
	if l, ok := v.([]any); ok {
		return append([]any{}, l...)
	}
	return []any{v}
}

// listOr is `value or []` for a list value (non-lists iterate like Python
// only for str and dict; other types are treated as empty).
func listOr(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []*obj:
		out := make([]any, len(x))
		for i, o := range x {
			out[i] = o
		}
		return out
	case string:
		out := []any{}
		for _, r := range x {
			out = append(out, string(r))
		}
		return out
	case *obj:
		out := []any{}
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out
	}
	return []any{}
}

func head(l []any, n int) []any {
	if len(l) > n {
		return l[:n]
	}
	return l
}

func lower(s string) string { return pytext.Lower(s) }

func choice(v any, choices []string, def, field string, errs *[]string) string {
	t := lower(text(v, 40))
	if t == "" {
		t = def
	}
	if !in(t, choices) {
		*errs = append(*errs, fmt.Sprintf("%s: '%s' is not one of %s", field, t, strings.Join(choices, ", ")))
		return def
	}
	return t
}

func ident(v any, fallback, field string, errs *[]string) string {
	t := lower(text(v, 48))
	if t == "" {
		t = fallback
	}
	if !search(idRe, t) {
		*errs = append(*errs, field+": id must be lowercase letters, digits, '.', '_' or '-'")
	}
	return t
}

func texts(v any, limit, n int) []any {
	out := []any{}
	for _, x := range list(v) {
		out = append(out, text(x, limit))
	}
	return head(out, n)
}

// MachineFingerprint is machine_fingerprint(): sha256(hostname|system|home)[:12].
func MachineFingerprint() string {
	host, _ := os.Hostname()
	home, _ := os.UserHomeDir()
	sum := sha256.Sum256([]byte(host + "|" + PlatformSystem() + "|" + home))
	return hex.EncodeToString(sum[:])[:12]
}

// PlatformSystem is platform.system().
func PlatformSystem() string {
	if s, ok := map[string]string{"windows": "Windows", "linux": "Linux", "darwin": "Darwin", "freebsd": "FreeBSD"}[runtime.GOOS]; ok {
		return s
	}
	return ""
}

// IsLocalPath is is_local_path.
func IsLocalPath(v string) bool { return search(localPathRe, v) }

// SanitizeRemote drops user-info from a remote URL.
func SanitizeRemote(url any) string { return sub(remoteUserRe, "://", text(url, 400)) }

// FindSecretValues returns the JSON paths whose content looks like a secret.
func FindSecretValues(v any, path string) []string {
	hits := []string{}
	switch x := v.(type) {
	case *obj:
		for _, key := range x.Keys() {
			item := x.Value(key)
			p := key
			if path != "" {
				p = path + "." + key
			}
			if s, ok := item.(string); ok && s != "" && search(secretKeyRe, key) && !search(secretRefRe, s) && key != "name" {
				hits = append(hits, p)
				continue
			}
			hits = append(hits, FindSecretValues(item, p)...)
		}
	case []any:
		for i, item := range x {
			hits = append(hits, FindSecretValues(item, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case string:
		for _, re := range secretValueRes {
			if search(re, x) {
				if path == "" {
					path = "<value>"
				}
				hits = append(hits, path)
				break
			}
		}
	}
	return hits
}

// ------------------------------------------------------------- normalisation

type builder func(item *obj, i int, errs, warns *[]string) *obj

func entries(raw any, field string, errs *[]string, build func(*obj, int) *obj) []any {
	out := []any{}
	seen := map[string]bool{}
	for i, item := range head(list(raw), 50) {
		m := mapOf(item)
		if m == nil {
			*errs = append(*errs, fmt.Sprintf("%s[%d] must be an object", field, i))
			continue
		}
		e := build(m, i)
		id := str(e.Value("id"))
		if seen[id] {
			*errs = append(*errs, fmt.Sprintf("%s: duplicate id '%s'", field, id))
		}
		seen[id] = true
		out = append(out, e)
	}
	return out
}

func buildRepository(item *obj, i int, errs, warns *[]string) *obj {
	path := text(get(item, "path"), 500)
	if path == "" {
		*errs = append(*errs, fmt.Sprintf("repositories[%d].path is required", i))
	}
	o := pyjson.NewObject()
	o.Set("id", ident(get(item, "id"), fmt.Sprintf("repo-%d", i+1), fmt.Sprintf("repositories[%d]", i), errs))
	o.Set("path", path)
	o.Set("role", choice(get(item, "role"), repositoryRoles, "code", fmt.Sprintf("repositories[%d].role", i), errs))
	o.Set("access", choice(get(item, "access"), access, "read-write", fmt.Sprintf("repositories[%d].access", i), errs))
	o.Set("default_branch", text(get(item, "default_branch"), 120))
	wt := text(get(item, "worktree_root"), 200)
	if wt == "" {
		wt = ".worktrees"
	}
	o.Set("worktree_root", wt)
	o.Set("remote", SanitizeRemote(or(get(item, "remote"), "")))
	o.Set("instructions", texts(get(item, "instructions"), 300, 20))
	o.Set("note", text(get(item, "note"), 500))
	return o
}

func buildTaskSpace(item *obj, i int, errs, warns *[]string) *obj {
	o := pyjson.NewObject()
	o.Set("id", ident(get(item, "id"), fmt.Sprintf("space-%d", i+1), fmt.Sprintf("task_spaces[%d]", i), errs))
	o.Set("kind", choice(get(item, "kind"), taskSpaceKinds, "other", fmt.Sprintf("task_spaces[%d].kind", i), errs))
	o.Set("location", text(or(get(item, "location"), get(item, "path")), 500))
	o.Set("access", choice(get(item, "access"), access, "read-only", fmt.Sprintf("task_spaces[%d].access", i), errs))
	rel := text(get(item, "relation"), 300)
	if rel == "" {
		rel = "reference"
	}
	o.Set("relation", rel)
	o.Set("note", text(get(item, "note"), 500))
	return o
}

func buildCompute(item *obj, i int, errs, warns *[]string) *obj {
	kind := choice(get(item, "kind"), computeKinds, "local", fmt.Sprintf("compute[%d].kind", i), errs)
	o := pyjson.NewObject()
	o.Set("id", ident(get(item, "id"), fmt.Sprintf("compute-%d", i+1), fmt.Sprintf("compute[%d]", i), errs))
	o.Set("kind", kind)
	o.Set("gpu_server", text(get(item, "gpu_server"), 64))
	o.Set("gpus", text(get(item, "gpus"), 200))
	o.Set("use", choice(get(item, "use"), computeUse, "ask", fmt.Sprintf("compute[%d].use", i), errs))
	o.Set("scratch", text(get(item, "scratch"), 500))
	o.Set("note", text(get(item, "note"), 500))
	if kind == "ssh" && o.Value("gpu_server") == "" {
		*warns = append(*warns, fmt.Sprintf("compute[%d]: ssh compute should name a gpu_server from "+
			"the private GPU server list (no hosts or keys here)", i))
	}
	return o
}

func buildEnvironment(item *obj, i int, errs, warns *[]string) *obj {
	o := pyjson.NewObject()
	o.Set("id", ident(get(item, "id"), fmt.Sprintf("env-%d", i+1), fmt.Sprintf("environments[%d]", i), errs))
	o.Set("kind", choice(get(item, "kind"), envKinds, "other", fmt.Sprintf("environments[%d].kind", i), errs))
	o.Set("repository", lower(text(get(item, "repository"), 48)))
	o.Set("compute", lower(text(get(item, "compute"), 48)))
	o.Set("activate", text(get(item, "activate"), 500))
	o.Set("python", text(get(item, "python"), 40))
	o.Set("note", text(get(item, "note"), 500))
	return o
}

func buildStorage(item *obj, i int, errs, warns *[]string) *obj {
	o := pyjson.NewObject()
	o.Set("id", ident(get(item, "id"), fmt.Sprintf("store-%d", i+1), fmt.Sprintf("storage[%d]", i), errs))
	o.Set("kind", choice(get(item, "kind"), storageKinds, "other", fmt.Sprintf("storage[%d].kind", i), errs))
	o.Set("location", text(or(get(item, "location"), get(item, "path")), 500))
	o.Set("retention", text(get(item, "retention"), 300))
	o.Set("note", text(get(item, "note"), 500))
	return o
}

// boolDefault is bool(item.get(key, def)).
func boolDefault(item *obj, key string, def bool) bool {
	if v, ok := item.Get(key); ok {
		return pyjson.Truthy(v)
	}
	return def
}

func buildCommand(item *obj, i int, errs, warns *[]string) *obj {
	gate := lower(text(get(item, "gate"), 40))
	if gate != "" && !in(gate, gateIDs) {
		*errs = append(*errs, fmt.Sprintf("commands[%d].gate: '%s' is not one of %s", i, gate, strings.Join(gateIDs, ", ")))
	}
	run := text(get(item, "run"), 1000)
	if run == "" {
		*errs = append(*errs, fmt.Sprintf("commands[%d].run is required", i))
	}
	o := pyjson.NewObject()
	o.Set("id", ident(get(item, "id"), fmt.Sprintf("cmd-%d", i+1), fmt.Sprintf("commands[%d]", i), errs))
	o.Set("run", run)
	o.Set("repository", lower(text(get(item, "repository"), 48)))
	o.Set("environment", lower(text(get(item, "environment"), 48)))
	o.Set("cwd", text(get(item, "cwd"), 300))
	o.Set("gate", gate)
	o.Set("mandatory", boolDefault(item, "mandatory", gate == "static" || gate == "unit"))
	o.Set("expensive", boolDefault(item, "expensive", gate == "experiment" || gate == "publication"))
	o.Set("proposed", boolDefault(item, "proposed", false))
	return o
}

func buildSecret(item *obj, i int, errs, warns *[]string) *obj {
	ref := text(get(item, "ref"), 160)
	if !search(secretRefRe, ref) {
		*errs = append(*errs, fmt.Sprintf("secrets[%d].ref must be a reference such as env:NAME, "+
			"alias:name or ssh-config:host; never a value", i))
	}
	o := pyjson.NewObject()
	o.Set("id", ident(get(item, "id"), fmt.Sprintf("secret-%d", i+1), fmt.Sprintf("secrets[%d]", i), errs))
	o.Set("ref", ref)
	o.Set("purpose", text(get(item, "purpose"), 300))
	return o
}

// Kinds / KindFields is KIND_FIELDS in order.
var (
	Kinds      = []string{"repository", "task_space", "compute", "environment", "storage", "command", "secret"}
	KindFields = map[string]string{"repository": "repositories", "task_space": "task_spaces", "compute": "compute",
		"environment": "environments", "storage": "storage", "command": "commands", "secret": "secrets"}
	builders = map[string]builder{"repository": buildRepository, "task_space": buildTaskSpace,
		"compute": buildCompute, "environment": buildEnvironment, "storage": buildStorage,
		"command": buildCommand, "secret": buildSecret}
)

// NormalizeEntry is normalize_entry.
func NormalizeEntry(kind string, item *obj, index int) (*obj, []string, []string) {
	errs, warns := []string{}, []string{}
	return builders[kind](item, index, &errs, &warns), errs, warns
}

// pyInt is int(v) for the profile's integer fields: ok=false is a
// TypeError/ValueError.  big reports the sign of values beyond int64.
func pyInt(v any) (n int64, ok bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int64:
		return x, true
	case *big.Int:
		if x.Sign() < 0 {
			return math.MinInt64, true
		}
		return math.MaxInt64, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		t := math.Trunc(x)
		if t >= 9.2e18 {
			return math.MaxInt64, true
		}
		if t <= -9.2e18 {
			return math.MinInt64, true
		}
		return int64(t), true
	case string:
		n, err := pytext.Int(x)
		if err == nil {
			return n, true
		}
		if _, isValue := err.(*pytext.IntError); isValue {
			return 0, false
		}
		if strings.HasPrefix(pytext.Strip(x), "-") {
			return math.MinInt64, true
		}
		return math.MaxInt64, true
	}
	return 0, false
}

func clamp(n, lo, hi int64) int64 { return max(lo, min(n, hi)) }

// upper is str.upper() with the special casings that grow a string.
func upper(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case 'ß':
			b.WriteString("SS")
		case 'ﬀ':
			b.WriteString("FF")
		case 'ﬁ':
			b.WriteString("FI")
		case 'ﬂ':
			b.WriteString("FL")
		case 'ﬃ':
			b.WriteString("FFI")
		case 'ﬄ':
			b.WriteString("FFL")
		case 'ﬅ', 'ﬆ':
			b.WriteString("ST")
		default:
			b.WriteString(strings.ToUpper(string(r)))
		}
	}
	return b.String()
}

// Stages is STAGES.
var (
	stageIDs   = []string{"scope", "code", "team", "compute", "commands", "authority"}
	skipEffect = map[string][2]string{
		"scope":     {"plan", "objective skipped by the user"},
		"code":      {"implement", "codebase setup skipped by the user"},
		"team":      {"", "team defaults apply (agents ask the user to review)"},
		"compute":   {"gpu", "compute setup skipped by the user"},
		"commands":  {"gates", "gate commands skipped; agents must ask for test commands per task"},
		"authority": {"", "authority defaults apply (every risky action asks the user)"},
	}
)

// NormalizeProfile is normalize_profile: (profile, errors, warnings).
func NormalizeProfile(profile *obj) (*obj, []string, []string) {
	if profile == nil {
		profile = pyjson.NewObject()
	}
	errs, warns := []string{}, []string{}
	b := func(fn builder) func(*obj, int) *obj {
		return func(item *obj, i int) *obj { return fn(item, i, &errs, &warns) }
	}
	repositories := entries(get(profile, "repositories"), "repositories", &errs, b(buildRepository))
	out := pyjson.NewObject("format", ProfileFormat, "setup_version", SetupVersion)
	out.Set("objective", text(get(profile, "objective"), 4000))
	out.Set("non_goals", texts(get(profile, "non_goals"), 500, 30))
	out.Set("repositories", repositories)
	out.Set("task_spaces", entries(get(profile, "task_spaces"), "task_spaces", &errs, b(buildTaskSpace)))
	out.Set("compute", entries(get(profile, "compute"), "compute", &errs, b(buildCompute)))
	out.Set("environments", entries(get(profile, "environments"), "environments", &errs, b(buildEnvironment)))
	out.Set("storage", entries(get(profile, "storage"), "storage", &errs, b(buildStorage)))
	out.Set("commands", entries(get(profile, "commands"), "commands", &errs, b(buildCommand)))
	out.Set("secrets", entries(get(profile, "secrets"), "secrets", &errs, b(buildSecret)))

	agents := orMap(mapOf(get(profile, "agents")))
	var concurrency any = int64(3)
	if v, ok := agents.Get("concurrency"); ok {
		concurrency = v
	}
	if lower(str(concurrency)) != "unlimited" {
		if n, ok := pyInt(concurrency); ok {
			concurrency = clamp(n, 1, 1000)
		} else {
			errs = append(errs, "agents.concurrency must be a positive integer or 'unlimited'")
			concurrency = int64(3)
		}
	} else {
		concurrency = "unlimited"
	}
	var contextTokens int64
	if n, ok := pyInt(or(get(agents, "default_context_tokens"), int64(120000))); ok {
		contextTokens = clamp(n, 8000, 2000000)
	} else {
		errs = append(errs, "agents.default_context_tokens must be an integer")
		contextTokens = 120000
	}
	out.Set("agents", pyjson.NewObject(
		"coordinator", text(get(agents, "coordinator"), 60),
		"providers", texts(get(agents, "providers"), 40, 20),
		"concurrency", concurrency,
		"default_reviewer", text(get(agents, "default_reviewer"), 80),
		"default_context_tokens", contextTokens))

	rawPolicies := orMap(mapOf(get(profile, "policies")))
	policies, defaulted := pyjson.NewObject(), []any{}
	for _, key := range PolicyKeys {
		if rawPolicies.Has(key) {
			policies.Set(key, choice(rawPolicies.Value(key), policyValues, PolicyDefaults[key], "policies."+key, &errs))
		} else {
			policies.Set(key, PolicyDefaults[key])
			defaulted = append(defaulted, key)
		}
	}
	for _, key := range rawPolicies.Keys() {
		if _, known := PolicyDefaults[key]; !known {
			warns = append(warns, "policies."+key+" is not a known policy and is ignored")
		}
	}
	out.Set("policies", policies)
	out.Set("policies_defaulted", defaulted)

	cadence := orMap(mapOf(get(profile, "cadence")))
	var minutes int64
	if n, ok := pyInt(or(get(cadence, "status_minutes"), int64(10))); ok {
		minutes = clamp(n, 1, 1440)
	} else {
		errs = append(errs, "cadence.status_minutes must be an integer")
		minutes = 10
	}
	out.Set("cadence", pyjson.NewObject("status_minutes", minutes, "task_trigger", text(get(cadence, "task_trigger"), 500)))
	ids := orMap(mapOf(get(profile, "task_ids")))
	prefixes := []any{}
	for _, x := range list(get(ids, "prefixes")) {
		prefixes = append(prefixes, upper(text(x, 20)))
	}
	prefixes = head(prefixes, 30)
	if len(prefixes) == 0 {
		prefixes = []any{"T"}
	}
	for _, p := range prefixes {
		if !search(prefixRe, p.(string)) {
			errs = append(errs, fmt.Sprintf("task_ids.prefixes: '%s' is not a valid prefix", p))
		}
	}
	out.Set("task_ids", pyjson.NewObject("prefixes", prefixes, "scope", "project-global"))

	unknowns := []any{}
	for _, item := range head(list(get(profile, "unknowns")), 50) {
		if m := mapOf(item); m != nil {
			owner := text(get(m, "owner"), 80)
			if owner == "" {
				owner = "user"
			}
			unknowns = append(unknowns, pyjson.NewObject("field", text(get(m, "field"), 120),
				"blocks", text(get(m, "blocks"), 300), "owner", owner))
		} else {
			unknowns = append(unknowns, pyjson.NewObject("field", text(item, 120), "blocks", "", "owner", "user"))
		}
	}
	out.Set("unknowns", unknowns)
	skipped := []any{}
	seenSkip := map[string]bool{}
	for _, item := range list(get(profile, "skipped")) {
		stage := lower(text(item, 20))
		if !in(stage, stageIDs) {
			errs = append(errs, fmt.Sprintf("skipped: '%s' is not one of %s", stage, strings.Join(stageIDs, ", ")))
		} else if !seenSkip[stage] {
			seenSkip[stage] = true
			skipped = append(skipped, stage)
		}
	}
	out.Set("skipped", skipped)
	out.Set("notes", text(get(profile, "notes"), 4000))

	repoIDs, envIDs, computeIDs := idSet(repositories), idSet(out.Value("environments").([]any)), idSet(out.Value("compute").([]any))
	for _, x := range out.Value("environments").([]any) {
		e := x.(*obj)
		if r := str(e.Value("repository")); r != "" && !repoIDs[r] {
			errs = append(errs, fmt.Sprintf("environments.%s: unknown repository '%s'", str(e.Value("id")), r))
		}
		if c := str(e.Value("compute")); c != "" && !computeIDs[c] {
			errs = append(errs, fmt.Sprintf("environments.%s: unknown compute '%s'", str(e.Value("id")), c))
		}
	}
	for _, x := range out.Value("commands").([]any) {
		c := x.(*obj)
		if r := str(c.Value("repository")); r != "" && !repoIDs[r] {
			errs = append(errs, fmt.Sprintf("commands.%s: unknown repository '%s'", str(c.Value("id")), r))
		}
		if e := str(c.Value("environment")); e != "" && !envIDs[e] {
			errs = append(errs, fmt.Sprintf("commands.%s: unknown environment '%s'", str(c.Value("id")), e))
		}
	}
	for _, x := range repositories {
		r := x.(*obj)
		if r.Value("role") == "primary" && r.Value("access") == "read-only" {
			warns = append(warns, fmt.Sprintf("repositories.%s: this primary repository is read-only; "+
				"agents will not be able to implement changes there", str(r.Value("id"))))
		}
	}
	for _, hit := range FindSecretValues(profile, "") {
		errs = append(errs, hit+": looks like a secret value; store a reference (env:NAME) instead")
	}
	return out, errs, warns
}

func idSet(items []any) map[string]bool {
	out := map[string]bool{}
	for _, x := range items {
		out[str(x.(*obj).Value("id"))] = true
	}
	return out
}

// Digest is digest(): sha256 of the sorted, compact JSON, first 16 hex.
func Digest(profile any) string {
	sum := sha256.Sum256(sortedJSON(nil, profile))
	return hex.EncodeToString(sum[:])[:16]
}

// sortedJSON is json.dumps(v, sort_keys=True, ensure_ascii=False, separators=(",", ":")).
func sortedJSON(b []byte, v any) []byte {
	switch x := v.(type) {
	case *obj:
		if x == nil {
			return append(b, "null"...)
		}
		keys := append([]string(nil), x.Keys()...)
		sort.Strings(keys)
		b = append(b, '{')
		for i, k := range keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = pyjson.AppendString(b, k)
			b = append(b, ':')
			b = sortedJSON(b, x.Value(k))
		}
		return append(b, '}')
	case []any:
		b = append(b, '[')
		for i, item := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = sortedJSON(b, item)
		}
		return append(b, ']')
	case []string:
		b = append(b, '[')
		for i, item := range x {
			if i > 0 {
				b = append(b, ',')
			}
			b = pyjson.AppendString(b, item)
		}
		return append(b, ']')
	}
	return pyjson.Append(b, v)
}

// ------------------------------------------------------------- questions and readiness

// Questions is questions(profile).
func Questions(profile *obj) []any {
	out := []*obj{}
	ask := func(stage, field, prompt, blocks string, required bool) {
		out = append(out, pyjson.NewObject("stage", stage, "field", field, "prompt", prompt, "blocks", blocks, "required", required))
	}
	if !pyjson.Truthy(get(profile, "objective")) {
		ask("scope", "objective", "What is the measurable research objective for this environment?",
			"planning and every task manifest", true)
	}
	if !pyjson.Truthy(get(profile, "non_goals")) {
		ask("scope", "non_goals", "What is explicitly out of scope?", "scope gates and owned-path review", false)
	}
	repositories := listOr(get(profile, "repositories"))
	if len(repositories) == 0 {
		ask("code", "repositories", "Which repositories/codebases does this project use, and which "+
			"ones are primary (where implementation tasks land)?", "worktrees and implementation tasks", true)
	} else if !anyField(repositories, "role", "primary") {
		ask("code", "repositories[].role", "Which repository is primary?", "default task repository and integration", false)
	}
	for _, x := range repositories {
		item := mapOf(x)
		if !pyjson.Truthy(get(item, "default_branch")) {
			id := str(get(item, "id"))
			ask("code", "repositories."+id+".default_branch",
				"Which branch is the integration base for '"+id+"'?", "base_revision in task manifests", true)
		}
	}
	if !pyjson.Truthy(get(profile, "task_spaces")) {
		ask("team", "task_spaces", "Are there existing task spaces (agent-team folders, issue "+
			"trackers, other ARK projects) to import or reference?", "task-ID uniqueness against historical work", false)
	}
	agents := orMap(get(profile, "agents"))
	if !pyjson.Truthy(get(agents, "providers")) {
		ask("team", "agents.providers", "Which coding agents will work on this project (DSH, Codex, "+
			"Claude Code, OpenCode...), and with which models or permission levels? ARK's "+
			"Settings -> Providers shows what is installed here.", "task-session launch and model routing", false)
	}
	if !pyjson.Truthy(get(agents, "coordinator")) {
		ask("team", "agents.coordinator", "Which agent coordinates the team (DSH, Codex, Claude Code, "+
			"a human)?", "delegation and monitoring", false)
	}
	if !pyjson.Truthy(get(agents, "default_reviewer")) {
		ask("team", "agents.default_reviewer", "Who accepts work by default (a person or an "+
			"independent reviewer agent)?", "independent acceptance", true)
	}
	if !pyjson.Truthy(get(orMap(get(profile, "cadence")), "task_trigger")) {
		ask("team", "cadence.task_trigger", "When should an agent open a formal task record?", "task creation", false)
	}
	if !pyjson.Truthy(get(profile, "compute")) {
		ask("compute", "compute", "Which compute may agents use (this machine, GPU servers, a "+
			"cluster)? GPU servers come from ARK's private server list ($ARK_GPU_SERVERS_CONFIG, "+
			"$ARK_PRIVATE_DIR/gpu_servers.yaml or ~/.ark/gpu_servers.yaml; see docs/operations/gpu.md). "+
			"Name servers from that list; never paste hosts with credentials or keys.",
			"experiment gates and GPU dispatch", false)
	}
	if !pyjson.Truthy(get(profile, "environments")) {
		ask("compute", "environments", "How is each codebase's environment activated "+
			"(conda/venv/uv/docker)?", "reproducible gate commands", false)
	}
	if !anyField(listOr(get(profile, "storage")), "kind", "artifacts") {
		ask("compute", "storage", "Where do bulk artifacts (logs, checkpoints, media) live "+
			"outside Git?", "evidence retention", false)
	}
	commands := listOr(get(profile, "commands"))
	if !anyField(commands, "gate", "unit") {
		ask("commands", "commands[gate=unit]", "What is the fast deterministic test command?",
			"the unit gate for implementation tasks", true)
	}
	for _, x := range commands {
		if pyjson.Truthy(get(mapOf(x), "proposed")) {
			ask("commands", "commands[].proposed", "Confirm or correct the commands ARK discovered "+
				"(marked proposed).", "gates", false)
			break
		}
	}
	defaulted := listOr(get(profile, "policies_defaulted"))
	if len(defaulted) > 0 {
		parts := make([]string, len(defaulted))
		for i, k := range defaulted {
			parts[i] = str(k) + "=" + PolicyDefaults[str(k)]
		}
		ask("authority", "policies", "Confirm the authority policy defaults: "+strings.Join(parts, ", ")+".",
			"any action beyond local edits", true)
	}
	skipped := map[string]bool{}
	for _, s := range listOr(get(profile, "skipped")) {
		if t, ok := s.(string); ok {
			skipped[t] = true
		}
	}
	res := []any{}
	for _, q := range out {
		if !skipped[q.Value("stage").(string)] {
			res = append(res, q)
		}
	}
	return res
}

// anyField is any(item.get(field) == value for item in items).
func anyField(items []any, field, value string) bool {
	for _, x := range items {
		if v, ok := get(mapOf(x), field).(string); ok && v == value {
			return true
		}
	}
	return false
}

var capabilities = []string{"plan", "tasks", "implement", "gates", "gpu"}

// Readiness is readiness(profile, state).
func Readiness(profile *obj, state string) *obj {
	blocked := []*obj{}
	confirmed := in(state, ReadyStates)
	skipped := []any{}
	for _, s := range listOr(get(profile, "skipped")) {
		if t, ok := s.(string); ok {
			if _, known := skipEffect[t]; known {
				skipped = append(skipped, t)
			}
		}
	}
	for _, s := range skipped {
		eff := skipEffect[s.(string)]
		if eff[0] != "" {
			blocked = append(blocked, pyjson.NewObject("capability", eff[0], "reason", eff[1], "skipped", true))
		}
	}
	repositories := listOr(get(profile, "repositories"))
	var primaries []*obj
	for _, x := range repositories {
		if m := mapOf(x); get(m, "role") == "primary" {
			primaries = append(primaries, m)
		}
	}
	var primary *obj
	if len(primaries) > 0 {
		primary = primaries[0]
	} else if len(repositories) > 0 {
		primary = mapOf(repositories[0])
	}
	if !confirmed {
		blocked = append(blocked, pyjson.NewObject("capability", "tasks", "reason", "setup is '"+state+"'; a human must "+
			"confirm the environment profile before task work starts"))
	}
	if !pyjson.Truthy(get(profile, "objective")) {
		blocked = append(blocked, pyjson.NewObject("capability", "plan", "reason", "no objective recorded"))
	}
	if len(primaries) == 0 {
		blocked = append(blocked, pyjson.NewObject("capability", "implement", "reason", "no primary repository registered"))
	} else {
		rw := false
		for _, p := range primaries {
			if get(p, "access") == "read-write" {
				rw = true
			}
		}
		if !rw {
			blocked = append(blocked, pyjson.NewObject("capability", "implement", "reason", "all primary repositories are read-only"))
		}
	}
	if !anyField(listOr(get(profile, "commands")), "gate", "unit") {
		blocked = append(blocked, pyjson.NewObject("capability", "gates", "reason", "no unit-gate command recorded"))
	}
	policies := orMap(get(profile, "policies"))
	if !pyjson.Truthy(get(profile, "compute")) {
		blocked = append(blocked, pyjson.NewObject("capability", "gpu", "reason", "no compute registered"))
	} else if get(policies, "gpu_dispatch") == "deny" {
		blocked = append(blocked, pyjson.NewObject("capability", "gpu", "reason", "gpu_dispatch policy is deny"))
	}
	blocked = dedupeBlocked(blocked)
	ids := map[string]bool{}
	for _, b := range blocked {
		ids[b.Value("capability").(string)] = true
	}
	ready := confirmed
	for _, b := range blocked {
		c := b.Value("capability").(string)
		if (c == "plan" || c == "tasks") && !pyjson.Truthy(b.Value("skipped")) {
			ready = false
		}
	}
	notes := []any{}
	for _, s := range skipped {
		if eff := skipEffect[s.(string)]; eff[0] == "" {
			notes = append(notes, eff[1])
		}
	}
	allowed := []any{}
	for _, c := range capabilities {
		if !ids[c] {
			allowed = append(allowed, c)
		}
	}
	requires, denied := pyjson.NewObject(), []string{}
	for _, k := range policies.Keys() {
		v := policies.Value(k)
		if v == "ask" {
			requires.Set(k, v)
		}
		if v == "deny" {
			denied = append(denied, k)
		}
	}
	sort.Strings(denied)
	deniedAny := make([]any, len(denied))
	for i, d := range denied {
		deniedAny[i] = d
	}
	primaryID := any("")
	if primary != nil {
		primaryID = primary.Value("id")
	}
	pids := []any{}
	for _, p := range primaries {
		if id := p.Value("id"); pyjson.Truthy(id) {
			pids = append(pids, id)
		}
	}
	blockedAny := make([]any, len(blocked))
	for i, b := range blocked {
		blockedAny[i] = b
	}
	return pyjson.NewObject("state", state, "ready", ready, "skipped", skipped, "notes", notes,
		"allowed", allowed, "blocked", blockedAny, "requires_human", requires, "denied", deniedAny,
		"primary_repository", primaryID, "primary_repositories", pids)
}

func dedupeBlocked(blocked []*obj) []*obj {
	seen := map[string]bool{}
	out := []*obj{}
	for _, b := range blocked {
		c := b.Value("capability").(string)
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, b)
	}
	return out
}

// ------------------------------------------------------------- rendering

func cell(v any) string {
	s := "—"
	if pyjson.Truthy(v) {
		s = str(v)
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}

// table is _table(rows, header).
func table(rows [][]any, header []string) string {
	if len(rows) == 0 {
		return "_None recorded._"
	}
	lines := []string{"| " + strings.Join(header, " | ") + " |", "|" + strings.Repeat("---|", len(header))}
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = cell(c)
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
	}
	return strings.Join(lines, "\n")
}

func dash(v any) string {
	if pyjson.Truthy(v) {
		return str(v)
	}
	return "—"
}

func rowsOf(items any, keys ...string) [][]any {
	rows := [][]any{}
	for _, x := range listOr(items) {
		m := mapOf(x)
		row := make([]any, len(keys))
		for i, k := range keys {
			row[i] = get(m, k)
		}
		rows = append(rows, row)
	}
	return rows
}

func joinStrs(v any) string {
	parts := []string{}
	for _, x := range listOr(v) {
		parts = append(parts, str(x))
	}
	return strings.Join(parts, ", ")
}

// RenderSummary is render_summary(record).
func RenderSummary(record *obj) string {
	profile := orMap(get(record, "profile"))
	state := orStr(get(record, "state"))
	if state == "" {
		state = "draft"
	}
	ready := Readiness(profile, state)
	dg := get(record, "digest")
	if !pyjson.Truthy(dg) {
		dg = Digest(profile)
	}
	parts := []string{"# ARK Environment Setup", "",
		"- State: **" + str(get(record, "state")) + "**  ·  digest `" + str(dg) + "`",
		"- Updated: " + dash(get(record, "updated_at")) + " by " + dash(get(record, "updated_by"))}
	if conf := mapOf(get(record, "confirmation")); conf.Len() > 0 {
		parts = append(parts, "- Confirmed: "+str(get(conf, "at"))+" by "+str(get(conf, "by")))
	}
	objective := "_Not recorded._"
	if pyjson.Truthy(get(profile, "objective")) {
		objective = str(get(profile, "objective"))
	}
	parts = append(parts, "", "## Objective", "", objective, "")
	if ng := listOr(get(profile, "non_goals")); len(ng) > 0 {
		parts = append(parts, "Non-goals:", "")
		for _, x := range ng {
			parts = append(parts, "- "+str(x))
		}
		parts = append(parts, "")
	}
	parts = append(parts, "## Repositories", "", table(rowsOf(get(profile, "repositories"), "id", "role", "access", "path", "default_branch", "worktree_root"),
		[]string{"id", "role", "access", "path", "base", "worktrees"}), "")
	parts = append(parts, "## Task spaces", "", table(rowsOf(get(profile, "task_spaces"), "id", "kind", "access", "location", "relation"),
		[]string{"id", "kind", "access", "location", "relation"}), "")
	parts = append(parts, "## Compute and environments", "", table(rowsOf(get(profile, "compute"), "id", "kind", "gpu_server", "gpus", "use"),
		[]string{"id", "kind", "gpu server", "gpus", "use"}), "", table(rowsOf(get(profile, "environments"), "id", "kind", "repository", "activate"),
		[]string{"id", "kind", "repository", "activate"}), "")
	parts = append(parts, "## Storage", "", table(rowsOf(get(profile, "storage"), "id", "kind", "location", "retention"),
		[]string{"id", "kind", "location", "retention"}), "")
	cmdRows := [][]any{}
	for _, x := range listOr(get(profile, "commands")) {
		c := mapOf(x)
		mand, src := "no", "confirmed"
		if pyjson.Truthy(get(c, "mandatory")) {
			mand = "yes"
		}
		if pyjson.Truthy(get(c, "proposed")) {
			src = "proposed"
		}
		cmdRows = append(cmdRows, []any{get(c, "id"), get(c, "gate"), get(c, "repository"), "`" + str(get(c, "run")) + "`", mand, src})
	}
	parts = append(parts, "## Gate commands", "", table(cmdRows, []string{"id", "gate", "repository", "run", "mandatory", "source"}), "")
	parts = append(parts, "## Secret references", "", table(rowsOf(get(profile, "secrets"), "id", "ref", "purpose"),
		[]string{"id", "ref", "purpose"}), "")
	policies := orMap(get(profile, "policies"))
	defaulted := map[string]bool{}
	for _, k := range listOr(get(profile, "policies_defaulted")) {
		defaulted[str(k)] = true
	}
	polRows := [][]any{}
	for _, k := range policies.Keys() {
		src := "set"
		if defaulted[k] {
			src = "default"
		}
		polRows = append(polRows, []any{k, policies.Value(k), policyNotes[k], src})
	}
	parts = append(parts, "## Authority", "", table(polRows, []string{"action", "policy", "covers", "source"}), "")
	agents := orMap(get(profile, "agents"))
	cadence := orMap(get(profile, "cadence"))
	providers := joinStrs(get(agents, "providers"))
	if providers == "" {
		providers = "—"
	}
	parts = append(parts, "## Team", "",
		"- Coordinator: "+dash(get(agents, "coordinator")),
		"- Providers: "+providers,
		"- Concurrency: "+str(get(agents, "concurrency")),
		"- Default reviewer: "+dash(get(agents, "default_reviewer")),
		"- Status cadence: every "+str(get(cadence, "status_minutes"))+" min",
		"- Task trigger: "+dash(get(cadence, "task_trigger")),
		"- Task ID prefixes: "+joinStrs(get(orMap(get(profile, "task_ids")), "prefixes"))+" (project-global serial)", "")
	if sk := listOr(get(profile, "skipped")); len(sk) > 0 {
		parts = append(parts, "## Skipped by the user", "")
		for _, s := range sk {
			parts = append(parts, "- `"+str(s)+"`: "+skipEffect[str(s)][1])
		}
		parts = append(parts, "")
	}
	if uk := listOr(get(profile, "unknowns")); len(uk) > 0 {
		parts = append(parts, "## Unknowns", "", table(rowsOf(uk, "field", "blocks", "owner"), []string{"field", "blocks", "owner"}), "")
	}
	allowed := joinStrs(ready.Value("allowed"))
	if allowed == "" {
		allowed = "nothing"
	}
	parts = append(parts, "## Readiness", "", "- Allowed now: "+allowed)
	for _, x := range listOr(ready.Value("blocked")) {
		b := x.(*obj)
		parts = append(parts, "- Blocked `"+str(b.Value("capability"))+"`: "+str(b.Value("reason")))
	}
	if qs := Questions(profile); len(qs) > 0 {
		parts = append(parts, "", "## Open questions", "")
		for i, x := range qs {
			q := x.(*obj)
			parts = append(parts, strconv.Itoa(i+1)+". ["+str(q.Value("stage"))+"] "+str(q.Value("prompt"))+" _(blocks: "+str(q.Value("blocks"))+")_")
		}
	}
	return pytext.RStrip(strings.Join(parts, "\n")) + "\n"
}

// utcNow is protocol.utc_now() (the replay clock when one is set).
func utcNow() string { return clock.UTCNow() }
