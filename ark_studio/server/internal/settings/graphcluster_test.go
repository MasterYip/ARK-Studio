package settings

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestGraphClusterAbsentByDefault(t *testing.T) {
	got := string(pyjson.Dumps(NormalizeGraph(nil)))
	want := `{"repulsion": 3286.0639999999985, "distance": 0.0, "gravity": 0.011506123058542407, "damping": 0.3}`
	if got != want {
		t.Fatalf("defaults changed: %s", got)
	}
}

func TestGraphClusterPatchAndRead(t *testing.T) {
	m := gdLoad(t, `{"settings": {}}`)
	next, err := SettingsPatch(m, gdLoad(t, `{"graph": {"cluster": true, "cluster_attract": 0.8, "cluster_repel": 0, "cluster_by": "tag"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	g := next.Value("graph").(*pyjson.Object)
	if g.Value("cluster") != true || g.Value("cluster_attract") != 0.8 || g.Value("cluster_by") != "tag" {
		t.Fatalf("stored %s", pyjson.Dumps(g))
	}
	if n, _ := g.Value("cluster_repel").(float64); n != 0 || !g.Has("cluster_repel") {
		t.Fatalf("cluster_repel %v", g.Value("cluster_repel"))
	}
	// Lenient read keeps valid values and drops broken ones.
	read := NormalizeGraph(gdLoad(t, `{"cluster": "yes", "cluster_attract": 2, "cluster_repel": 0.25, "cluster_by": "line"}`))
	if read.Has("cluster") || read.Has("cluster_attract") || read.Has("cluster_by") || read.Value("cluster_repel") != 0.25 {
		t.Fatalf("lenient read %s", pyjson.Dumps(read))
	}
	for body, msg := range map[string]string{
		`{"graph": {"cluster": 1}}`:           "cluster must be true or false",
		`{"graph": {"cluster_attract": 1.5}}`: "cluster_attract must be between 0 and 1",
		`{"graph": {"cluster_repel": "x"}}`:   "cluster_repel must be a number",
		`{"graph": {"cluster_by": "line"}}`:   "cluster_by must be one of type, status, tag",
	} {
		if _, err := SettingsPatch(m, gdLoad(t, body), nil); err == nil || err.Error() != msg {
			t.Errorf("%s: got %v, want %q", body, err, msg)
		}
	}
}

func TestGraphClusterPersonal(t *testing.T) {
	merged, err := UpdatePersonalAppearance(nil, gdLoad(t,
		`{"graph": {"repulsion": 3000, "distance": 0, "gravity": 0.01, "damping": 0.3, "cluster": true, "cluster_by": "status"}}`))
	if err != nil {
		t.Fatal(err)
	}
	g := merged.Value("graph").(*pyjson.Object)
	if g.Value("cluster") != true || g.Value("cluster_by") != "status" {
		t.Fatalf("personal %s", pyjson.Dumps(g))
	}
	back := PersonalAppearance(merged).Value("graph").(*pyjson.Object)
	if back.Value("cluster") != true {
		t.Fatalf("read back %s", pyjson.Dumps(back))
	}
}
