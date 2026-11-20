package settings

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func hfJSON(t *testing.T, s string) any {
	t.Helper()
	v, err := pyjson.Loads(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestHomeFilterPrefs(t *testing.T) {
	merged, err := UpdateHomeFilter(nil, hfJSON(t, `{"prj_a": {"limit": 200, "types": ["task", "claim", "task"], "statuses": [], "days": 7}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := string(pyjson.DumpsASCII(merged))
	want := `{"prj_a": {"limit": 200, "types": ["claim", "task"], "statuses": [], "days": 7}}`
	if got != want {
		t.Fatalf("got %s", got)
	}
	f := HomeFilterFor(pyjson.NewObject("home_filter", merged), "prj_a")
	if f.Limit != 200 || f.Days != 7 || len(f.Types) != 2 {
		t.Fatalf("filter %+v", f)
	}
	if d := HomeFilterFor(pyjson.NewObject("home_filter", merged), "prj_b"); d.Limit != DefaultHomeLimit || d.Days != 0 {
		t.Fatalf("default for another project: %+v", d)
	}
	if d := HomeFilterFor(nil, "prj_a"); d.Limit != 500 {
		t.Fatalf("default limit: %+v", d)
	}
	// null restores the default; 0 means all.
	merged, err = UpdateHomeFilter(merged, hfJSON(t, `{"prj_a": null, "prj_c": {"limit": 0}}`))
	if err != nil || merged.Has("prj_a") || HomeFilterFor(pyjson.NewObject("home_filter", merged), "prj_c").Limit != 0 {
		t.Fatalf("null / all: %v %v", err, merged)
	}
	for _, bad := range []string{`{"prj": {"limit": -1}}`, `{"prj": {"limit": 1.5}}`, `{"prj": {"days": 4000}}`,
		`{"prj": {"types": "task"}}`, `{"prj": {"other": 1}}`, `[1]`, `{"prj": {"types": [1]}}`} {
		if _, err := UpdateHomeFilter(nil, hfJSON(t, bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	// The read path is lenient: a broken entry reads as the default.
	clean := HomeFilterPrefs(hfJSON(t, `{"prj_a": {"limit": "x", "days": 30}, "prj_b": 3}`))
	if string(pyjson.DumpsASCII(clean)) != `{"prj_a": {"limit": 500, "types": [], "statuses": [], "days": 30}}` {
		t.Fatalf("lenient: %s", pyjson.DumpsASCII(clean))
	}
}
