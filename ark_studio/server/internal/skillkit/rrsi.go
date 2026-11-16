package skillkit

import (
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyre"
	"arkstudio/go/internal/compat/pytext"
)

// rrsi.py

var Components = []string{"prompt", "control_flow", "config", "output_plumbing", "context_mgmt",
	"client_tool", "skill", "memory", "subagent"}

var StructuralComponents = []string{"client_tool", "skill", "memory", "subagent"}

type signal struct {
	component string
	patterns  []*pyre.Regexp
}

func mustAll(patterns ...string) []*pyre.Regexp {
	out := make([]*pyre.Regexp, len(patterns))
	for i, p := range patterns {
		out[i] = pyre.MustCompile(p, 0)
	}
	return out
}

var genericSignals = []signal{
	{"memory", mustAll(`\bMemory\(`, `\.remember\(`, `\.recall\(`, `_STATE_DIR`)},
	{"skill", mustAll(`skills/`, `SkillRegistry`, `skill_use`, `skill_catalog`, `upload_skills`, `SKILL\.md`)},
	{"client_tool", mustAll(`ToolRegistry`, `register_tool`, `tool_spec`, `CLIENT_TOOLS`)},
	{"subagent", mustAll(`\bsubcall\(`, `sub_agent`, `subagent`)},
}

var DefaultLeakagePatterns = []string{
	`\btests?/`,
	`\btest_[a-z0-9_]+\.py\b`,
	`\bexpected_(?:output|result|answer)\b`,
	`\bassert(?:Equal|In|True)\b`,
	`\bhidden[_ ]tests?\b`,
	`\bbenchmark[_ ]task\b`,
	`\bT-\d{3,}\b`,
}

var (
	stringLineRe = pyre.MustCompile(`^[+-]\s*(?:[frb]?["\']|""")`, 0)
	componentRe  = pyre.MustCompile(`^[a-z][a-z0-9_]*$`, 0)
	rrsiDriveRe  = pyre.MustCompile(`^[A-Za-z]:`, 0)
)

const searchBudget = 50_000_000

// search is re.search(pattern, text) (ok only).
func search(re *pyre.Regexp, s string) (pyre.Match, bool) {
	b := searchBudget
	m, ok, _ := re.SearchFrom([]rune(s), 0, -1, &b)
	return m, ok
}

func reMatch(re *pyre.Regexp, s string) bool {
	m, ok := search(re, s)
	return ok && m.Start == 0
}

// rrsiNumber is rrsi._number(value, name).
func rrsiNumber(v any, name string) (float64, error) {
	if _, ok := v.(bool); ok {
		return 0, errf(name + " must be a number")
	}
	f, ok, err := pyFloat(v)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, errf(name + " must be a number")
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, errf(name + " must be finite")
	}
	return f, nil
}

// TextOnly is text_only(diff).
func TextOnly(diff string) bool {
	changed := []string{}
	for _, line := range pytext.SplitLines(diff, false) {
		if (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) &&
			!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") &&
			pytext.Strip(pytext.SliceFrom(line, 1)) != "" {
			changed = append(changed, line)
		}
	}
	if len(changed) == 0 {
		return false
	}
	for _, line := range changed {
		if !reMatch(stringLineRe, line) && !strings.HasPrefix(pytext.LStrip(pytext.SliceFrom(line, 1)), "#") {
			return false
		}
	}
	return true
}

func anySearch(patterns []*pyre.Regexp, diff string) bool {
	for _, p := range patterns {
		if _, ok := search(p, diff); ok {
			return true
		}
	}
	return false
}

func classifyDiff(diff string) string {
	if TextOnly(diff) {
		return "prompt"
	}
	for _, s := range genericSignals {
		if anySearch(s.patterns, diff) {
			return s.component
		}
	}
	return "prompt"
}

func hasEvidence(component, diff string) bool {
	for _, s := range genericSignals {
		if s.component == component && anySearch(s.patterns, diff) {
			return true
		}
	}
	return false
}

func normalizeComponent(declared, diff string) string {
	value := pytext.Lower(pytext.Strip(declared))
	if inList(value, Components) && hasEvidence(value, diff) {
		return value
	}
	return classifyDiff(diff)
}

// searchConfig keeps SearchConfig's fields as the caller passed them
// (the dataclass does not coerce types).
type searchConfig struct {
	vals map[string]any
}

var configFields = []string{"delta", "beta0", "beta1", "w_s", "w_c", "w_n", "b_min", "b_max", "T", "stall_window", "recent_window"}

var configDefaults = map[string]any{"delta": 0.017, "beta0": 0.0, "beta1": 0.5, "w_s": 1.0, "w_c": 0.25,
	"w_n": 0.1, "b_min": int64(1), "b_max": int64(3), "T": int64(10), "stall_window": int64(3), "recent_window": int64(3)}

func (c *searchConfig) v(k string) any { return c.vals[k] }

// f is a numeric field as a float (validated numbers only).
func (c *searchConfig) f(k string) float64 {
	n, _ := asNum(c.vals[k])
	return n.float()
}

func configFromMapping(value any) (*searchConfig, error) {
	m := pyjson.NewObject()
	if pyjson.Truthy(value) {
		var err error
		if m, err = toDict(value); err != nil {
			return nil, err
		}
	}
	unknown := []string{}
	for _, k := range m.Keys() {
		if !inList(k, configFields) {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, errf("unknown RRSI config keys: " + strings.Join(unknown, ", "))
	}
	c := &searchConfig{vals: map[string]any{}}
	for _, k := range configFields {
		c.vals[k] = configDefaults[k]
		if v, ok := m.Get(k); ok {
			c.vals[k] = v
		}
	}
	return c, c.validate()
}

func (c *searchConfig) less0(k string) (bool, error) { return lessThanZero(c.v(k), "<") }

func (c *searchConfig) validate() error {
	if neg, err := c.less0("delta"); err != nil || neg {
		if err != nil {
			return err
		}
		return errf("delta must be >= 0")
	}
	for _, group := range [][]string{{"beta0", "beta1"}, {"w_s", "w_c", "w_n"}} {
		for _, k := range group {
			neg, err := c.less0(k)
			if err != nil {
				return err
			}
			if neg {
				if group[0] == "beta0" {
					return errf("beta0 and beta1 must be >= 0")
				}
				return errf("weights must be >= 0")
			}
		}
	}
	bmin, err := pyInt(c.v("b_min"))
	if err != nil {
		return err
	}
	if bmin.Cmp(big.NewInt(1)) < 0 {
		return errf("budget requires 1 <= b_min <= b_max")
	}
	bmax, err := pyInt(c.v("b_max"))
	if err != nil {
		return err
	}
	if bmin.Cmp(bmax) > 0 {
		return errf("budget requires 1 <= b_min <= b_max")
	}
	for _, k := range []string{"T", "stall_window", "recent_window"} {
		n, err := pyInt(c.v(k))
		if err != nil {
			return err
		}
		limit := int64(1)
		if k == "T" {
			limit = 0
		}
		if n.Cmp(big.NewInt(limit)) < 0 {
			return errf("T >= 0 and windows >= 1 are required")
		}
	}
	return nil
}

// editBudget is edit_budget(t, T, b_min, b_max); T is passed as given.
func editBudget(t *big.Int, T any, bMin, bMax any) (int64, error) {
	lo, err := pyInt(bMin)
	if err != nil {
		return 0, err
	}
	hi, err := pyInt(bMax)
	if err != nil {
		return 0, err
	}
	if lo.Cmp(big.NewInt(1)) < 0 || hi.Cmp(lo) < 0 {
		return 0, errf("budget requires 1 <= b_min <= b_max")
	}
	tn, ok := asNum(T)
	if !ok {
		return 0, uncaught("TypeError", "'<=' not supported between instances of '"+pyerr.TypeName(T)+"' and 'int'")
	}
	if tn.cmpInt(0) <= 0 && tn.cmpInt(0) != 2 {
		return hi.Int64(), nil
	}
	ti, err := pyInt(T)
	if err != nil {
		return 0, err
	}
	tt := t
	if tt.Cmp(ti) > 0 {
		tt = ti
	}
	if tt.Sign() < 0 {
		tt = new(big.Int)
	}
	tf, _ := new(big.Float).SetInt(tt).Float64()
	lf, _ := new(big.Float).SetInt(lo).Float64()
	diff := new(big.Int).Sub(hi, lo)
	df, _ := new(big.Float).SetInt(diff).Float64()
	value := lf + df*0.5*(1.0+math.Cos(math.Pi*tf/tn.float()))
	rounded, _ := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 9, 64), 64)
	return int64(math.Ceil(rounded)), nil
}

func budgetTable(c *searchConfig) ([]any, error) {
	ti, err := pyInt(c.v("T"))
	if err != nil {
		return nil, err
	}
	out := []any{}
	if ti.Sign() <= 0 {
		return out, nil
	}
	for t := int64(0); t < ti.Int64(); t++ {
		b, err := editBudget(big.NewInt(t), c.v("T"), c.v("b_min"), c.v("b_max"))
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func relativeCostChange(newCost, incumbentCost float64) float64 {
	if incumbentCost <= 0 {
		if newCost <= incumbentCost {
			return 0.0
		}
		return 1.0
	}
	return (newCost - incumbentCost) / incumbentCost
}

type candidate struct {
	variant     string
	edits       []*obj
	score, cost any // None or a number (floats after validate)
	gateFailure any
	commit      any
}

func (c *candidate) components() []string {
	out := []string{}
	for _, e := range c.edits {
		if pyjson.Truthy(e.Value("component")) {
			out = append(out, str(e.Value("component")))
		}
	}
	return out
}

func (c *candidate) validate() error {
	if pytext.Strip(c.variant) == "" {
		return errf("candidate variant is required")
	}
	if len(c.edits) == 0 {
		return errf("candidate " + c.variant + ": at least one edit is required")
	}
	for _, e := range c.edits {
		component := pytext.Lower(pytext.Strip(str(or(e.Value("component"), ""))))
		if !reMatch(componentRe, component) || !inList(component, Components) {
			return errf("candidate " + c.variant + ": unknown component " + pyjson.Repr(component))
		}
		if pytext.Strip(str(or(e.Value("hypothesis"), ""))) == "" {
			return errf("candidate " + c.variant + ": every edit needs a hypothesis")
		}
	}
	if c.score != nil {
		f, err := rrsiNumber(c.score, "score")
		if err != nil {
			return err
		}
		c.score = f
	}
	if c.cost != nil {
		f, err := rrsiNumber(c.cost, "cost")
		if err != nil {
			return err
		}
		c.cost = f
	}
	return nil
}

func novelty(components []string, accepted *obj) (int64, error) {
	seen := map[string]bool{}
	var n int64
	for _, comp := range components {
		if seen[comp] {
			continue
		}
		seen[comp] = true
		if !inList(comp, StructuralComponents) {
			continue
		}
		v := or(accepted.Value(comp), int64(0))
		i, err := pyInt(v)
		if err != nil {
			return 0, err
		}
		if i.Sign() == 0 {
			n++
		}
	}
	return n, nil
}

func fmtNum(v any) string { return str(v) }

func costRule(dS, dC float64, nov int64, delta float64, c *searchConfig) (bool, string) {
	if dS > delta {
		budget := c.f("beta0") + c.f("beta1")*dS
		ok := dC <= budget
		op := ">"
		if ok {
			op = "<="
		}
		return ok, "gain " + fmtFixed(dS, 4, true) + " > delta " + fmtFixed(delta, 4, false) +
			"; cost change " + fmtFixed(dC, 3, true) + " " + op + " budget " + fmtFixed(budget, 3, false) +
			" (beta0 " + fmtNum(c.v("beta0")) + " + beta1 " + fmtNum(c.v("beta1")) + " * dS)"
	}
	shaped := c.f("w_s")*dS - c.f("w_c")*dC + c.f("w_n")*float64(nov)
	ok := shaped > 0
	op := "<="
	if ok {
		op = ">"
	}
	return ok, "gain " + fmtFixed(dS, 4, true) + " within delta " + fmtFixed(delta, 4, false) +
		"; shaped " + fmtNum(c.v("w_s")) + "*dS - " + fmtNum(c.v("w_c")) + "*dC + " + fmtNum(c.v("w_n")) +
		"*nu = " + fmtFixed(shaped, 4, true) + " " + op + " 0 (nu=" + strconv.FormatInt(nov, 10) + ")"
}

type decision struct {
	variant    string
	admissible bool
	reason     any
	score      any
	cost       any
	dS, dC     any
	novelty    int64
	guards     []any
}

func (d *decision) json() *obj {
	g := d.guards
	if g == nil {
		g = []any{}
	}
	return pyjson.NewObject("variant", d.variant, "admissible", d.admissible, "reason", d.reason,
		"score", d.score, "cost", d.cost, "delta_score", d.dS, "delta_cost", d.dC,
		"novelty", d.novelty, "guards", append([]any{}, g...))
}

func judge(c *candidate, incScore, incCost, sStar any, accepted *obj, cfg *searchConfig, guards any) (*decision, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	if c.score == nil {
		return &decision{variant: c.variant, reason: or(c.gateFailure, "not evaluated")}, nil
	}
	is, err := rrsiNumber(incScore, "incumbent_score")
	if err != nil {
		return nil, err
	}
	ic, err := rrsiNumber(incCost, "incumbent_cost")
	if err != nil {
		return nil, err
	}
	ss, err := rrsiNumber(sStar, "s_star")
	if err != nil {
		return nil, err
	}
	score := c.score.(float64)
	dS := score - is
	newCost := 0.0
	if f, ok := c.cost.(float64); ok && f != 0 {
		newCost = f
	}
	dC := relativeCostChange(newCost, ic)
	nov, err := novelty(c.components(), accepted)
	if err != nil {
		return nil, err
	}
	gl := []any{}
	if pyjson.Truthy(guards) {
		if gl, err = iterate(guards); err != nil {
			return nil, err
		}
	}
	d := &decision{variant: c.variant, reason: "", score: c.score, cost: c.cost, dS: dS, dC: dC, novelty: nov, guards: gl}
	delta := cfg.f("delta")
	floor := ss - delta
	if score < floor {
		d.reason = "below noise-adjusted floor: S' " + fmtFixed(score, 4, false) + " < S* " + fmtFixed(ss, 4, false) +
			" - delta " + fmtFixed(delta, 4, false)
		return d, nil
	}
	ok, why := costRule(dS, dC, nov, delta, cfg)
	if !ok {
		d.reason = "cost rule failed: " + why
		return d, nil
	}
	if len(gl) > 0 {
		j, err := joinStrings("; ", gl)
		if err != nil {
			return nil, err
		}
		d.reason = "domain guard violated: " + j
		return d, nil
	}
	d.admissible = true
	d.reason = "admissible: " + why
	return d, nil
}

// getAttr is value.get(key, def) where value must be a dict (AttributeError otherwise).
func getAttr(v any, key string, def any) (any, error) {
	o, ok := v.(*obj)
	if !ok {
		return nil, uncaught("AttributeError", "'"+pyerr.TypeName(v)+"' object has no attribute 'get'")
	}
	return get(o, key, def), nil
}

// ValidateRound is rrsi.validate_round(payload).
func ValidateRound(payload *obj) (*obj, error) {
	payload = copyObj(payload)
	cfg, err := configFromMapping(payload.Value("config"))
	if err != nil {
		return nil, err
	}
	incumbent := or(payload.Value("incumbent"), pyjson.NewObject())
	rawScore, err := getAttr(incumbent, "score", nil)
	if err != nil {
		return nil, err
	}
	incScore, err := rrsiNumber(rawScore, "incumbent.score")
	if err != nil {
		return nil, err
	}
	rawCost, _ := getAttr(incumbent, "cost", int64(0))
	incCost, err := rrsiNumber(rawCost, "incumbent.cost")
	if err != nil {
		return nil, err
	}
	sStar, err := rrsiNumber(get(payload, "s_star", incScore), "s_star")
	if err != nil {
		return nil, err
	}
	accepted, ok := or(payload.Value("accepted_counts"), pyjson.NewObject()).(*obj)
	if !ok {
		return nil, errf("accepted_counts must be an object")
	}
	candidates := []*candidate{}
	for _, raw := range asList(payload.Value("candidates")) {
		r, ok := raw.(*obj)
		if !ok {
			return nil, errf("candidates must be objects")
		}
		edits := []*obj{}
		for _, e := range asList(r.Value("edits")) {
			d, err := toDict(e)
			if err != nil {
				return nil, err
			}
			edits = append(edits, d)
		}
		candidates = append(candidates, &candidate{
			variant: str(or(r.Value("variant"), "")), edits: edits,
			score: r.Value("score"), cost: get(r, "cost", int64(0)),
			gateFailure: r.Value("gate_failure"), commit: r.Value("commit"),
		})
	}
	for _, c := range candidates {
		if err := c.validate(); err != nil {
			return nil, err
		}
		round, err := pyInt(or(payload.Value("round"), int64(0)))
		if err != nil {
			return nil, err
		}
		b, err := editBudget(round, cfg.v("T"), cfg.v("b_min"), cfg.v("b_max"))
		if err != nil {
			return nil, err
		}
		if int64(len(c.edits)) > b {
			return nil, errf("candidate " + c.variant + " exceeds the edit budget b_t=" + strconv.FormatInt(b, 10))
		}
	}
	var guardMap *obj
	if g, ok := payload.Value("guards").(*obj); ok {
		guardMap = g
	} else {
		guardMap = pyjson.NewObject()
	}
	decisions := []any{}
	var winner *candidate
	var best float64
	for _, c := range candidates {
		d, err := judge(c, incScore, incCost, sStar, accepted, cfg, guardMap.Value(c.variant))
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, d.json())
		if d.admissible {
			s := d.score.(float64)
			if winner == nil || s > best {
				winner, best = c, s
			}
		}
	}
	var wVariant, wCommit any
	if winner != nil {
		wVariant, wCommit = winner.variant, winner.commit
	}
	return pyjson.NewObject("winner", wVariant, "commit", wCommit, "decisions", decisions,
		"config", pyjson.NewObject("delta", cfg.v("delta"), "beta0", cfg.v("beta0"), "beta1", cfg.v("beta1"),
			"w_s", cfg.v("w_s"), "w_c", cfg.v("w_c"), "w_n", cfg.v("w_n"))), nil
}

// editRecord is History.add + EditRecord.to_json.
func editRecord(record any) (*obj, error) {
	item, err := toDict(record)
	if err != nil {
		return nil, err
	}
	round := get(item, "round", int64(0))
	component := str(or(item.Value("component"), ""))
	hypothesis := str(or(item.Value("hypothesis"), ""))
	verdict := str(or(item.Value("verdict"), ""))
	dS := get(item, "delta_score", 0.0)
	dC := get(item, "delta_cost", 0.0)
	validate := func() (*big.Int, error) {
		r, err := pyInt(round)
		if err != nil {
			return nil, err
		}
		if r.Sign() < 0 {
			return nil, errf("round must be >= 0")
		}
		if !inList(component, Components) {
			return nil, errf("unknown component: " + pyjson.Repr(component))
		}
		if pytext.Strip(hypothesis) == "" {
			return nil, errf("hypothesis is required")
		}
		if !inList(verdict, []string{"accepted", "rejected", "neutral", "invalid"}) {
			return nil, errf("verdict must be accepted, rejected, neutral or invalid")
		}
		if _, err := rrsiNumber(dS, "delta_score"); err != nil {
			return nil, err
		}
		if _, err := rrsiNumber(dC, "delta_cost"); err != nil {
			return nil, err
		}
		return r, nil
	}
	if _, err := validate(); err != nil {
		return nil, err
	}
	r, err := validate()
	if err != nil {
		return nil, err
	}
	fs, _ := rrsiNumber(dS, "")
	fc, _ := rrsiNumber(dC, "")
	var roundOut any = r
	if r.IsInt64() {
		roundOut = r.Int64()
	}
	return pyjson.NewObject("round", roundOut, "component", component, "hypothesis", hypothesis,
		"verdict", verdict, "delta_score", fs, "delta_cost", fc,
		"variant", str(or(item.Value("variant"), "")), "commit", str(or(item.Value("commit"), "")),
		"evidence", str(or(item.Value("evidence"), ""))), nil
}

func lastN(records []*obj, n int64) []*obj {
	if n <= 0 || n >= int64(len(records)) {
		if n <= 0 {
			// records[-0:] is the whole list
			return records
		}
		return records
	}
	return records[int64(len(records))-n:]
}

func recDelta(r *obj) float64 {
	f, _ := r.Value("delta_score").(float64)
	return f
}

// HistorySnapshot is rrsi.history_snapshot(payload).
func HistorySnapshot(payload *obj) (*obj, error) {
	payload = copyObj(payload)
	records := []*obj{}
	raw := payload.Value("records")
	if pyjson.Truthy(raw) {
		items, err := iterate(raw)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			rec, err := editRecord(it)
			if err != nil {
				return nil, err
			}
			records = append(records, rec)
		}
	}
	cfg, err := configFromMapping(payload.Value("config"))
	if err != nil {
		return nil, err
	}
	accepted := pyjson.NewObject()
	for _, c := range Components {
		accepted.Set(c, int64(0))
	}
	tried := map[string]bool{}
	for _, r := range records {
		comp := r.Value("component").(string)
		if r.Value("verdict") == "accepted" {
			n, _ := accepted.Value(comp).(int64)
			accepted.Set(comp, n+1)
		}
		if r.Value("verdict") != "invalid" {
			tried[comp] = true
		}
	}
	triedList := []string{}
	for c := range tried {
		triedList = append(triedList, c)
	}
	sort.Strings(triedList)
	untried := []any{}
	for _, c := range Components {
		if !tried[c] {
			untried = append(untried, c)
		}
	}
	recent := lastN(records, 3)
	exercised := map[string]bool{}
	for _, r := range recent {
		if r.Value("verdict") != "invalid" {
			exercised[r.Value("component").(string)] = true
		}
	}
	prune := []string{}
	for c := range exercised {
		yield := 0
		for _, r := range recent {
			if r.Value("component") == c && r.Value("verdict") == "accepted" && recDelta(r) > 0 {
				yield++
			}
		}
		if yield == 0 {
			prune = append(prune, c)
		}
	}
	sort.Strings(prune)

	window := int64(3)
	if pyjson.Truthy(cfg.v("stall_window")) {
		w, err := pyInt(cfg.v("stall_window"))
		if err != nil {
			return nil, err
		}
		window = w.Int64()
	}
	delta := cfg.f("delta")
	stalled := false
	if int64(len(records)) >= window {
		stalled = true
		for _, r := range lastN(records, window) {
			if r.Value("verdict") == "accepted" && recDelta(r) > delta {
				stalled = false
			}
		}
	}
	budget, err := budgetTable(cfg)
	if err != nil {
		return nil, err
	}
	var reserved any
	if stalled && len(untried) > 0 {
		reserved = untried[0]
	}
	recAny := make([]any, len(records))
	for i, r := range records {
		recAny[i] = r
	}
	return pyjson.NewObject(
		"records", recAny,
		"accepted_counts", accepted,
		"tried_components", strList(triedList),
		"untried_components", untried,
		"prune_set", strList(prune),
		"stall_flag", stalled,
		"exploration", pyjson.NewObject("stalled", stalled, "untried_components", append([]any{}, untried...), "reserved_component", reserved),
		"budget", budget,
	), nil
}

// ValidateEdit is rrsi.validate_edit(payload).
func ValidateEdit(payload *obj) *obj {
	diff := str(or(payload.Value("diff"), ""))
	declared := str(or(payload.Value("component"), ""))
	component := normalizeComponent(declared, diff)
	evidence := false
	if diff != "" {
		evidence = hasEvidence(component, diff)
	}
	return pyjson.NewObject("component", component, "declared_component", declared,
		"component_recovered", declared != "" && declared != component,
		"hypothesis", pytext.Strip(str(or(payload.Value("hypothesis"), ""))),
		"diff_supplied", diff != "", "evidence", evidence)
}

// ScanLeakage is rrsi.scan_leakage(text, patterns); patterns nil = defaults.
func ScanLeakage(text string, patterns []any) ([]any, error) {
	if patterns == nil {
		patterns = strList(DefaultLeakagePatterns)
	}
	findings := []any{}
	lines := pytext.SplitLines(text, false)
	for _, p := range patterns {
		ps, ok := p.(string)
		if !ok {
			return nil, uncaught("TypeError", "first argument must be string or compiled pattern")
		}
		re, err := pyre.Compile(ps, 0)
		if err != nil {
			return nil, errf("invalid leakage pattern " + pyjson.Repr(ps) + ": " + err.Error())
		}
		for i, line := range lines {
			m, ok := search(re, line)
			if !ok {
				continue
			}
			rs := []rune(line)
			findings = append(findings, pyjson.NewObject("pattern", ps, "line", int64(i+1),
				"match", string(rs[m.Start:m.End]), "excerpt", pytext.Prefix(pytext.Strip(line), 180)))
		}
	}
	return findings, nil
}

func stripAll(items []any) []string {
	out := make([]string, len(items))
	for i, v := range items {
		out[i] = pytext.Strip(str(v))
	}
	return out
}

func strItems(v any) []any {
	out := []any{}
	for _, x := range asList(v) {
		out = append(out, str(x))
	}
	return out
}

// ValidateDomain is rrsi.validate_domain(payload).
func ValidateDomain(payload *obj) (*obj, error) {
	signals := or(payload.Value("component_signals"), pyjson.NewObject())
	sig, ok := signals.(*obj)
	if !ok {
		return nil, errf("component_signals must be an object")
	}
	name := pytext.Strip(str(or(or(payload.Value("name"), ""), "")))
	path := strings.ReplaceAll(pytext.Strip(str(or(or(payload.Value("harness_path"), ""), ""))), `\`, "/")
	evolve, heldout, smoke := strItems(payload.Value("evolve")), strItems(payload.Value("heldout")), strItems(payload.Value("smoke"))
	metric := or(payload.Value("metric"), "task success rate")
	guards := strItems(payload.Value("guards"))
	signalsOut := pyjson.NewObject()
	for _, k := range sig.Keys() {
		signalsOut.Set(k, strItems(sig.Value(k)))
	}
	if name == "" {
		return nil, errf("domain name is required")
	}
	if path == "" {
		return nil, errf("harness_path is required")
	}
	if strings.HasPrefix(path, "/") || reMatch(rrsiDriveRe, path) || hasDotDot(path) {
		return nil, errf("harness_path must be repository-relative")
	}
	splits := map[string][]string{}
	for _, sp := range []struct {
		name  string
		items []any
	}{{"evolve", evolve}, {"heldout", heldout}, {"smoke", smoke}} {
		values := stripAll(sp.items)
		seen := map[string]bool{}
		for _, v := range values {
			if seen[v] {
				return nil, errf(sp.name + " contains duplicate task IDs")
			}
			seen[v] = true
		}
		splits[sp.name] = values
	}
	if len(splits["evolve"]) == 0 {
		return nil, errf("evolve must contain at least one task ID")
	}
	held := map[string]bool{}
	for _, v := range splits["heldout"] {
		held[v] = true
	}
	overlapSet := map[string]bool{}
	for _, v := range append(append([]string{}, splits["evolve"]...), splits["smoke"]...) {
		if held[v] {
			overlapSet[v] = true
		}
	}
	if len(overlapSet) > 0 {
		overlap := []string{}
		for v := range overlapSet {
			overlap = append(overlap, v)
		}
		sort.Strings(overlap)
		return nil, errf("heldout must not overlap evolve or smoke: " + strings.Join(overlap, ", "))
	}
	metricText := pytext.Strip(str(or(metric, "")))
	if metricText == "" {
		metricText = "task success rate"
	}
	for _, k := range sig.Keys() {
		if !inList(k, Components) {
			return nil, errf("unknown component signal: " + pyjson.Repr(k))
		}
	}
	return pyjson.NewObject("ok", true, "domain", pyjson.NewObject(
		"name", name, "harness_path", path,
		"evolve", strList(splits["evolve"]), "heldout", strList(splits["heldout"]), "smoke", strList(splits["smoke"]),
		"metric", metricText, "guards", guards, "component_signals", signalsOut)), nil
}

// ExampleDomain is rrsi.example_domain().
func ExampleDomain() *obj {
	return pyjson.NewObject(
		"name", "ark-coding",
		"harness_path", "arkstudio/skills",
		"evolve", []any{"T-0001", "T-0002", "T-0003"},
		"heldout", []any{"T-0004", "T-0005"},
		"smoke", []any{"T-0001"},
		"metric", "accepted task rate",
		"guards", []any{"unit tests must pass", "no new runtime dependency"},
		"component_signals", pyjson.NewObject(
			"skill", []any{"arkstudio/skills/", `SKILL\.md`},
			"client_tool", []any{"ToolRegistry", "register_tool"}),
	)
}

// BuildBriefs is rrsi.build_briefs(context).
func BuildBriefs(context *obj) (*obj, error) {
	defaults := pyjson.NewObject(
		"task_title", "Improve the ARK coding-agent harness",
		"incumbent_score", "0.0000", "incumbent_cost", "0", "s_star", "0.0000", "delta", "0.017",
		"round_index", "0", "budget", "1",
		"components", strings.Join(Components, ", "),
		"structural_components", strings.Join(StructuralComponents, ", "),
		"prune_set", "_none_", "untried_components", strings.Join(Components, ", "),
		"reserved_component", "_none_", "accepted_counts", "{}",
		"round_evidence", "_Round evidence has not been attached yet._", "diff", "",
	)
	for _, k := range context.Keys() {
		if v := context.Value(k); v != nil {
			defaults.Set(k, v)
		}
	}
	out := pyjson.NewObject()
	for _, role := range []string{"proposer", "analyst", "critic"} {
		t, err := substitute(assets["rrsi/"+role+".md"], defaults)
		if err != nil {
			return nil, err
		}
		out.Set(role, t)
	}
	return out, nil
}
