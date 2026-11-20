package settings

import (
	"math"
	"sort"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
)

// Go-only (T-0036): graph display preferences, grouped by graph type.
//
//   - project settings "graph_display" (PATCH /api/project/settings, admins):
//     the project default, shared with every collaborator;
//   - profile prefs.appearance.graph_display (PATCH /api/me): the person's
//     override, sparse (only the settings they changed) so every other
//     setting keeps following the project.
//
// Both use the same shape, {tab: {key: value}}; a value missing at every
// level falls back to the built-in default in the client
// (ark_studio/web/js/graph-display.js keeps the same table).  Python refuses
// the new section, group and preference (tests/python/contract/go_only.py
// FIXES "project-graph-display", "profile-graph-display", "profile-mind-layout").

type gdKind int

const (
	gdBool gdKind = iota
	gdNumber
	gdInt
	gdChoice
)

type gdField struct {
	key      string
	kind     gdKind
	min, max float64
	choices  []string
}

type gdTab struct {
	id     string
	fields []gdField
}

// graphDisplaySchema lists the tabs and settings in display order.
var graphDisplaySchema = []gdTab{
	{"general", []gdField{{key: "transitions", kind: gdBool}, {key: "agent_presence", kind: gdBool}}},
	{"force", []gdField{
		{key: "marker_scale", kind: gdNumber, min: 0.5, max: 2.5},
		{key: "marker_gap", kind: gdNumber, min: 0, max: 2},
		{key: "marker_arc", kind: gdNumber, min: 0.1, max: 1},
	}},
	{"mind", []gdField{
		{key: "align", kind: gdChoice, choices: []string{"left", "center"}},
		{key: "auto_layout", kind: gdBool},
		{key: "marker_scale", kind: gdNumber, min: 0.3, max: 2},
		{key: "show_images", kind: gdBool},
		{key: "image_size", kind: gdInt, min: 80, max: 620},
	}},
	{"local", []gdField{
		{key: "depth", kind: gdInt, min: 1, max: 3},
		{key: "rings", kind: gdBool},
	}},
}

func gdTabByID(id string) *gdTab {
	for i := range graphDisplaySchema {
		if graphDisplaySchema[i].id == id {
			return &graphDisplaySchema[i]
		}
	}
	return nil
}

func (f gdField) find(key string) bool { return f.key == key }

func gdNumberValue(v any) (float64, bool) {
	switch x := v.(type) {
	case int64:
		return float64(x), true
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return 0, false
		}
		return x, true
	}
	return 0, false
}

// clean validates one value; the error text names the setting.
func (f gdField) clean(tab string, v any) (any, error) {
	name := tab + "." + f.key
	switch f.kind {
	case gdBool:
		if b, ok := v.(bool); ok {
			return b, nil
		}
		return nil, &Error{name + " must be true or false"}
	case gdChoice:
		if s, ok := v.(string); ok && contains(f.choices, s) {
			return s, nil
		}
		return nil, &Error{name + " must be one of " + strings.Join(f.choices, ", ")}
	}
	n, ok := gdNumberValue(v)
	if !ok {
		return nil, &Error{name + " must be a number"}
	}
	if n < f.min || n > f.max {
		return nil, &Error{name + " must be between " + fmtG(f.min) + " and " + fmtG(f.max)}
	}
	if f.kind == gdInt {
		if n != math.Trunc(n) {
			return nil, &Error{name + " must be a whole number"}
		}
		return int64(n), nil
	}
	return n, nil
}

// GraphDisplay validates (strict: PATCH bodies) or cleans (lenient: stored
// values) a graph_display object.  The result is sparse: only the settings
// present (and valid) are kept, tabs without any are omitted, and the
// order follows the schema.  An empty result means "nothing set".
func GraphDisplay(value any, strict bool) (*pyjson.Object, error) {
	out := pyjson.NewObject()
	if value == nil {
		return out, nil
	}
	v, ok := value.(*pyjson.Object)
	if !ok {
		if strict {
			return nil, &Error{"graph display settings must be an object"}
		}
		return out, nil
	}
	if strict {
		var unknown []string
		for _, k := range v.Keys() {
			if gdTabByID(k) == nil {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return nil, &Error{"unknown graph display tab: " + strings.Join(unknown, ", ")}
		}
	}
	for _, tab := range graphDisplaySchema {
		raw := v.Value(tab.id)
		if raw == nil {
			continue
		}
		values, ok := raw.(*pyjson.Object)
		if !ok {
			if strict {
				return nil, &Error{"graph display " + tab.id + " must be an object"}
			}
			continue
		}
		if strict {
			for _, k := range values.Keys() {
				known := false
				for _, f := range tab.fields {
					known = known || f.find(k)
				}
				if !known {
					return nil, &Error{"unknown graph display setting: " + tab.id + "." + k}
				}
			}
		}
		clean := pyjson.NewObject()
		for _, f := range tab.fields {
			item, has := values.Get(f.key)
			if !has || item == nil {
				continue
			}
			c, err := f.clean(tab.id, item)
			if err != nil {
				if strict {
					return nil, err
				}
				continue
			}
			clean.Set(f.key, c)
		}
		if clean.Len() > 0 {
			out.Set(tab.id, clean)
		}
	}
	return out, nil
}

// ------------------------------------------------- per-node Mind layout

// prefs.mind_layout = {project_id: {"widths": {uid: px}, "pinned_images":
// {uid: {"media_id", "side", "w", "h", "aspect"}}}}: the card widths and
// pinned images one person arranged in a project's Mind graph.  Bounded:
// the most recently saved projects are kept.

const (
	maxMindLayoutProjects = 30
	maxMindLayoutNodes    = 500
)

var mindImageSides = []string{"right", "left", "top", "bottom"}

// MindLayoutError is an invalid prefs.mind_layout value.
type MindLayoutError struct{ Message string }

func (e MindLayoutError) Error() string { return e.Message }

func mindLayoutErr(msg string) error {
	return MindLayoutError{"invalid Mind layout preferences: " + msg}
}

func mindNumber(v any, lo, hi float64) (float64, bool) {
	n, ok := gdNumberValue(v)
	return n, ok && n >= lo && n <= hi
}

func mindPinnedImage(value any) (*pyjson.Object, bool) {
	v, ok := value.(*pyjson.Object)
	if !ok || !onlyKeys(v, "media_id", "side", "w", "h", "aspect") {
		return nil, false
	}
	media := v.Value("media_id")
	switch m := media.(type) {
	case string:
		if !validProjectID(m) {
			return nil, false
		}
	case int64:
	default:
		return nil, false
	}
	side := "right"
	if s, has := v.Get("side"); has {
		str, ok := s.(string)
		if !ok || !contains(mindImageSides, str) {
			return nil, false
		}
		side = str
	}
	w, okW := mindNumber(v.Value("w"), 8, 8000)
	h, okH := mindNumber(v.Value("h"), 8, 8000)
	if !okW || !okH {
		return nil, false
	}
	out := pyjson.NewObject("media_id", media, "side", side, "w", w, "h", h)
	if a, has := v.Get("aspect"); has && a != nil {
		aspect, ok := mindNumber(a, 0.01, 100)
		if !ok {
			return nil, false
		}
		out.Set("aspect", aspect)
	}
	return out, true
}

// mindProjectLayout cleans one project's entry; strict refuses any invalid
// part, lenient drops it.  The result is empty when nothing is left.
func mindProjectLayout(value any, strict bool) (*pyjson.Object, error) {
	v, ok := value.(*pyjson.Object)
	if !ok || (strict && !onlyKeys(v, "widths", "pinned_images")) {
		if strict {
			return nil, mindLayoutErr("a project entry has widths and pinned_images")
		}
		return pyjson.NewObject(), nil
	}
	out := pyjson.NewObject()
	section := func(key string, item func(any) (any, bool)) error {
		raw := v.Value(key)
		if raw == nil {
			return nil
		}
		m, ok := raw.(*pyjson.Object)
		if !ok || (strict && m.Len() > maxMindLayoutNodes) {
			if strict {
				return mindLayoutErr(key + " must be an object of at most 500 nodes")
			}
			return nil
		}
		clean := pyjson.NewObject()
		for _, uid := range m.Keys() {
			c, ok := item(m.Value(uid))
			if !ok || !validProjectID(uid) {
				if strict {
					return mindLayoutErr(key + " has an invalid entry for " + uid)
				}
				continue
			}
			if clean.Len() < maxMindLayoutNodes {
				clean.Set(uid, c)
			}
		}
		if clean.Len() > 0 {
			out.Set(key, clean)
		}
		return nil
	}
	if err := section("widths", func(x any) (any, bool) {
		n, ok := mindNumber(x, 60, 4000)
		return int64(math.Round(n)), ok
	}); err != nil {
		return nil, err
	}
	if err := section("pinned_images", func(x any) (any, bool) { return mindPinnedImage(x) }); err != nil {
		return nil, err
	}
	return out, nil
}

// MindLayoutPrefs cleans a stored prefs.mind_layout (lenient read path).
func MindLayoutPrefs(value any) *pyjson.Object {
	out := pyjson.NewObject()
	v, ok := value.(*pyjson.Object)
	if !ok {
		return out
	}
	for _, pid := range v.Keys() {
		if !validProjectID(pid) {
			continue
		}
		entry, _ := mindProjectLayout(v.Value(pid), false)
		if entry.Len() > 0 {
			out.Set(pid, entry)
		}
	}
	for out.Len() > maxMindLayoutProjects {
		out.Delete(out.Keys()[0])
	}
	return out
}

// UpdateMindLayout applies a PATCH /api/me prefs.mind_layout patch
// {project_id: entry | null}: an entry replaces that project's layout (and
// makes it the most recent), null or an empty entry removes it.  The oldest
// projects are dropped beyond the limit.
func UpdateMindLayout(base, patch any) (*pyjson.Object, error) {
	p, ok := patch.(*pyjson.Object)
	if !ok {
		return nil, mindLayoutErr("expected an object keyed by project id")
	}
	if p.Len() > maxMindLayoutProjects {
		return nil, mindLayoutErr("too many projects in one update")
	}
	merged := MindLayoutPrefs(base)
	for _, pid := range p.Keys() {
		if !validProjectID(pid) {
			return nil, mindLayoutErr("invalid project id")
		}
		merged.Delete(pid)
		raw := p.Value(pid)
		if raw == nil {
			continue
		}
		entry, err := mindProjectLayout(raw, true)
		if err != nil {
			return nil, err
		}
		if entry.Len() > 0 {
			merged.Set(pid, entry)
		}
	}
	for merged.Len() > maxMindLayoutProjects {
		merged.Delete(merged.Keys()[0])
	}
	return merged, nil
}
