// Package assistant ports the write and prompt half of arkstudio/assistant.py
// (insights live in the store package): apply, extract_ideas and
// dispatch_prompt.
package assistant

import (
	"context"
	"errors"
	"math"
	"math/big"
	"strings"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/store"
)

func attrGet(v any) error {
	return &store.InternalError{Message: "'" + store.PyTypeName(v) + "' object has no attribute 'get'"}
}

// iterate is Python's `for item in value` for a decoded JSON value.
func iterate(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case string:
		out := []any{}
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, nil
	case *pyjson.Object:
		out := []any{}
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out, nil
	}
	return nil, &store.InternalError{Message: "'" + store.PyTypeName(v) + "' object is not iterable"}
}

func orValue(v any, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

// Apply is assistant.apply(store, suggestion, actor).
func Apply(ctx context.Context, s *store.Store, suggestion any, actor string) (*pyjson.Object, error) {
	obj, ok := suggestion.(*pyjson.Object)
	if !ok {
		return nil, attrGet(suggestion)
	}
	ops, err := iterate(orValue(obj.Value("ops"), []any{}))
	if err != nil {
		return nil, err
	}
	applied := []any{}
	for _, op := range ops {
		o, ok := op.(*pyjson.Object)
		if !ok {
			return nil, attrGet(op)
		}
		if kind, isStr := o.Value("op").(string); !isStr || kind != "link" {
			continue
		}
		result, err := s.CreateLink(ctx, o.Value("source"), o.Value("target"),
			orValue(o.Value("relation"), "relates_to"), "Applied from assistant insight", actor)
		if err != nil {
			return nil, err
		}
		applied = append(applied, result)
	}
	detail := pyjson.NewObject("suggestion", obj.Value("id"), "kind", obj.Value("kind"), "ops", int64(len(applied)))
	if err := s.LogActivity(ctx, actor, "assistant.apply", nil, detail); err != nil {
		return nil, err
	}
	return pyjson.NewObject("applied", applied), nil
}

var (
	bulletRe  = pytext.MustCompile(`^(?:[-*•]|\d+[.)])\s+`)
	headingRe = pytext.MustCompile(`^#{1,4}\s+`)
)

// pyIntArg is int(value) for a truthy decoded JSON value.
func pyIntArg(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case *big.Int:
		if x.Sign() > 0 {
			return math.MaxInt64, nil
		}
		return math.MinInt64, nil
	case float64:
		if math.IsInf(x, 0) {
			return 0, &store.InternalError{Message: "cannot convert float infinity to integer"}
		}
		if math.IsNaN(x) {
			return 0, &store.ValueError{Message: "cannot convert float NaN to integer"}
		}
		t := math.Trunc(x)
		if t > math.MaxInt64/2 {
			return math.MaxInt64, nil
		}
		if t < math.MinInt64/2 {
			return math.MinInt64, nil
		}
		return int64(t), nil
	case string:
		n, err := pytext.Int(x)
		if err != nil {
			var ie *pytext.IntError
			if errors.As(err, &ie) {
				return 0, &store.ValueError{Message: err.Error()}
			}
			if strings.HasPrefix(pytext.Strip(x), "-") {
				return math.MinInt64, nil
			}
			return math.MaxInt64, nil
		}
		return n, nil
	}
	return 0, &store.InternalError{Message: "int() argument must be a string, a bytes-like object or a real number, not '" +
		store.PyTypeName(v) + "'"}
}

// Extract is assistant.extract_ideas(store, text, actor, source, limit).
func Extract(ctx context.Context, s *store.Store, textArg, source, limitArg any, actor string, who store.Who) (*pyjson.Object, error) {
	text := strings.ReplaceAll(pyjson.Str(orValue(textArg, "")), "\r\n", "\n")
	if pytext.Strip(text) == "" {
		return nil, &store.Error{Message: "text is required"}
	}
	var blocks []string
	var current []string
	flush := func() {
		blocks = append(blocks, pytext.Strip(strings.Join(current, " ")))
	}
	for _, raw := range pytext.SplitLines(text, false) {
		line := pytext.Strip(raw)
		switch {
		case line == "":
			if len(current) > 0 {
				flush()
				current = nil
			}
		case bulletRe.MatchString(line):
			if len(current) > 0 {
				flush()
			}
			loc := bulletRe.FindStringIndex(line)
			current = []string{line[loc[1]:]}
		case headingRe.MatchString(line):
			if len(current) > 0 {
				flush()
			}
			current = nil
		default:
			current = append(current, line)
		}
	}
	if len(current) > 0 {
		flush()
	}
	kept := []string{}
	for _, b := range blocks {
		if pytext.Len(b) >= 2 {
			kept = append(kept, b)
		}
	}
	limit, err := pyIntArg(orValue(limitArg, int64(24)))
	if err != nil {
		return nil, err
	}
	n := max(int64(1), min(int64(60), limit))
	if int64(len(kept)) > n {
		kept = kept[:n]
	}
	if len(kept) == 0 {
		return nil, &store.Error{Message: "no idea-like lines found"}
	}
	var sourceUnit *pyjson.Object
	if pyjson.Truthy(source) {
		if sourceUnit, err = s.GetUnitAny(ctx, source); err != nil {
			return nil, err
		}
	}
	created := []any{}
	skipped := int64(0)
	for _, block := range kept {
		title := block
		if pytext.Len(block) > 120 {
			title = pytext.RStrip(pytext.Prefix(block, 117)) + "…"
		}
		sourceTitle := any("Pasted outline")
		if sourceUnit != nil {
			sourceTitle = sourceUnit.Value("title")
		}
		data := pyjson.NewObject(
			"type", "idea", "title", title, "body", block,
			"tags", []any{"origin:extract", "stage:capture", "topic:intake"},
			"importance", "p2", "origin", "assistant", "source_title", sourceTitle,
			"meta", pyjson.NewObject("extracted", true, "extractor", "outline-v1"))
		unit, err := s.CreateUnit(ctx, data, actor, who)
		if err != nil {
			var se *store.Error
			if errors.As(err, &se) {
				skipped++
				continue
			}
			return nil, err
		}
		if sourceUnit != nil {
			_, lerr := s.CreateLink(ctx, unit.Value("id"), sourceUnit.Value("id"), "derived_from", "Extracted from source", actor)
			if lerr == nil {
				if again, gerr := s.GetUnitAny(ctx, unit.Value("id")); gerr == nil {
					unit = again
				} else if !isStoreErr(gerr) {
					return nil, gerr
				}
			} else if !isStoreErr(lerr) {
				return nil, lerr
			}
		}
		created = append(created, unit)
	}
	var sourceUID any
	if sourceUnit != nil {
		sourceUID = sourceUnit.Value("uid")
	}
	detail := pyjson.NewObject("created", int64(len(created)), "skipped", skipped, "source", sourceUID)
	if err := s.LogActivity(ctx, actor, "assistant.extract", nil, detail); err != nil {
		return nil, err
	}
	return pyjson.NewObject("created", created, "created_count", int64(len(created)), "skipped", skipped), nil
}

func isStoreErr(err error) bool {
	var se *store.Error
	return errors.As(err, &se)
}

func linkTarget(links any, match func(rel string, target *pyjson.Object) bool) any {
	list, _ := links.([]any)
	for _, item := range list {
		l, ok := item.(*pyjson.Object)
		if !ok {
			continue
		}
		target, _ := l.Value("target").(*pyjson.Object)
		rel, _ := l.Value("relation").(string)
		if target != nil && match(rel, target) {
			return target
		}
	}
	return nil
}

func or(v any, def string) string {
	if pyjson.Truthy(v) {
		return pyjson.Str(v)
	}
	return def
}

func ref(v any, missing string) string {
	t, ok := v.(*pyjson.Object)
	if !ok {
		return missing
	}
	return pyjson.Str(t.Value("uid")) + " · " + pyjson.Str(t.Value("title"))
}

// Dispatch is assistant.dispatch_prompt(store, task_ref).
func Dispatch(ctx context.Context, s *store.Store, taskRef string) (*pyjson.Object, error) {
	task, err := s.GetUnit(ctx, taskRef)
	if err != nil {
		return nil, err
	}
	if t, _ := task.Value("type").(string); t != "task" {
		return nil, &store.Error{Message: "dispatch requires a task unit"}
	}
	out := task.Value("links_out")
	typeOf := func(t *pyjson.Object) string { v, _ := t.Value("type").(string); return v }
	claim := linkTarget(out, func(rel string, t *pyjson.Object) bool {
		return rel == "targets" && (typeOf(t) == "claim" || typeOf(t) == "question")
	})
	experiment := linkTarget(out, func(rel string, t *pyjson.Object) bool {
		return rel == "part_of" && typeOf(t) == "experiment"
	})
	agent := linkTarget(out, func(rel string, _ *pyjson.Object) bool { return rel == "assigned_to" })
	host := linkTarget(out, func(rel string, _ *pyjson.Object) bool { return rel == "runs_on" })
	gates := []any{}
	gateNames := []string{}
	for _, text := range stringList(task.Value("tags")) {
		switch store.TagNamespace(text) {
		case "gate", "risk", "protocol":
			gates = append(gates, text)
			gateNames = append(gateNames, text)
		}
	}
	gateText := "—"
	if len(gateNames) > 0 {
		gateText = strings.Join(gateNames, ", ")
	}
	lines := []string{
		"# ARK dispatch · " + pyjson.Str(task.Value("uid")) + " — " + pyjson.Str(task.Value("title")),
		"",
		"You are receiving one task from an ARK research project. Do not expand scope.",
		"",
		"## Task",
		"- UID: " + pyjson.Str(task.Value("uid")),
		"- Status: " + pyjson.Str(task.Value("status")),
		"- Importance: " + pyjson.Str(task.Value("importance")),
		"- Window: " + or(task.Value("start_at"), "—") + " → " + or(task.Value("end_at"), "—"),
		"- Body: " + or(task.Value("body"), "—"),
		"",
		"## Trace",
		"- Claim / question: " + ref(claim, "MISSING — stop and ask"),
		"- Experiment: " + ref(experiment, "—"),
		"- Agent: " + ref(agent, "UNASSIGNED"),
		"- Host: " + ref(host, "—"),
		"- Gates: " + gateText,
		"",
		"## Contract",
		"1. Work only in the assigned worktree/host; never touch another task's artifacts.",
		"2. Write a report with command, commit, seeds, budget, artifact path and stop condition.",
		"3. If a gate fails, stop and report the residual; do not retry silently.",
		"4. Return the report path and a one-paragraph verdict to the manager.",
		"",
		"## Suggested CLI",
		"```bash",
		"python -m arkstudio agent context --project \"" + s.Path + "\"",
		"```",
		"",
		"_Generated locally by ARK Studio. No message was sent._",
	}
	summary := pyjson.NewObject()
	for _, k := range []string{"id", "uid", "title", "status", "importance", "body"} {
		summary.Set(k, task.Value(k))
	}
	return pyjson.NewObject("task", summary, "claim", claim, "experiment", experiment, "agent", agent,
		"host", host, "gates", gates, "prompt", strings.Join(lines, "\n")), nil
}

// stringList reads a hydrated tag list ([]string in the Go store, []any
// when it came from JSON).
func stringList(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
