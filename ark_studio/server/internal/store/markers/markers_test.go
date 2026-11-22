package markers

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func load(t *testing.T, src string) any {
	t.Helper()
	v, err := pyjson.Loads(src)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func keyOf(v any) string { return strings.ToLower(pyjson.Str(v)) }

func TestDefaultsAreValid(t *testing.T) {
	groups, err := Parse(ToJSON(Defaults()))
	if err != nil || len(groups) != len(Defaults()) {
		t.Fatalf("defaults round trip: %v %v", groups, err)
	}
	for _, g := range Defaults() {
		if builtinText["group:"+g.ID] == "" {
			t.Errorf("no English name for group %s", g.ID)
		}
		for _, m := range g.Markers {
			if builtinText[m.ID] == "" {
				t.Errorf("no English label for %s", m.ID)
			}
		}
	}
	// The markers nodes had before groups existed keep their ids.
	all := map[string]bool{}
	for _, g := range Defaults() {
		for _, m := range g.Markers {
			all[m.ID] = true
		}
	}
	for _, id := range []string{"star-yellow", "star-blue", "star-red", "star-green", "flag-red", "check-green", "question", "priority"} {
		if !all[id] {
			t.Errorf("legacy marker %s is missing", id)
		}
	}
}

func TestResolveKeepsLastOfExclusiveGroup(t *testing.T) {
	tags := []any{"marker:star-yellow", "tag:x", "marker:question", "marker:star-blue", "marker:check-green", "marker:flag-red"}
	got := Resolve(tags, keyOf, Defaults())
	want := []any{"tag:x", "marker:question", "marker:star-blue", "marker:check-green", "marker:flag-red"}
	if pyjson.Repr(got) != pyjson.Repr(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// Non-exclusive group: both symbols stay.
	if got := Resolve([]any{"marker:question", "marker:check-green"}, keyOf, Defaults()); len(got) != 2 {
		t.Fatalf("symbols: %v", got)
	}
	// The same marker twice keeps one (the later place).
	if got := Resolve([]any{"marker:star-red", "marker:star-red"}, keyOf, Defaults()); len(got) != 1 {
		t.Fatalf("same marker: %v", got)
	}
	// Unknown markers are plain tags.
	if got := Resolve([]any{"marker:zzz", "marker:yyy"}, keyOf, Defaults()); len(got) != 2 {
		t.Fatalf("unknown: %v", got)
	}
}

func TestParseCustomGroups(t *testing.T) {
	groups, err := Parse(load(t, `{"groups": [{"id": "mood", "name": "心情", "exclusive": true, "markers": [
		{"id": "mood-happy", "glyph": "☺", "color": "#22C55E", "label": ""},
		{"id": "mood-sad", "glyph": "☹", "color": "#64748b", "label": "Sad"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || !groups[0].Exclusive || groups[0].Markers[0].Color != "#22c55e" || groups[0].Name != "心情" {
		t.Fatalf("parsed %+v", groups)
	}
	got := Resolve([]any{"marker:mood-happy", "marker:mood-sad", "marker:star-red", "marker:star-blue"}, keyOf, groups)
	// Stars are not a group of this project: both stay.
	if pyjson.Repr(got) != pyjson.Repr([]any{"marker:mood-sad", "marker:star-red", "marker:star-blue"}) {
		t.Fatalf("custom resolve: %v", got)
	}
	for _, src := range []string{`null`, `{}`, `{"groups": []}`, `{"groups": null}`} {
		if g, err := Parse(load(t, src)); err != nil || g != nil {
			t.Errorf("%s: %v %v", src, g, err)
		}
	}
	if len(Effective(nil)) != len(Defaults()) {
		t.Error("no setting: defaults")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		`[]`:                        "must be an object",
		`{"group": []}`:             "unknown key",
		`{"groups": {}}`:            "must be a list",
		`{"groups": [{"id": "A"}]}`: "bad group id",
		`{"groups": [{"id": "a", "markers": []}]}`: "at least one marker",
		`{"groups": [{"id": "a", "exclusive": "yes", "markers": [{"id": "x", "glyph": "x", "color": "#000000"}]}]}`:                                                      "exclusive",
		`{"groups": [{"id": "a", "markers": [{"id": "x", "glyph": "", "color": "#000000"}]}]}`:                                                                           "glyph",
		`{"groups": [{"id": "a", "markers": [{"id": "x", "glyph": "abcd", "color": "#000000"}]}]}`:                                                                       "at most 3",
		`{"groups": [{"id": "a", "markers": [{"id": "x", "glyph": "x", "color": "red"}]}]}`:                                                                              "color",
		`{"groups": [{"id": "a", "markers": [{"id": "x", "glyph": "x", "color": "#000000"}]}, {"id": "a", "markers": [{"id": "y", "glyph": "y", "color": "#000000"}]}]}`: "duplicate group",
		`{"groups": [{"id": "a", "markers": [{"id": "x", "glyph": "x", "color": "#000000"}]}, {"id": "b", "markers": [{"id": "x", "glyph": "y", "color": "#000000"}]}]}`: "more than one group",
		`{"groups": [{"id": "a", "name": 3, "markers": [{"id": "x", "glyph": "x", "color": "#000000"}]}]}`:                                                               "must be a string",
	}
	for src, want := range cases {
		if _, err := Parse(load(t, src)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", src, err, want)
		}
	}
}

func TestDescribeFillsBuiltinText(t *testing.T) {
	d := Describe(Defaults(), false)
	first := d.Value("groups").([]any)[0].(*pyjson.Object)
	m := first.Value("markers").([]any)[0].(*pyjson.Object)
	if first.Value("name") != "Stars" || first.Value("exclusive") != true || m.Value("tag") != "marker:star-yellow" || m.Value("label") != "Yellow star" {
		t.Fatalf("describe: %s", pyjson.Dumps(d))
	}
}
