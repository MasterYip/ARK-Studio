package settings

import (
	"math"
	"math/big"
	"regexp"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// workbench_prefs.py: personal workbench layout presets on the profile.

var (
	builtinPresets = map[string]bool{"default": true, "focus": true, "research": true, "writing": true, "review": true}
	// re.match with `$`: a single trailing newline is accepted.
	viewIDRe   = regexp.MustCompile(`\A[a-z][a-z0-9-]{0,31}\n?\z`)
	presetIDRe = regexp.MustCompile(`\Au:[a-z0-9-]{1,40}\n?\z`)
)

const (
	maxWorkbenchPresets = 20
	maxWorkbenchName    = 60
	maxWorkbenchViews   = 24
)

// WorkbenchError is WorkbenchPrefsError.
type WorkbenchError struct{}

func (WorkbenchError) Error() string { return "invalid workbench layout preferences" }

func onlyKeys(o *pyjson.Object, allowed ...string) bool {
	for _, k := range o.Keys() {
		ok := false
		for _, a := range allowed {
			ok = ok || k == a
		}
		if !ok {
			return false
		}
	}
	return true
}

// pyEqualsOne is `value == 1` (True and 1.0 compare equal).
func pyEqualsOne(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x == 1
	case float64:
		return x == 1
	}
	return false
}

func getDefault(o *pyjson.Object, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

func workbenchLayout(value any) (*pyjson.Object, error) {
	v, ok := value.(*pyjson.Object)
	if !ok || !pyEqualsOne(v.Value("v")) {
		return nil, WorkbenchError{}
	}
	containers, ok := v.Value("containers").(*pyjson.Object)
	if !ok || !onlyKeys(containers, "left", "right", "bottom") {
		return nil, WorkbenchError{}
	}
	out := pyjson.NewObject()
	seen := map[string]bool{}
	for _, name := range []string{"left", "right", "bottom"} {
		raw := containers.Value(name)
		if raw == nil {
			continue
		}
		box, ok := raw.(*pyjson.Object)
		if !ok || !onlyKeys(box, "views", "active", "visible", "size", "maximized", "sections") {
			return nil, WorkbenchError{}
		}
		views, ok := getDefault(box, "views", []any{}).([]any)
		if !ok || len(views) > maxWorkbenchViews {
			return nil, WorkbenchError{}
		}
		clean := []any{}
		in := map[string]bool{}
		for _, view := range views {
			s, ok := view.(string)
			if !ok || !viewIDRe.MatchString(s) || seen[s] {
				return nil, WorkbenchError{}
			}
			seen[s], in[s] = true, true
			clean = append(clean, s)
		}
		active, okActive := getDefault(box, "active", "").(string)
		visible, okVisible := getDefault(box, "visible", true).(bool)
		maximized, okMax := getDefault(box, "maximized", false).(bool)
		var size int64
		okSize := false
		switch x := getDefault(box, "size", int64(300)).(type) {
		case int64:
			size, okSize = x, x >= 80 && x <= 2000
		case *big.Int:
			okSize = false
		case float64:
			if !math.IsNaN(x) && x >= 80 && x <= 2000 {
				size, okSize = int64(x), true
			}
		}
		if !okActive || (active != "" && !in[active]) || !okVisible || !okMax || !okSize {
			return nil, WorkbenchError{}
		}
		entry := pyjson.NewObject("views", clean, "active", active, "visible", visible, "size", size)
		if maximized {
			entry.Set("maximized", true)
		}
		if raw, ok := box.Get("sections"); ok {
			sections, err := workbenchSections(raw, clean)
			if err != nil {
				return nil, err
			}
			entry.Set("sections", sections)
		}
		out.Set(name, entry)
	}
	return pyjson.NewObject("v", int64(1), "containers", out), nil
}

// numberIn is workbench_prefs._number_in: an int or float (not bool) within
// bounds; the int() of it.
func numberIn(v any, lo, hi int64) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, x >= lo && x <= hi
	case float64:
		if !math.IsNaN(x) && x >= float64(lo) && x <= float64(hi) {
			return int64(x), true
		}
	}
	return 0, false // bool, *big.Int (always out of range), anything else
}

// workbenchSections is workbench_prefs._sections (main 8e022a0): stacked
// view sections must list exactly the container's views, in order.
func workbenchSections(value any, views []any) ([]any, error) {
	list, ok := value.([]any)
	if !ok || len(list) > maxWorkbenchViews {
		return nil, WorkbenchError{}
	}
	out := []any{}
	var flat []any
	for _, raw := range list {
		section, ok := raw.(*pyjson.Object)
		if !ok || !onlyKeys(section, "views", "active", "size", "collapsed", "maximized") {
			return nil, WorkbenchError{}
		}
		ids, ok := section.Value("views").([]any)
		if !ok || len(ids) == 0 {
			return nil, WorkbenchError{}
		}
		in := map[string]bool{}
		for _, id := range ids {
			s, ok := id.(string)
			if !ok {
				return nil, WorkbenchError{}
			}
			in[s] = true
		}
		active, okActive := getDefault(section, "active", "").(string)
		size, okSize := numberIn(getDefault(section, "size", int64(240)), 40, 4000)
		collapsed, okCollapsed := getDefault(section, "collapsed", false).(bool)
		maximized, okMax := getDefault(section, "maximized", false).(bool)
		if !okActive || (active != "" && !in[active]) || !okSize || !okCollapsed || !okMax {
			return nil, WorkbenchError{}
		}
		item := pyjson.NewObject("views", append([]any(nil), ids...), "active", active, "size", size)
		if collapsed {
			item.Set("collapsed", true)
		}
		if maximized {
			item.Set("maximized", true)
		}
		out = append(out, item)
		flat = append(flat, ids...)
	}
	if len(flat) != len(views) {
		return nil, WorkbenchError{}
	}
	for i := range flat {
		if flat[i] != views[i] {
			return nil, WorkbenchError{}
		}
	}
	return out, nil
}

// WorkbenchPrefs is workbench_prefs(value, strict): a validated copy; when
// not strict, anything invalid reads as an empty object.
func WorkbenchPrefs(value any, strict bool) (*pyjson.Object, error) {
	out, err := workbenchPrefs(value)
	if err != nil {
		if strict {
			return nil, err
		}
		return pyjson.NewObject(), nil
	}
	return out, nil
}

func workbenchPrefs(value any) (*pyjson.Object, error) {
	v, ok := value.(*pyjson.Object)
	if !ok || !onlyKeys(v, "presets", "default") {
		return nil, WorkbenchError{}
	}
	presets, ok := getDefault(v, "presets", []any{}).([]any)
	if !ok || len(presets) > maxWorkbenchPresets {
		return nil, WorkbenchError{}
	}
	outPresets := []any{}
	ids := map[string]bool{}
	for _, raw := range presets {
		p, ok := raw.(*pyjson.Object)
		if !ok || p.Len() != 3 || !onlyKeys(p, "id", "name", "layout") {
			return nil, WorkbenchError{}
		}
		pid, okID := p.Value("id").(string)
		name, okName := p.Value("name").(string)
		if !okID || !presetIDRe.MatchString(pid) || ids[pid] || !okName || pytext.Strip(name) == "" ||
			pytext.Len(name) > maxWorkbenchName {
			return nil, WorkbenchError{}
		}
		ids[pid] = true
		layout, err := workbenchLayout(p.Value("layout"))
		if err != nil {
			return nil, err
		}
		outPresets = append(outPresets, pyjson.NewObject("id", pid, "name", pytext.Strip(name), "layout", layout))
	}
	def, ok := getDefault(v, "default", "").(string)
	if !ok || (def != "" && !builtinPresets[def] && !ids[def]) {
		return nil, WorkbenchError{}
	}
	return pyjson.NewObject("presets", outPresets, "default", def), nil
}
