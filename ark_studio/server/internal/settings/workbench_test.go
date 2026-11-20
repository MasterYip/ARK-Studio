package settings

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func mustLoad(t *testing.T, text string) any {
	t.Helper()
	v, err := pyjson.Loads(text)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

const sampleLayout = `{"v": 1, "containers": {
  "left": {"views": ["terminal", "filters"], "active": "terminal", "visible": true, "size": 288,
           "sections": [{"views": ["terminal", "filters"], "active": "terminal", "size": 240}]},
  "right": {"views": ["details"], "active": "details", "visible": false, "size": 430},
  "bottom": {"views": ["assistant"], "active": "", "visible": true, "size": 260, "maximized": true}}}`

// T-0024: the current arrangement rides along with the presets.
func TestWorkbenchPrefsCurrentLayout(t *testing.T) {
	in := mustLoad(t, `{"presets": [], "default": "", "layout_at": 1789000000123, "layout": `+sampleLayout+`}`)
	out, err := WorkbenchPrefs(in, true)
	if err != nil {
		t.Fatal(err)
	}
	layout, ok := out.Value("layout").(*pyjson.Object)
	if !ok {
		t.Fatalf("layout dropped: %s", pyjson.Dumps(out))
	}
	left := layout.Value("containers").(*pyjson.Object).Value("left").(*pyjson.Object)
	if got := string(pyjson.Dumps(left.Value("views"))); got != `["terminal", "filters"]` {
		t.Fatalf("left views = %s", got)
	}
	if at := out.Value("layout_at"); at != int64(1789000000123) {
		t.Fatalf("layout_at = %#v", at)
	}
	bottom := layout.Value("containers").(*pyjson.Object).Value("bottom").(*pyjson.Object)
	if bottom.Value("maximized") != true {
		t.Fatalf("maximized lost: %s", pyjson.Dumps(bottom))
	}
	// Without a layout the reply has none (and null means none).
	for _, text := range []string{`{"presets": []}`, `{"presets": [], "layout": null}`} {
		out, err := WorkbenchPrefs(mustLoad(t, text), true)
		if err != nil {
			t.Fatal(err)
		}
		if out.Has("layout") {
			t.Fatalf("%s: unexpected layout %s", text, pyjson.Dumps(out))
		}
	}
}

func TestWorkbenchPrefsRejectsBadLayout(t *testing.T) {
	bad := []string{
		`{"layout": {"v": 2, "containers": {}}}`,
		`{"layout": {"v": 1, "containers": {"top": {}}}}`,
		`{"layout": {"v": 1, "containers": {"left": {"views": ["Bad Id"]}}}}`,
		`{"layout": {"v": 1, "containers": {"left": {"views": ["a"]}, "right": {"views": ["a"]}}}}`,
		`{"layout": {"v": 1, "containers": {"left": {"views": ["a", "b"], "sections": [{"views": ["b", "a"]}]}}}}`,
		`{"layout": "x"}`,
		`{"layout_at": 5}`,
		`{"layout": ` + sampleLayout + `, "layout_at": "soon"}`,
		`{"layout": ` + sampleLayout + `, "layout_at": -1}`,
		`{"layout": ` + sampleLayout + `, "layout_at": true}`,
		`{"other": 1}`,
	}
	for _, text := range bad {
		if _, err := WorkbenchPrefs(mustLoad(t, text), true); err == nil {
			t.Errorf("%s: accepted", text)
		}
		// Reading a stored value never fails: invalid reads as {}.
		if out, err := WorkbenchPrefs(mustLoad(t, text), false); err != nil || out.Len() != 0 {
			t.Errorf("%s: lenient read = %v, %v", text, out, err)
		}
	}
}
