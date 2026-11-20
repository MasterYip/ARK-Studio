package server

// T-0034: GET /api/projects carries the person's own activity per project.

import (
	"context"
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
)

func activityApp(t *testing.T, names ...string) (*App, map[string]string) {
	t.Helper()
	root := t.TempDir()
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	for _, name := range names {
		if r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": name}, nil); r.code != 200 {
			t.Fatalf("create project: %d %s", r.code, r.raw)
		}
	}
	paths := map[string]string{}
	for _, p := range app.Projects.ListProjects() {
		paths[pyjson.Str(p.Value("name"))] = pyjson.Str(p.Value("path"))
	}
	return app, paths
}

// addActivity writes rows straight into a project database (another
// connection, like the Python CLI would), replacing what creation logged.
func addActivity(t *testing.T, folder string, rows ...[2]string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(folder, "ark.sqlite3"))+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("DELETE FROM activity"); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec("INSERT INTO activity(at,actor,action,unit_id,detail_json) VALUES(?,?,'unit.update',NULL,'{}')", r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func stamp(t time.Time) string { return clock.Format(t) }

func projectsByName(t *testing.T, app *App) map[string]map[string]any {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/projects", nil, nil)
	if r.code != 200 {
		t.Fatalf("GET /api/projects: %d %s", r.code, r.raw)
	}
	out := map[string]map[string]any{}
	list, _ := r.body["projects"].([]any)
	for _, item := range list {
		m := item.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func TestActivityScore(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	cases := []struct {
		times []time.Time
		want  float64
	}{
		{nil, 0},
		{[]time.Time{now}, 1},
		{[]time.Time{now.Add(time.Hour)}, 1}, // a future stamp counts as now
		{[]time.Time{now.Add(-7 * day)}, 0.5},
		{[]time.Time{now, now.Add(-14 * day)}, 1.25},
		{[]time.Time{now.Add(-31 * day)}, 0}, // outside the 30-day window
	}
	for i, c := range cases {
		if got := activityScore(c.times, now); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("case %d: score %v, want %v", i, got, c.want)
		}
	}
}

func TestProjectsListCarriesMyActivity(t *testing.T) {
	app, paths := activityApp(t, "Busy", "Quiet", "Others")
	now := time.Now().UTC()
	addActivity(t, paths["Busy"],
		[2]string{stamp(now.Add(-time.Hour)), "local"},
		[2]string{stamp(now.Add(-2 * time.Hour)), "local"},
		[2]string{stamp(now.Add(-8 * 24 * time.Hour)), "local"})
	addActivity(t, paths["Quiet"], [2]string{stamp(now.Add(-40 * 24 * time.Hour)), "local"})
	addActivity(t, paths["Others"], [2]string{stamp(now.Add(-time.Minute)), "ana"},
		[2]string{stamp(now.Add(-time.Minute)), "agent"})

	got := projectsByName(t, app)
	busy, quiet, others := got["Busy"], got["Quiet"], got["Others"]
	if s, _ := busy["my_activity"].(float64); s < 2.4 || s > 2.5 {
		t.Errorf("Busy my_activity = %v, want ≈ 2.45", busy["my_activity"])
	}
	if busy["my_last_active_at"] != stamp(now.Add(-time.Hour)) {
		t.Errorf("Busy my_last_active_at = %v", busy["my_last_active_at"])
	}
	// Older than the window: no score, but the last time is still known.
	if quiet["my_activity"] != 0.0 || quiet["my_last_active_at"] != stamp(now.Add(-40*24*time.Hour)) {
		t.Errorf("Quiet = %v / %v", quiet["my_activity"], quiet["my_last_active_at"])
	}
	// Other people's (and agents') work does not count.
	if others["my_activity"] != 0.0 || others["my_last_active_at"] != nil {
		t.Errorf("Others = %v / %v", others["my_activity"], others["my_last_active_at"])
	}
	if _, has := others["my_last_active_at"]; !has {
		t.Error("my_last_active_at must be present (null) when there is none")
	}

	// Another person sees their own figures.
	c := &reqCtx{app: app, ctx: context.Background(), user: "ana"}
	items := []any{pyjson.NewObject("name", "Others", "path", paths["Others"]),
		pyjson.NewObject("name", "Busy", "path", paths["Busy"])}
	c.withMyActivity(items)
	if s := items[0].(*pyjson.Object).Value("my_activity"); s != 1.0 {
		t.Errorf("ana on Others: %v", s)
	}
	if s := items[1].(*pyjson.Object).Value("my_activity"); s != 0.0 {
		t.Errorf("ana on Busy: %v", s)
	}
}

func TestProjectActivityCache(t *testing.T) {
	app, paths := activityApp(t, "Cached")
	folder := paths["Cached"]
	now := time.Now().UTC()
	addActivity(t, folder, [2]string{stamp(now), "local"})
	key := activityKey{filepath.Join(folder, "ark.sqlite3"), "local"}
	first := app.activityFor(context.Background(), folder, "local", now)
	if first == nil || len(first.times) != 1 {
		t.Fatalf("first read: %+v", first)
	}
	if again := app.activityFor(context.Background(), folder, "local", now); again != first {
		t.Error("an unchanged database must be served from the cache")
	}
	// The cache is keyed by the files' signature: a change is read again.
	app.activity.get(key).sig = "stale"
	if fresh := app.activityFor(context.Background(), folder, "local", now); fresh == first {
		t.Error("a changed signature must be read again")
	}
	// A database untouched for longer than the window is not opened.
	old := now.Add(-45 * 24 * time.Hour)
	for _, f := range []string{"ark.sqlite3", "ark.sqlite3-wal"} {
		os.Chtimes(filepath.Join(folder, f), old, old) //nolint:errcheck // the WAL may not exist
	}
	if e := app.activityFor(context.Background(), folder, "local", now); e == nil || !e.skipped || len(e.times) != 0 {
		t.Errorf("stale database: %+v", e)
	}
	if e := app.activityFor(context.Background(), filepath.Join(folder, "missing"), "local", now); e != nil {
		t.Errorf("missing database: %+v", e)
	}
}

func TestProfileProjectPrefs(t *testing.T) {
	app, _ := activityApp(t, "Prefs")
	set := func(v any) gpuReply {
		return gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": map[string]any{"projects": v}}, nil)
	}
	r := set(map[string]any{"pinned": []any{"b", "a", "b", "c"}, "hidden": []any{"c"}})
	if r.code != 200 {
		t.Fatalf("PATCH /api/me: %d %s", r.code, r.raw)
	}
	prefs := r.body["profile"].(map[string]any)["prefs"].(map[string]any)["projects"].(map[string]any)
	pinned, hidden := prefs["pinned"].([]any), prefs["hidden"].([]any)
	if len(pinned) != 2 || pinned[0] != "b" || pinned[1] != "a" || len(hidden) != 1 || hidden[0] != "c" {
		t.Errorf("stored prefs.projects = %v", prefs)
	}
	if r := set(map[string]any{"pinned": "a"}); r.code != 400 {
		t.Errorf("invalid pinned: %d", r.code)
	}
	if r := set(map[string]any{"order": []any{}}); r.code != 400 {
		t.Errorf("unknown member: %d", r.code)
	}
	r = set(nil)
	if r.code != 200 {
		t.Fatalf("clear: %d %s", r.code, r.raw)
	}
	if _, has := r.body["profile"].(map[string]any)["prefs"].(map[string]any)["projects"]; has {
		t.Error("null must clear prefs.projects")
	}
}
