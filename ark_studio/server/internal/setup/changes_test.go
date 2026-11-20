package setup

import (
	"os"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
)

// testEnv is an empty setup folder and a clock that ticks one second per
// reading, so that timestamps order the writes.
func testEnv(t *testing.T) *Env {
	t.Helper()
	wall := clock.Wall
	tick := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)
	clock.Wall = func() time.Time { tick = tick.Add(time.Second); return tick }
	t.Cleanup(func() { clock.Wall = wall })
	dir := t.TempDir()
	if err := os.MkdirAll(join(dir, ProfileDir), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Env{Project: dir, DefaultOwner: "ana"}
}

func resourceRaw(id, kind, name string, details map[string]any, path string) *obj {
	d := pyjson.NewObject()
	for k, v := range details {
		d.Set(k, v)
	}
	r := pyjson.NewObject("id", id, "kind", kind, "name", name, "details", d)
	if path != "" {
		r.Set("path_or_ref", path)
	}
	return pyjson.NewObject("resources", []any{r})
}

func mustSave(t *testing.T, e *Env, v *Viewer, layer string, raw *obj, propose bool) *obj {
	t.Helper()
	r, err := e.SaveLayer(raw, v, layer, "agent:"+v.User, propose, "")
	if err != nil {
		t.Fatalf("save %s: %v", layer, err)
	}
	if !pyjson.Truthy(r.Value("ok")) {
		t.Fatalf("save %s: %s", layer, pyjson.Dumps(r))
	}
	return r
}

func layerState(t *testing.T, e *Env, layer, owner string) (string, string) {
	t.Helper()
	rec, err := e.loadLayer(layer, owner)
	if err != nil {
		t.Fatal(err)
	}
	return str(rec.Value("state")), str(rec.Value("digest"))
}

var (
	admin  = NewViewer(Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "host-a"})
	member = NewViewer(Viewer{User: "bob", CanWrite: true, HostID: "host-b"})
)

// confirmBoth sets up a project and ana's layer, both confirmed.
func confirmBoth(t *testing.T, e *Env) {
	t.Helper()
	mustSave(t, e, admin, "project", resourceRaw("repo", "repository", "Repo",
		map[string]any{"role": "primary", "access": "read-only"}, "/src/repo"), true)
	mustSave(t, e, admin, "user", resourceRaw("ark-public", "repository", "ARK public",
		map[string]any{"role": "code", "access": "read-only"}, "/src/ark"), true)
	_, pd := layerState(t, e, "project", "")
	_, ud := layerState(t, e, "user", "ana")
	if _, err := e.ConfirmLayers(admin, []LayerDigest{{"project", pd}, {"user", ud}}, "human:ana", ""); err != nil {
		t.Fatal(err)
	}
}

// An agent proposes only the project layer while ana's own layer stays
// confirmed: confirming the project layer alone works and leaves her layer
// untouched.
func TestConfirmSingleProposedLayer(t *testing.T) {
	e := testEnv(t)
	confirmBoth(t, e)
	_, userDigest := layerState(t, e, "user", "ana")
	mustSave(t, e, admin, "project", resourceRaw("agent-team-tasks", "task_space", "agent-team-tasks",
		map[string]any{"kind": "agent-team-folder", "access": "read-write"}, "/tasks"), true)
	if _, err := e.AddLocation(admin, "u:ana/ark-public", "/home/ana/ark", "agent:ana", "host-c", "", ""); err != nil {
		t.Fatal(err)
	}
	ps, pd := layerState(t, e, "project", "")
	us, _ := layerState(t, e, "user", "ana")
	if ps != "proposed" || us != "confirmed" {
		t.Fatalf("states: project %s, user %s", ps, us)
	}
	// The user layer has nothing pending; a request for both is refused as a
	// whole (its digest is current, but a wrong project digest fails all).
	if _, err := e.ConfirmLayers(admin, []LayerDigest{{"project", "nope"}, {"user", userDigest}}, "human:ana", ""); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("stale digest: %v", err)
	}
	if s, _ := layerState(t, e, "project", ""); s != "proposed" {
		t.Fatalf("a refused request must not confirm anything: %s", s)
	}
	out, err := e.ConfirmLayers(admin, []LayerDigest{{"project", pd}}, "human:ana", "")
	if err != nil || len(out) != 1 {
		t.Fatalf("confirm project: %v %v", out, err)
	}
	if s, _ := layerState(t, e, "project", ""); s != "confirmed" {
		t.Fatalf("project: %s", s)
	}
	if s, d := layerState(t, e, "user", "ana"); s != "confirmed" || d != userDigest {
		t.Fatalf("user layer changed: %s %s", s, d)
	}
}

// Permissions: the project layer needs a project admin, a user layer only
// its owner; a member confirms their own layer while the project change
// waits for an admin.
func TestConfirmLayersPermissions(t *testing.T) {
	e := testEnv(t)
	confirmBoth(t, e)
	mustSave(t, e, member, "project", resourceRaw("scratch", "storage", "Scratch", map[string]any{"kind": "artifacts"}, "/scratch"), true)
	mustSave(t, e, member, "user", resourceRaw("laptop-env", "environment", "venv", map[string]any{"kind": "venv"}, ""), true)
	_, pd := layerState(t, e, "project", "")
	_, bd := layerState(t, e, "user", "bob")
	_, err := e.ConfirmLayers(member, []LayerDigest{{"project", pd}, {"user", bd}}, "human:bob", "")
	if se, ok := err.(*Error); !ok || se.Code != "SETUP_ADMIN_REQUIRED" {
		t.Fatalf("member confirming project: %v", err)
	}
	if s, _ := layerState(t, e, "user", "bob"); s != "proposed" {
		t.Fatalf("refused request confirmed bob's layer: %s", s)
	}
	if _, err := e.ConfirmLayers(member, []LayerDigest{{"user", bd}}, "human:bob", ""); err != nil {
		t.Fatalf("member confirming own layer: %v", err)
	}
	if s, _ := layerState(t, e, "project", ""); s != "proposed" {
		t.Fatalf("project: %s", s)
	}
	if _, err := e.ConfirmLayers(admin, []LayerDigest{{"project", pd}}, "agent:ana", ""); err == nil {
		t.Fatal("an agent confirmed")
	}
	if _, err := e.ConfirmLayers(admin, []LayerDigest{{"project", pd}, {"project", pd}}, "human:ana", ""); err == nil {
		t.Fatal("duplicate layer accepted")
	}
	if _, err := e.ConfirmLayers(admin, []LayerDigest{{"project", pd}}, "human:ana", ""); err != nil {
		t.Fatalf("admin confirming project: %v", err)
	}
}

// One propose call sends every changed layer; resource writes with
// propose: true do the same through the server.
func TestProposeAll(t *testing.T) {
	e := testEnv(t)
	if _, _, err := e.ProposeAll(admin, "agent:ana", true); err == nil {
		t.Fatal("nothing recorded: want SETUP_UNSET")
	}
	confirmBoth(t, e)
	mustSave(t, e, admin, "project", resourceRaw("tasks", "task_space", "Tasks", map[string]any{"kind": "ark"}, ""), false)
	mustSave(t, e, admin, "user", resourceRaw("scratch", "storage", "Scratch", map[string]any{"kind": "artifacts"}, "/s"), false)
	layers, warnings, err := e.ProposeAll(admin, "agent:ana", true)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("propose all: %v %v", err, warnings)
	}
	pending := Pending(layers)
	if len(layers) != 2 || len(pending) != 2 {
		t.Fatalf("layers %s pending %s", pyjson.Dumps(layers), pyjson.Dumps(pending))
	}
	for _, l := range []struct{ layer, owner string }{{"project", ""}, {"user", "ana"}} {
		if s, _ := layerState(t, e, l.layer, l.owner); s != "proposed" {
			t.Fatalf("%s: %s", l.layer, s)
		}
	}
	// Again: nothing new is proposed, both stay pending.
	layers, _, _ = e.ProposeAll(admin, "agent:ana", true)
	for _, x := range layers {
		if pyjson.Truthy(x.(*obj).Value("proposed")) {
			t.Fatalf("re-proposed: %s", pyjson.Dumps(x))
		}
	}
	// The legacy server token has no user layer.
	layers, _, err = e.ProposeAll(LegacyViewer(""), "agent", true)
	if err != nil || len(layers) != 1 || layers[0].(*obj).Value("layer") != "project" {
		t.Fatalf("legacy: %v %s", err, pyjson.Dumps(layers))
	}
	if _, _, err := e.ProposeAll(NewViewer(Viewer{User: "vic"}), "agent:vic", true); err == nil {
		t.Fatal("read-only viewer proposed")
	}
}

func changesOf(t *testing.T, e *Env, layer, owner string) *obj {
	t.Helper()
	rec, err := e.loadLayer(layer, owner)
	if err != nil {
		t.Fatal(err)
	}
	return e.Changes(rec)
}

func summarize(c *obj) []string {
	out := []string{str(c.Value("baseline"))}
	for _, x := range listOr(c.Value("resources")) {
		r := x.(*obj)
		line := str(r.Value("change")) + " " + str(r.Value("key"))
		for _, f := range listOr(r.Value("fields")) {
			fo := f.(*obj)
			line += " " + str(fo.Value("field")) + ":" + str(fo.Value("old")) + ">" + str(fo.Value("new"))
		}
		out = append(out, line)
	}
	for _, k := range []string{"fields", "policies"} {
		for _, f := range listOr(c.Value(k)) {
			fo := f.(*obj)
			out = append(out, k+" "+str(fo.Value("field"))+":"+str(fo.Value("old"))+">"+str(fo.Value("new")))
		}
	}
	for _, x := range listOr(c.Value("locations")) {
		l := x.(*obj)
		out = append(out, "location "+str(l.Value("change"))+" "+str(l.Value("resource"))+" "+str(l.Value("path_or_ref")))
	}
	return out
}

// The diff against the last confirmed version: added, changed fields with
// old and new values, removed, policies, project fields and locations.
func TestChangesSinceConfirmation(t *testing.T) {
	e := testEnv(t)
	if c := changesOf(t, e, "project", ""); c.Value("baseline") != "none" || c.Value("count") != int64(0) {
		t.Fatalf("never confirmed: %s", pyjson.Dumps(c))
	}
	confirmBoth(t, e)
	mustSave(t, e, admin, "project", resourceRaw("old", "storage", "Old", map[string]any{"kind": "logs"}, ""), true)
	_, pd := layerState(t, e, "project", "")
	if _, err := e.ConfirmLayers(admin, []LayerDigest{{"project", pd}}, "human:ana", ""); err != nil {
		t.Fatal(err)
	}
	if c := changesOf(t, e, "project", ""); c.Value("baseline") != "confirmed" || c.Value("count") != int64(0) {
		t.Fatalf("just confirmed: %s", pyjson.Dumps(c))
	}
	// Agent: add a task space, change the repo's access, remove "old", set
	// the objective and a policy.
	mustSave(t, e, admin, "project", resourceRaw("agent-team-tasks", "task_space", "agent-team-tasks",
		map[string]any{"kind": "agent-team-folder", "access": "read-write"}, "/tasks"), false)
	mustSave(t, e, admin, "project", resourceRaw("repo", "repository", "Repo",
		map[string]any{"role": "primary", "access": "read-write"}, ""), false)
	mustSave(t, e, admin, "project", pyjson.NewObject("delete", []any{"old"}), false)
	mustSave(t, e, admin, "project", pyjson.NewObject("objective", "Ship the setup fix",
		"policies", pyjson.NewObject("push", "deny")), true)
	got := strings.Join(summarize(changesOf(t, e, "project", "")), "\n")
	want := strings.Join([]string{
		"confirmed",
		"changed project/repo access:read-only>read-write",
		"added project/agent-team-tasks",
		"removed project/old",
		"fields objective:>Ship the setup fix",
		"policies push:ask>deny",
		"location added project/agent-team-tasks /tasks",
	}, "\n")
	if got != want {
		t.Fatalf("project changes:\n%s\nwant\n%s", got, want)
	}
	// ana's layer is still confirmed; a path added on another machine shows.
	if _, err := e.AddLocation(admin, "u:ana/ark-public", "/home/ana/ark", "agent:ana", "host-c", "", ""); err != nil {
		t.Fatal(err)
	}
	got = strings.Join(summarize(changesOf(t, e, "user", "ana")), "\n")
	if got != "confirmed\nlocation added u:ana/ark-public /home/ana/ark" {
		t.Fatalf("user changes:\n%s", got)
	}
	// After confirming, nothing is new.
	_, pd = layerState(t, e, "project", "")
	if _, err := e.ConfirmLayers(admin, []LayerDigest{{"project", pd}}, "human:ana", ""); err != nil {
		t.Fatal(err)
	}
	if c := changesOf(t, e, "project", ""); c.Value("count") != int64(0) {
		t.Fatalf("after confirm: %s", pyjson.Dumps(c))
	}
}

// Records confirmed before snapshots existed: a write carries the
// confirmed profile forward; without one, the last confirmation time from
// history marks what was added since.
func TestChangesWithoutSnapshot(t *testing.T) {
	e := testEnv(t)
	confirmBoth(t, e)
	strip := func(layer, owner string) {
		rec := readJSON(e.layerTarget(layer, owner))
		rec.Delete(SnapshotKey)
		if err := e.write(e.layerTarget(layer, owner), rec, pyjson.NewObject("event", "test")); err != nil {
			t.Fatal(err)
		}
	}
	strip("project", "")
	// Confirmed, no snapshot: carried forward on the next write.
	mustSave(t, e, admin, "project", resourceRaw("repo", "repository", "Repo",
		map[string]any{"role": "primary", "access": "read-write"}, ""), true)
	got := strings.Join(summarize(changesOf(t, e, "project", "")), "\n")
	if got != "confirmed\nchanged project/repo access:read-only>read-write" {
		t.Fatalf("carried snapshot:\n%s", got)
	}
	// Already changed without a snapshot: history marks the additions.
	strip("project", "")
	mustSave(t, e, admin, "project", resourceRaw("tasks", "task_space", "Tasks", map[string]any{"kind": "ark"}, "/tasks"), true)
	got = strings.Join(summarize(changesOf(t, e, "project", "")), "\n")
	if got != "history\nadded project/tasks\nlocation added project/tasks /tasks" {
		t.Fatalf("history fallback:\n%s", got)
	}
}
