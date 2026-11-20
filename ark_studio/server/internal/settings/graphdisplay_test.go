package settings

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func gdLoad(t *testing.T, text string) *pyjson.Object {
	t.Helper()
	v, ok := pyjson.LoadsObject(text)
	if !ok {
		t.Fatalf("bad json %s", text)
	}
	return v
}

func TestGraphDisplayStrict(t *testing.T) {
	got, err := GraphDisplay(gdLoad(t, `{"mind": {"show_images": false, "align": "center", "image_size": 300.0},
		"local": {"depth": 3}, "force": {"marker_gap": 1}, "general": {}}`), true)
	if err != nil {
		t.Fatal(err)
	}
	// Schema order, sparse, empty tabs dropped, integers kept whole.
	want := `{"force": {"marker_gap": 1.0}, "mind": {"align": "center", "show_images": false, "image_size": 300}, "local": {"depth": 3}}`
	if s := string(pyjson.Dumps(got)); s != want {
		t.Errorf("got %s\nwant %s", s, want)
	}
	for body, msg := range map[string]string{
		`{"mind": {"zoom": 1}}`:               "unknown graph display setting: mind.zoom",
		`{"tree": {}}`:                        "unknown graph display tab: tree",
		`{"mind": {"align": "right"}}`:        "mind.align must be one of left, center",
		`{"mind": {"show_images": 1}}`:        "mind.show_images must be true or false",
		`{"force": {"marker_scale": 9}}`:      "force.marker_scale must be between 0.5 and 2.5",
		`{"force": {"marker_scale": true}}`:   "force.marker_scale must be a number",
		`{"local": {"depth": 1.5}}`:           "local.depth must be a whole number",
		`{"local": 3}`:                        "graph display local must be an object",
		`{"mind": {"image_size": "big"}}`:     "mind.image_size must be a number",
		`{"general": {"transitions": "yes"}}`: "general.transitions must be true or false",
	} {
		if _, err := GraphDisplay(gdLoad(t, body), true); err == nil || err.Error() != msg {
			t.Errorf("%s: got %v, want %q", body, err, msg)
		}
	}
	if _, err := GraphDisplay("x", true); err == nil {
		t.Error("a non-object is refused")
	}
}

func TestGraphDisplayLenient(t *testing.T) {
	got, _ := GraphDisplay(gdLoad(t, `{"mind": {"align": "right", "auto_layout": false, "x": 1}, "tree": {"a": 1}, "local": "x"}`), false)
	if s := string(pyjson.Dumps(got)); s != `{"mind": {"auto_layout": false}}` {
		t.Errorf("lenient read keeps only valid values, got %s", s)
	}
	if got, _ := GraphDisplay("junk", false); got.Len() != 0 {
		t.Error("junk reads as nothing set")
	}
}

func TestProjectSettingsGraphDisplay(t *testing.T) {
	m := gdLoad(t, `{"settings": {}}`)
	s, err := ProjectSettings(m)
	if err != nil {
		t.Fatal(err)
	}
	if s.Has("graph_display") {
		t.Error("absent until a project admin sets it (byte-identical to Python)")
	}
	next, err := SettingsPatch(m, gdLoad(t, `{"graph_display": {"mind": {"show_images": false}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(pyjson.Dumps(next.Value("graph_display"))); got != `{"mind": {"show_images": false}}` {
		t.Errorf("stored %s", got)
	}
	m.Set("settings", next)
	again, _ := ProjectSettings(m)
	if !again.Has("graph_display") {
		t.Error("a stored value is returned")
	}
	cleared, err := SettingsPatch(m, gdLoad(t, `{"graph_display": null}`), nil)
	if err != nil || cleared.Has("graph_display") {
		t.Errorf("null clears: %v %v", err, cleared)
	}
	if _, err := SettingsPatch(m, gdLoad(t, `{"graph_display": {"mind": {"align": 2}}}`), nil); err == nil {
		t.Error("invalid values are refused")
	}
}

func TestPersonalAppearanceGraphDisplay(t *testing.T) {
	merged, err := UpdatePersonalAppearance(nil, gdLoad(t, `{"graph_display": {"force": {"marker_scale": 2}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(pyjson.Dumps(merged)); got != `{"graph_display": {"force": {"marker_scale": 2.0}}}` {
		t.Errorf("got %s", got)
	}
	// Replaces the group; {} or null clears it; other groups untouched.
	merged.Set("graph", pyjson.NewObject("repulsion", 1.0, "distance", 1.0, "gravity", 0.01, "damping", 0.3))
	next, err := UpdatePersonalAppearance(merged, gdLoad(t, `{"graph_display": {}}`))
	if err != nil || next.Has("graph_display") || !next.Has("graph") {
		t.Errorf("clear: %v %s", err, pyjson.Dumps(next))
	}
	if _, err := UpdatePersonalAppearance(nil, gdLoad(t, `{"graph_display": {"mind": {"depth": 2}}}`)); err == nil ||
		!strings.Contains(err.Error(), "unknown graph display setting") {
		t.Errorf("unknown setting: %v", err)
	}
	read := PersonalAppearance(gdLoad(t, `{"graph_display": {"mind": {"align": "x"}}, "arrows": null}`))
	if read.Len() != 0 {
		t.Errorf("an invalid stored group reads as unset, got %s", pyjson.Dumps(read))
	}
}

func TestMindLayout(t *testing.T) {
	patch := gdLoad(t, `{"prj_a": {"widths": {"U-1": 240.4, "U-2": 300},
		"pinned_images": {"U-1": {"media_id": 7, "side": "left", "w": 260, "h": 130, "aspect": 2}}}}`)
	got, err := UpdateMindLayout(nil, patch)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"prj_a": {"widths": {"U-1": 240, "U-2": 300}, "pinned_images": {"U-1": {"media_id": 7, "side": "left", "w": 260.0, "h": 130.0, "aspect": 2.0}}}}`
	if s := string(pyjson.Dumps(got)); s != want {
		t.Errorf("got %s\nwant %s", s, want)
	}
	// Another project is added after it; null removes one project only.
	got, _ = UpdateMindLayout(got, gdLoad(t, `{"prj_b": {"widths": {"U-9": 200}}}`))
	if strings.Join(got.Keys(), ",") != "prj_a,prj_b" {
		t.Errorf("keys %v", got.Keys())
	}
	got, _ = UpdateMindLayout(got, gdLoad(t, `{"prj_a": null}`))
	if strings.Join(got.Keys(), ",") != "prj_b" {
		t.Errorf("after delete %v", got.Keys())
	}
	got, _ = UpdateMindLayout(got, gdLoad(t, `{"prj_b": {}}`))
	if got.Len() != 0 {
		t.Error("an empty entry removes the project")
	}
	for _, bad := range []string{
		`{"prj_a": {"widths": {"U-1": 10}}}`,
		`{"prj_a": {"widths": {"U-1": "wide"}}}`,
		`{"prj_a": {"order": []}}`,
		`{"prj_a": {"pinned_images": {"U-1": {"media_id": 1, "side": "middle", "w": 10, "h": 10}}}}`,
		`{"prj_a": {"pinned_images": {"U-1": {"media_id": 1, "w": 10}}}}`,
		`{"prj_a": {"pinned_images": {"U-1": {"media_id": null, "w": 10, "h": 10}}}}`,
		`{"": {"widths": {}}}`,
		`[]`,
	} {
		var v any
		if strings.HasPrefix(bad, "[") {
			v = []any{}
		} else {
			v = gdLoad(t, bad)
		}
		if _, err := UpdateMindLayout(nil, v); err == nil {
			t.Errorf("%s accepted", bad)
		} else if _, ok := err.(MindLayoutError); !ok {
			t.Errorf("%s: error type %T", bad, err)
		}
	}
}

func TestMindLayoutBounds(t *testing.T) {
	base := pyjson.NewObject()
	for i := 0; i < maxMindLayoutProjects; i++ {
		base.Set("p"+strings.Repeat("x", i+1), pyjson.NewObject("widths", pyjson.NewObject("U", int64(200))))
	}
	got, err := UpdateMindLayout(base, gdLoad(t, `{"new": {"widths": {"U": 220}}}`))
	if err != nil {
		t.Fatal(err)
	}
	keys := got.Keys()
	if len(keys) != maxMindLayoutProjects || keys[len(keys)-1] != "new" || keys[0] != "pxx" {
		t.Errorf("the oldest project is dropped: %d %s..%s", len(keys), keys[0], keys[len(keys)-1])
	}
	widths := pyjson.NewObject()
	for i := 0; i <= maxMindLayoutNodes; i++ {
		widths.Set("U-"+strings.Repeat("1", 1)+string(rune('a'+i%26))+strings.Repeat("z", i/26), int64(200))
	}
	if _, err := UpdateMindLayout(nil, pyjson.NewObject("p", pyjson.NewObject("widths", widths))); err == nil {
		t.Error("more than the node limit is refused")
	}
	// The read path drops what it cannot use.
	read := MindLayoutPrefs(gdLoad(t, `{"p": {"widths": {"U": 5, "V": 200}}, "q": "x"}`))
	if s := string(pyjson.Dumps(read)); s != `{"p": {"widths": {"V": 200}}}` {
		t.Errorf("read %s", s)
	}
}
