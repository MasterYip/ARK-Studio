// Package markers holds the node marker groups (Go only): the small icons
// (stars, flags, priority, progress, symbols) drawn next to a node.
//
// A marker on a node is the tag "marker:<id>".  Markers are organised in
// groups; in an exclusive group a node holds at most one marker, so adding
// one replaces the others of that group (a node has one star colour, one
// priority, one progress step).  Every marker is one tag, so a node never
// holds the same marker twice.
//
// The groups are a project setting, settings.markers = {"groups": [...]}
// (PATCH /api/project/settings, project admins).  Without one the project
// uses Defaults.  Names and labels may be empty: the client then shows the
// built-in, translated text for the id (ark_studio/web/js/markers.js keeps
// the same default table).
package markers

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"arkstudio/go/internal/compat/pyjson"
)

// Namespace is the tag namespace of markers.
const Namespace = "marker"

// Limits of a marker group setting.
const (
	MaxGroups          = 12
	MaxMarkersPerGroup = 16
	MaxNameRunes       = 40
	MaxGlyphRunes      = 3
)

// Marker is one marker of a group.
type Marker struct {
	ID    string
	Glyph string
	Color string
	Label string
}

// Group is one marker group.
type Group struct {
	ID        string
	Name      string
	Exclusive bool
	Markers   []Marker
}

var (
	idRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// Defaults are the built-in groups (names and labels in English for
// agents; the client translates them by id).
func Defaults() []Group {
	m := func(id, glyph, color string) Marker { return Marker{ID: id, Glyph: glyph, Color: color} }
	return []Group{
		{ID: "star", Exclusive: true, Markers: []Marker{
			m("star-yellow", "★", "#f59e0b"), m("star-red", "★", "#ef4444"),
			m("star-blue", "★", "#3b82f6"), m("star-green", "★", "#22c55e")}},
		{ID: "flag", Exclusive: true, Markers: []Marker{
			m("flag-red", "⚑", "#ef4444"), m("flag-orange", "⚑", "#f97316"),
			m("flag-blue", "⚑", "#3b82f6"), m("flag-green", "⚑", "#22c55e")}},
		{ID: "priority", Exclusive: true, Markers: []Marker{
			m("priority-1", "①", "#ef4444"), m("priority-2", "②", "#f97316"), m("priority-3", "③", "#3b82f6")}},
		{ID: "progress", Exclusive: true, Markers: []Marker{
			m("progress-0", "○", "#64748b"), m("progress-25", "◔", "#0ea5e9"), m("progress-50", "◑", "#0ea5e9"),
			m("progress-75", "◕", "#0ea5e9"), m("progress-100", "●", "#22c55e")}},
		{ID: "symbol", Exclusive: false, Markers: []Marker{
			m("check-green", "✓", "#22c55e"), m("question", "?", "#38bdf8"), m("priority", "!", "#fb923c")}},
	}
}

// builtinText are the English names of the default groups and markers.
var builtinText = map[string]string{
	"group:star": "Stars", "group:flag": "Flags", "group:priority": "Priority", "group:progress": "Progress",
	"group:symbol": "Symbols",
	"star-yellow":  "Yellow star", "star-red": "Red star", "star-blue": "Blue star", "star-green": "Green star",
	"flag-red": "Red flag", "flag-orange": "Orange flag", "flag-blue": "Blue flag", "flag-green": "Green flag",
	"priority-1": "Priority 1", "priority-2": "Priority 2", "priority-3": "Priority 3",
	"progress-0": "Not started", "progress-25": "25% done", "progress-50": "Half done", "progress-75": "75% done",
	"progress-100": "Done",
	"check-green":  "Check", "question": "Question", "priority": "Important",
}

// Error is a validation error of the marker setting.
type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

func fail(msg string) error { return &Error{"markers: " + msg} }

func text(v any) (string, bool) {
	s, ok := v.(string)
	return strings.TrimSpace(s), ok
}

func cleanName(v any, what string) (string, error) {
	if v == nil {
		return "", nil
	}
	s, ok := text(v)
	if !ok {
		return "", fail(what + " must be a string")
	}
	if utf8.RuneCountInString(s) > MaxNameRunes {
		return "", fail(what + " is too long")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", fail(what + " has control characters")
		}
	}
	return s, nil
}

func cleanGlyph(v any) (string, error) {
	s, ok := text(v)
	if !ok || s == "" {
		return "", fail("each marker needs a glyph")
	}
	if utf8.RuneCountInString(s) > MaxGlyphRunes {
		return "", fail("a glyph has at most 3 characters")
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fail("a glyph has no spaces or control characters")
		}
	}
	return s, nil
}

// Parse reads the stored or submitted setting {"groups": [...]}.  A nil
// value (or one without groups) is (nil, nil): the project uses Defaults.
func Parse(value any) ([]Group, error) {
	if value == nil {
		return nil, nil
	}
	obj, ok := value.(*pyjson.Object)
	if !ok {
		return nil, fail("must be an object with groups")
	}
	for _, k := range obj.Keys() {
		if k != "groups" {
			return nil, fail("unknown key: " + k)
		}
	}
	raw, has := obj.Get("groups")
	if !has || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fail("groups must be a list")
	}
	if len(list) > MaxGroups {
		return nil, fail("at most 12 groups")
	}
	groupIDs, markerIDs := map[string]bool{}, map[string]bool{}
	out := make([]Group, 0, len(list))
	for _, item := range list {
		g, ok := item.(*pyjson.Object)
		if !ok {
			return nil, fail("each group must be an object")
		}
		id, _ := text(g.Value("id"))
		if !idRe.MatchString(id) {
			return nil, fail("bad group id: " + pyjson.Repr(id))
		}
		if groupIDs[id] {
			return nil, fail("duplicate group id: " + id)
		}
		groupIDs[id] = true
		name, err := cleanName(g.Value("name"), "group name")
		if err != nil {
			return nil, err
		}
		group := Group{ID: id, Name: name}
		if v, ok := g.Get("exclusive"); ok && v != nil {
			b, isBool := v.(bool)
			if !isBool {
				return nil, fail("exclusive must be true or false")
			}
			group.Exclusive = b
		}
		ms, ok := g.Value("markers").([]any)
		if !ok || len(ms) == 0 {
			return nil, fail("group " + id + " needs at least one marker")
		}
		if len(ms) > MaxMarkersPerGroup {
			return nil, fail("at most 16 markers per group")
		}
		for _, mi := range ms {
			mo, ok := mi.(*pyjson.Object)
			if !ok {
				return nil, fail("each marker must be an object")
			}
			mid, _ := text(mo.Value("id"))
			if !idRe.MatchString(mid) {
				return nil, fail("bad marker id: " + pyjson.Repr(mid))
			}
			if markerIDs[mid] {
				return nil, fail("marker " + mid + " is in more than one group")
			}
			markerIDs[mid] = true
			glyph, err := cleanGlyph(mo.Value("glyph"))
			if err != nil {
				return nil, err
			}
			color, _ := text(mo.Value("color"))
			if !colorRe.MatchString(color) {
				return nil, fail("marker " + mid + " needs a color like #f59e0b")
			}
			label, err := cleanName(mo.Value("label"), "marker label")
			if err != nil {
				return nil, err
			}
			group.Markers = append(group.Markers, Marker{ID: mid, Glyph: glyph, Color: strings.ToLower(color), Label: label})
		}
		out = append(out, group)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// Effective is the project's groups: the stored setting, or Defaults when
// there is none (or it cannot be read).
func Effective(value any) []Group {
	if groups, err := Parse(value); err == nil && groups != nil {
		return groups
	}
	return Defaults()
}

// ToJSON is the stored form of groups.
func ToJSON(groups []Group) *pyjson.Object {
	list := make([]any, 0, len(groups))
	for _, g := range groups {
		ms := make([]any, 0, len(g.Markers))
		for _, m := range g.Markers {
			ms = append(ms, pyjson.NewObject("id", m.ID, "glyph", m.Glyph, "color", m.Color, "label", m.Label))
		}
		list = append(list, pyjson.NewObject("id", g.ID, "name", g.Name, "exclusive", g.Exclusive, "markers", ms))
	}
	return pyjson.NewObject("groups", list)
}

// Describe is the agent view of groups: names and labels filled in from
// the built-in English text where the setting leaves them empty, and the
// tag of each marker.
func Describe(groups []Group, custom bool) *pyjson.Object {
	list := make([]any, 0, len(groups))
	for _, g := range groups {
		name := g.Name
		if name == "" {
			name = orID(builtinText["group:"+g.ID], g.ID)
		}
		ms := make([]any, 0, len(g.Markers))
		for _, m := range g.Markers {
			label := m.Label
			if label == "" {
				label = orID(builtinText[m.ID], m.ID)
			}
			ms = append(ms, pyjson.NewObject("id", m.ID, "tag", Namespace+":"+m.ID, "glyph", m.Glyph,
				"color", m.Color, "label", label))
		}
		list = append(list, pyjson.NewObject("id", g.ID, "name", name, "exclusive", g.Exclusive, "markers", ms))
	}
	return pyjson.NewObject("groups", list, "custom", custom,
		"rules", "A marker is the tag marker:<id>. A node has each marker at most once; in an exclusive group "+
			"it has at most one marker, and the one written last replaces the others.")
}

func orID(s, id string) string {
	if s == "" {
		return id
	}
	return s
}

// ExclusiveGroupOf maps each marker id of an exclusive group to its group id.
func ExclusiveGroupOf(groups []Group) map[string]string {
	out := map[string]string{}
	for _, g := range groups {
		if !g.Exclusive {
			continue
		}
		for _, m := range g.Markers {
			out[m.ID] = g.ID
		}
	}
	return out
}

// Resolve applies the exclusive-group rule to tags in write order: of the
// markers of one exclusive group only the last one stays.  keyOf returns a
// tag's normalised "ns:value" form ("" when it cannot be read; such tags are
// kept for the caller's validation).  Other tags keep their place.
func Resolve(tags []any, keyOf func(any) string, groups []Group) []any {
	exclusive := ExclusiveGroupOf(groups)
	if len(exclusive) == 0 {
		return tags
	}
	last := map[string]int{} // group id -> index of the marker that stays
	for i, t := range tags {
		if g := groupOfTag(keyOf(t), exclusive); g != "" {
			last[g] = i
		}
	}
	if len(last) == 0 {
		return tags
	}
	out := make([]any, 0, len(tags))
	for i, t := range tags {
		if g := groupOfTag(keyOf(t), exclusive); g != "" && last[g] != i {
			continue
		}
		out = append(out, t)
	}
	return out
}

func groupOfTag(key string, exclusive map[string]string) string {
	if !strings.HasPrefix(key, Namespace+":") {
		return ""
	}
	return exclusive[key[len(Namespace)+1:]]
}
