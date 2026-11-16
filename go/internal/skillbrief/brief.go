// Package skillbrief ports interface.validate_brief from
// arkstudio/skills/interface.py (`skills brief`, and the agent-team
// "brief"/"delegation" validation kind) with Python's value semantics:
// str() of non-strings, truthiness, int() coercion and list() of strings
// and mappings.
package skillbrief

import (
	"fmt"
	"math"
	"math/big"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/pyurl"
)

type obj = pyjson.Object

// Uncaught is a Python exception the CLI lets escape (exit 1, traceback).
type Uncaught struct{ Class, Message string }

func (e *Uncaught) Error() string { return e.Class + ": " + e.Message }

const InterfaceVersion = "2.0"

var structureOrder = []string{"sequential", "breadth", "long-horizon", "adversarial"}

var allowedTopologies = map[string][]string{
	"sequential":   {"solo", "pipeline"},
	"breadth":      {"pipeline", "router-workers", "peer-team"},
	"long-horizon": {"pipeline", "router-workers"},
	"adversarial":  {"peer-team"},
}

var teamModes = []string{"solo", "pipeline", "router-workers", "peer-team"}

var topologyRange = map[string][2]int64{
	"solo": {1, 1}, "pipeline": {2, 3}, "router-workers": {2, 5}, "peer-team": {3, 5},
}

var FailureClasses = []string{"spec", "tool", "context", "model", "environment", "review", "integration"}

var routingModes = []string{"fixed", "tiered", "auction"}

var verificationMethods = []string{"command", "rubric", "human", "mixed"}

const (
	minTokens     = 8_000
	maxTokens     = 2_000_000
	defaultTokens = 120_000
	warnTokens    = 500_000
	absoluteMax   = 8
	heteroAbove   = 5
)

func in(s string, l []string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// text is _text(value, limit): str(value or "").strip()[:limit].
func text(v any, limit int) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pytext.Prefix(pytext.Strip(pyjson.Str(v)), limit)
}

// list is _list(value, limit, item_limit).
func list(v any, limit, itemLimit int) []any {
	out := []any{}
	if v == nil {
		return out
	}
	items, ok := v.([]any)
	if !ok {
		items = []any{v}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	for _, item := range items {
		if t := text(item, itemLimit); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func list200(v any) []any { return list(v, 200, 1000) }

// pyInt is _int(value, default): bool and non-numbers give the default.
func pyInt(v any, def int64) (*big.Int, error) {
	switch x := v.(type) {
	case bool:
		return big.NewInt(def), nil
	case int64:
		return big.NewInt(x), nil
	case *big.Int:
		return x, nil
	case float64:
		if math.IsNaN(x) {
			return big.NewInt(def), nil
		}
		if math.IsInf(x, 0) {
			return nil, &Uncaught{"OverflowError", "cannot convert float infinity to integer"}
		}
		b, _ := big.NewFloat(math.Trunc(x)).Int(nil)
		return b, nil
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			return big.NewInt(def), nil
		}
		return big.NewInt(n), nil
	}
	return big.NewInt(def), nil
}

func cmp(a *big.Int, b int64) int { return a.Cmp(big.NewInt(b)) }

func intValue(b *big.Int) any {
	if b.IsInt64() {
		return b.Int64()
	}
	return b
}

// pyList is list(x) for a truthy JSON value.
func pyList(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case string:
		out := []any{}
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, nil
	case *obj:
		out := []any{}
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out, nil
	case nil:
		return nil, &Uncaught{"TypeError", "'NoneType' object is not iterable"}
	case bool:
		return nil, &Uncaught{"TypeError", "'bool' object is not iterable"}
	case float64:
		return nil, &Uncaught{"TypeError", "'float' object is not iterable"}
	}
	return nil, &Uncaught{"TypeError", "'int' object is not iterable"}
}

func orDefault(v any, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

func mapping(v any) *obj {
	if o, ok := v.(*obj); ok {
		return o
	}
	return pyjson.NewObject()
}

func relativePath(value string) bool {
	raw := strings.ReplaceAll(pytext.Strip(value), `\`, "/")
	if raw == "" || strings.HasPrefix(raw, "/") || in("..", strings.Split(raw, "/")) {
		return false
	}
	p := pyurl.Parse(raw)
	return p.Scheme == "" && p.Netloc == ""
}

type Diag struct{ Errors, Warnings []any }

func (d *Diag) err(f string, a ...any)  { d.Errors = append(d.Errors, fmt.Sprintf(f, a...)) }
func (d *Diag) warn(f string, a ...any) { d.Warnings = append(d.Warnings, fmt.Sprintf(f, a...)) }

func normalizeTeam(d *Diag, value any, structure, reviewer string) (*obj, error) {
	raw := mapping(value)
	mode := pytext.Lower(text(orDefault(raw.Value("mode"), "solo"), 40))
	if _, ok := topologyRange[mode]; !ok {
		d.err("team.mode must be one of: %s", strings.Join(teamModes, ", "))
		mode = "solo"
	}
	spec := topologyRange[mode]
	size, err := pyInt(raw.Value("size"), 1)
	if err != nil {
		return nil, err
	}
	if cmp(size, 1) < 0 {
		d.err("team.size must be at least 1")
		size = big.NewInt(1)
	}
	if cmp(size, absoluteMax) > 0 {
		d.err("team.size %s exceeds the absolute maximum %d", size, absoluteMax)
	}
	if cmp(size, spec[0]) < 0 || cmp(size, spec[1]) > 0 {
		d.warn("team.size %s is outside the recommended range %d-%d for %s", size, spec[0], spec[1], mode)
	}
	heterogeneous := pyjson.Truthy(raw.Value("heterogeneous"))
	if cmp(size, heteroAbove) > 0 && !heterogeneous {
		d.err("team.size above %d requires heterogeneous=true (homogeneous agents hit a hard ceiling)", heteroAbove)
	}
	if mode == "peer-team" && !heterogeneous {
		d.err("peer-team requires heterogeneous=true (models or roles)")
	}
	rationale := text(raw.Value("rationale"), 600)
	if cmp(size, 1) > 0 && rationale == "" {
		d.err("team.size > 1 requires a rationale explaining why the task structure earns a team")
	}
	if mode == "solo" && cmp(size, 1) != 0 {
		d.err("solo requires team.size = 1")
	}
	if cmp(size, 1) == 0 && mode != "solo" {
		d.err("%s requires more than one agent; use solo instead", mode)
	}
	roles := list(raw.Value("roles"), 12, 80)
	if mode == "peer-team" {
		found := false
		for _, r := range roles {
			if in(pytext.Lower(r.(string)), []string{"reviewer", "critic", "verifier"}) {
				found = true
			}
		}
		if !found {
			d.err("peer-team requires a reviewer, critic or verifier role")
		}
	}
	if cmp(size, 1) > 0 && text(reviewer, 2000) == "" {
		d.warn("team tasks should name an independent reviewer")
	}
	allowed, ok := allowedTopologies[structure]
	if !ok {
		allowed = allowedTopologies["sequential"]
	}
	if !in(mode, allowed) {
		d.warn("topology %s is unusual for a %s task; consider one of: %s", mode, structure, strings.Join(allowed, ", "))
	}
	if cmp(size, 1) < 0 {
		size = big.NewInt(1)
	}
	return pyjson.NewObject("mode", mode, "size", intValue(size), "heterogeneous", heterogeneous,
		"rationale", rationale, "roles", roles), nil
}

func normalizeContext(d *Diag, value any) (*obj, error) {
	raw := mapping(value)
	budget, err := pyInt(raw.Value("budget_tokens"), defaultTokens)
	if err != nil {
		return nil, err
	}
	if cmp(budget, minTokens) < 0 {
		d.err("context.budget_tokens must be at least %d", minTokens)
	}
	if cmp(budget, maxTokens) > 0 {
		d.err("context.budget_tokens must not exceed %d", maxTokens)
	}
	if cmp(budget, warnTokens) > 0 {
		d.warn("a very large context budget is likely to be wasted; spill artefacts instead of inlining them")
	}
	inline := list200(raw.Value("inline"))
	if len(inline) == 0 {
		inline = []any{"task manifest", "current unit", "owned paths"}
	}
	spill := list200(raw.Value("spill"))
	if len(spill) == 0 {
		spill = []any{"full logs", "datasets", "prior reports"}
	}
	artifacts := list200(raw.Value("artifacts"))
	for _, p := range artifacts {
		if !relativePath(p.(string)) {
			d.err("context.artifacts path must be project-relative: %s", p)
		}
	}
	if cmp(budget, minTokens) < 0 {
		budget = big.NewInt(minTokens)
	}
	return pyjson.NewObject("budget_tokens", intValue(budget), "inline", inline, "spill", spill,
		"artifacts", artifacts), nil
}

func normalizeVerification(d *Diag, value any, gates []any, acceptance []*obj, acceptanceException bool) *obj {
	raw := mapping(value)
	independent := true
	if v, ok := raw.Get("independent"); ok {
		independent = pyjson.Truthy(v)
	}
	if !independent && !acceptanceException {
		d.err("verification.independent must be true unless acceptance_exception is declared")
	}
	method := pytext.Lower(text(orDefault(raw.Value("method"), "command"), 40))
	if !in(method, verificationMethods) {
		d.err("verification.method must be one of: %s", strings.Join(verificationMethods, ", "))
		method = "command"
	}
	evidence := list200(raw.Value("evidence"))
	if len(evidence) == 0 {
		for _, item := range acceptance {
			v := orDefault(item.Value("evidence"), orDefault(item.Value("command"), ""))
			if s := pytext.Strip(pyjson.Str(v)); s != "" {
				evidence = append(evidence, s)
			}
		}
	}
	contains := func(s any) bool {
		for _, g := range gates {
			if g == s {
				return true
			}
		}
		return false
	}
	reproduces := []any{}
	for _, g := range list200(raw.Value("reviewer_reproduces")) {
		if contains(g) {
			reproduces = append(reproduces, g)
		}
	}
	if len(reproduces) == 0 {
		if contains("unit") {
			reproduces = []any{"unit"}
		} else if len(gates) > 0 {
			reproduces = []any{gates[0]}
		}
	}
	if method == "human" && len(evidence) == 0 {
		d.warn("human verification without named evidence is weak")
	}
	return pyjson.NewObject("independent", independent, "method", method, "evidence", evidence,
		"reviewer_reproduces", reproduces)
}

func normalizeFailureTaxonomy(d *Diag, value any) []any {
	items := list200(value)
	out := []any{}
	if len(items) == 0 {
		for _, c := range FailureClasses {
			out = append(out, c)
		}
		return out
	}
	unknown := []string{}
	have := map[string]bool{}
	for _, it := range items {
		s := it.(string)
		have[s] = true
		if !in(s, FailureClasses) {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		d.err("failure_taxonomy has unknown classes: %s; allowed: %s", strings.Join(unknown, ", "),
			strings.Join(FailureClasses, ", "))
	}
	for _, c := range FailureClasses {
		if have[c] {
			out = append(out, c)
		}
	}
	return out
}

func normalizeHandoff(d *Diag, value any, objective string, deliverables, dependencies any) (*obj, error) {
	raw := mapping(value)
	contract := text(orDefault(raw.Value("contract"), objective), 2000)
	inputs := list200(raw.Value("inputs"))
	if len(inputs) == 0 {
		l, err := pyList(dependencies)
		if err != nil {
			return nil, err
		}
		inputs = append([]any{}, l...)
	}
	outputs := list200(raw.Value("outputs"))
	if len(outputs) == 0 {
		l, err := pyList(deliverables)
		if err != nil {
			return nil, err
		}
		outputs = append([]any{}, l...)
	}
	if contract == "" {
		d.err("handoff.contract is required (state what the next agent receives)")
	}
	if len(outputs) == 0 {
		d.warn("handoff.outputs is empty; the next agent cannot verify delivery")
	}
	return pyjson.NewObject("contract", contract, "inputs", inputs, "outputs", outputs), nil
}

// ValidateBrief is interface.validate_brief(payload).
func ValidateBrief(data *obj) (*obj, error) {
	if data == nil {
		data = pyjson.NewObject()
	}
	d := &Diag{Errors: []any{}, Warnings: []any{}}
	objective := text(orDefault(data.Value("objective"), data.Value("goal")), 4000)
	if objective == "" {
		d.err("objective is required")
	}
	structure := pytext.Lower(text(orDefault(data.Value("task_structure"), "sequential"), 40))
	if !in(structure, structureOrder) {
		d.err("task_structure must be one of: %s", strings.Join(structureOrder, ", "))
		structure = "sequential"
	}
	teamValue := data.Value("team")
	if !pyjson.Truthy(teamValue) {
		teamValue = pyjson.NewObject("mode", orDefault(data.Value("topology"), "solo"),
			"size", orDefault(data.Value("team_size"), int64(1)),
			"heterogeneous", data.Value("heterogeneous"), "rationale", data.Value("team_rationale"))
	}
	team, err := normalizeTeam(d, teamValue, structure, text(data.Value("reviewer"), 2000))
	if err != nil {
		return nil, err
	}
	plan, err := normalizeContext(d, data.Value("context"))
	if err != nil {
		return nil, err
	}
	owned := list200(data.Value("owned_paths"))
	if len(owned) == 0 {
		d.err("owned_paths must declare at least one file or glob")
	}
	acceptance := data.Value("acceptance")
	items, isList := acceptance.([]any)
	if !isList {
		items = []any{}
		if pyjson.Truthy(acceptance) {
			items = []any{acceptance}
		}
	}
	if len(items) == 0 {
		d.err("acceptance must include at least one command or check")
	}
	stops := list200(data.Value("stop_conditions"))
	if len(stops) == 0 {
		d.err("stop_conditions must say when the worker stops and escalates")
	}
	gates, err := pyList(orDefault(data.Value("gates"), []any{"scope", "unit"}))
	if err != nil {
		return nil, err
	}
	accObjs := make([]*obj, 0, len(items))
	for _, it := range items {
		if o, ok := it.(*obj); ok {
			accObjs = append(accObjs, o)
		} else {
			accObjs = append(accObjs, pyjson.NewObject("command", it))
		}
	}
	verification := normalizeVerification(d, data.Value("verification"), gates, accObjs, false)
	taxonomy := normalizeFailureTaxonomy(d, data.Value("failure_taxonomy"))
	handoff, err := normalizeHandoff(d, data.Value("handoff"), objective,
		orDefault(data.Value("deliverables"), []any{}), orDefault(data.Value("dependencies"), []any{}))
	if err != nil {
		return nil, err
	}
	brief := pyjson.NewObject(
		"format", "ark-agent-brief",
		"interface_version", InterfaceVersion,
		"objective", objective,
		"task_structure", structure,
		"team", team,
		"context", plan,
		"owned_paths", owned,
		"acceptance", items,
		"stop_conditions", stops,
		"verification", verification,
		"failure_taxonomy", taxonomy,
		"handoff", handoff,
	)
	return pyjson.NewObject("ok", len(d.Errors) == 0, "errors", d.Errors, "warnings", d.Warnings, "brief", brief), nil
}
