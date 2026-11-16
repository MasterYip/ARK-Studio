package resources

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/org"
	"arkstudio/go/internal/pyjson"
)

// testdata/golden.json comes from testdata/gen_golden.py (Python's
// resources_graph and setup_layers); every graph must match byte for byte.
func golden(t *testing.T) *pyjson.Object {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	g, ok := pyjson.LoadsObject(string(raw))
	if !ok {
		t.Fatal("golden.json is not an object")
	}
	return g
}

func TestGraphsMatchPython(t *testing.T) {
	for _, item := range golden(t).Value("graphs").([]any) {
		c := item.(*pyjson.Object)
		name := pyjson.Str(c.Value("name"))
		src := c.Value("sources").(*pyjson.Object)
		if setup, ok := c.Value("setup").(string); ok {
			dir := filepath.Join("testdata", "setup", setup)
			layers, err := ReadLayers(dir)
			if err != nil {
				t.Fatalf("%s: ReadLayers: %v", name, err)
			}
			history, err := RecentHistory(dir, HistoryLimit)
			if err != nil {
				t.Fatalf("%s: RecentHistory: %v", name, err)
			}
			src.Set("layers", layers).Set("history", history)
		}
		got, err := BuildGraph(src, pyjson.Str(c.Value("viewer")))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		want := pyjson.Str(c.Value("graph"))
		if have := string(pyjson.Dumps(got)); have != want {
			at := 0
			for at < len(have) && at < len(want) && have[at] == want[at] {
				at++
			}
			lo := max(0, at-120)
			t.Errorf("%s differs at byte %d:\n go: %s\n py: %s", name, at, have[lo:min(len(have), at+120)],
				want[lo:min(len(want), at+120)])
		}
	}
}

func TestTimesAndHelpersMatchPython(t *testing.T) {
	g := golden(t)
	for _, item := range g.Value("times").([]any) {
		c := item.(*pyjson.Object)
		ts, ok := parseTime(c.Value("value"))
		want := c.Value("ts")
		if want == nil {
			if ok {
				t.Errorf("_parse_time(%s) = %v, want None", pyjson.Repr(c.Value("value")), ts)
			}
			continue
		}
		if !ok || ts != pyFloat(want) {
			t.Errorf("_parse_time(%s) = %v %v, want %v", pyjson.Repr(c.Value("value")), ts, ok, want)
			continue
		}
		if w, isStr := c.Value("iso").(string); isStr && iso(ts) != w {
			t.Errorf("_iso(%v) = %s, want %s", ts, iso(ts), w)
		}
	}
	for _, item := range g.Value("ago").([]any) {
		pair := item.([]any)
		if got := ago(pyFloat(pair[0])); got != pair[1] {
			t.Errorf("_ago(%v) = %q, want %q", pair[0], got, pair[1])
		}
	}
	for _, item := range g.Value("git_host").([]any) {
		pair := item.([]any)
		if got := GitHost(pair[0]); got != pair[1] {
			t.Errorf("git_host(%v) = %q, want %q", pair[0], got, pair[1])
		}
	}
}

// Mirrors BuildGraphTests in tests/python/server/test_resources_graph.py on
// the decoded graphs (the byte comparison above already covers them).
func TestGraphSemantics(t *testing.T) {
	graphs := map[string]*pyjson.Object{}
	for _, item := range golden(t).Value("graphs").([]any) {
		c := item.(*pyjson.Object)
		g, _ := pyjson.LoadsObject(pyjson.Str(c.Value("graph")))
		graphs[pyjson.Str(c.Value("name"))] = g
	}
	devices := func(g *pyjson.Object) map[string]*pyjson.Object {
		out := map[string]*pyjson.Object{}
		for _, d := range g.Value("devices").([]any) {
			out[pyjson.Str(d.(*pyjson.Object).Value("id"))] = d.(*pyjson.Object)
		}
		return out
	}
	state := func(d *pyjson.Object) string { return pyjson.Str(d.Value("status").(*pyjson.Object).Value("state")) }
	gpu := devices(graphs["gpu"])
	if state(gpu["gpu:lab-01"]) != "online" || state(gpu["gpu:lab-02"]) != "offline" || state(gpu["gpu:lab-03"]) != "unknown" {
		t.Error("gpu states")
	}
	if state(devices(graphs["gpu_stale"])["gpu:lab-01"]) != "stale" {
		t.Error("an old probe is stale")
	}
	bob := pyjson.Dumps(graphs["privacy_bob"])
	for _, secret := range []string{"C:/Users/alice", "Alice laptop", "notebooks"} {
		if strings.Contains(string(bob), secret) {
			t.Errorf("bob sees %q", secret)
		}
	}
	if d := devices(graphs["privacy_bob"])["host:alice-pc"]; pyjson.Str(d.Value("label")) != "Alice's device" {
		t.Errorf("private label %v", d.Value("label"))
	}
	if _, ok := devices(graphs["privacy_hidden_bob"])["host:alice-pc"]; ok {
		t.Error("hidden device visible to bob")
	}
	own := devices(graphs["privacy_alice"])["host:alice-pc"]
	if own.Value("mine") != true || state(own) != "online" {
		t.Error("owner view")
	}
}

func TestDevicePrefs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	o, err := org.Open(filepath.Join(dir, "org.sqlite3"), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err := o.Bootstrap(ctx, "", "", false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := SetDevicePref(ctx, o, "nobody", "host:x", pyjson.NewObject("hidden", true)); err == nil ||
		err.(*Error).Code != "RESOURCES_ACCOUNT_REQUIRED" {
		t.Fatalf("unknown account: %v", err)
	}
	user := org.LocalUsername
	for _, bad := range []*pyjson.Object{pyjson.NewObject(), pyjson.NewObject("hidden", "yes"),
		pyjson.NewObject("hidden", true, "x", true)} {
		if _, err := SetDevicePref(ctx, o, user, "host:x", bad); err == nil || err.(*Error).Code != "RESOURCES_INVALID" {
			t.Errorf("%s: %v", pyjson.Dumps(bad), err)
		}
	}
	if _, err := SetDevicePref(ctx, o, user, "bad/id", pyjson.NewObject("hidden", true)); err == nil {
		t.Error("bad id accepted")
	}
	got, err := SetDevicePref(ctx, o, user, "host:x", pyjson.NewObject("share_details", true))
	if err != nil || string(pyjson.Dumps(got)) != `{"device": "host:x", "hidden": false, "share_details": true}` {
		t.Fatalf("%s %v", pyjson.Dumps(got), err)
	}
	prefs, _ := DevicePrefs(ctx, o, user)
	if string(pyjson.Dumps(prefs)) != `{"host:x": {"hidden": false, "share_details": true}}` {
		t.Fatalf("prefs %s", pyjson.Dumps(prefs))
	}
	if _, err := SetDevicePref(ctx, o, user, "host:x", pyjson.NewObject("share_details", false)); err != nil {
		t.Fatal(err)
	}
	if prefs, _ = DevicePrefs(ctx, o, user); prefs.Len() != 0 {
		t.Fatalf("cleared prefs %s", pyjson.Dumps(prefs))
	}
}

func TestGPUConfigPath(t *testing.T) {
	env := map[string]string{"ARK_PRIVATE_DIR": filepath.Join("p", "q")}
	if got := GPUConfigPath(func(k string) string { return env[k] }); got != filepath.Join("p", "q", "gpu_servers.yaml") {
		t.Error(got)
	}
	env["ARK_GPU_SERVERS_CONFIG"] = " /x/y.yaml "
	if got := GPUConfigPath(func(k string) string { return env[k] }); got != "/x/y.yaml" {
		t.Error(got)
	}
}
