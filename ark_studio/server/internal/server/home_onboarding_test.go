package server

// T-0030 (UI line A, Home and onboarding), Go-only behaviour:
//   - a blank project has no built-in topics, before or after GET /api/topics
//     (paper, demo and tutorial projects keep them; existing topics stay);
//   - the overview KPIs and Team load leave topics out;
//   - the overview's "agents" member reports agent writes;
//   - the example seeds' dates are relative to today.
// See tests/python/contract/go_only.py (FIXES, AUGMENTED).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

func homeApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	app, err := NewApp(Config{Root: filepath.Join(root, "projects"), NoDemo: true,
		SessionsRoot: filepath.Join(root, "sessions")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.Bind("127.0.0.1", 8765)
	return app
}

func homeCreate(t *testing.T, app *App, name, template string) *project.Project {
	t.Helper()
	body := map[string]any{"name": name}
	if template != "" {
		body["template"] = template
	}
	if r := gpuCall(t, app, "POST", "/api/projects", body, nil); r.code != 200 {
		t.Fatalf("create %s: %d %s", name, r.code, r.raw)
	}
	p, err := (&reqCtx{app: app, ctx: context.Background()}).currentProject()
	if err != nil || p == nil {
		t.Fatalf("no current project: %v", err)
	}
	return p
}

func topicTitles(t *testing.T, app *App) []string {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/topics", nil, nil)
	if r.code != 200 {
		t.Fatalf("GET /api/topics: %d %s", r.code, r.raw)
	}
	var out []string
	items, _ := r.body["items"].([]any)
	for _, item := range items {
		out = append(out, item.(map[string]any)["title"].(string))
	}
	return out
}

func topicUnits(t *testing.T, p *project.Project) int {
	t.Helper()
	var n int64
	stats, err := p.Store.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if byType, ok := stats.Value("by_type").(*pyjson.Object); ok {
		n, _ = byType.Value("topic").(int64)
	}
	return int(n)
}

func TestBlankProjectHasNoBuiltInTopics(t *testing.T) {
	app := homeApp(t)
	p := homeCreate(t, app, "Blank study", "")
	if n := topicUnits(t, p); n != 0 {
		t.Fatalf("blank project starts with %d topics", n)
	}
	for i := 0; i < 2; i++ {
		if got := topicTitles(t, app); len(got) != 0 {
			t.Fatalf("GET /api/topics #%d on a blank project: %v", i+1, got)
		}
	}
	if n := topicUnits(t, p); n != 0 {
		t.Fatalf("GET /api/topics created %d topics in a blank project", n)
	}
	// A topic of the user's own is listed as usual.
	if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "topic", "title": "Mine"}, nil); r.code != 200 {
		t.Fatalf("create topic: %d %s", r.code, r.raw)
	}
	if got := topicTitles(t, app); len(got) != 1 || got[0] != "Mine" {
		t.Fatalf("topics after creating one: %v", got)
	}
	// Built-in topics a blank project already has (made by an earlier
	// version) are kept and listed.
	if _, err := p.Store.EnsureDefaultTopics(context.Background(), store.Who{}); err != nil {
		t.Fatal(err)
	}
	if got := topicTitles(t, app); len(got) != 5 {
		t.Fatalf("existing built-in topics not listed: %v", got)
	}
}

func TestPaperAndDemoKeepBuiltInTopics(t *testing.T) {
	app := homeApp(t)
	for _, template := range []string{"paper", "demo"} {
		homeCreate(t, app, "With topics "+template, template)
		got := topicTitles(t, app)
		if len(got) != 4 || got[0] != "Story" || got[3] != "Experiment" {
			t.Fatalf("%s: built-in topics %v", template, got)
		}
	}
	// The per-user tutorial project too.
	if r := gpuCall(t, app, "POST", "/api/projects/open", map[string]any{"id": project.DemoID}, nil); r.code != 200 {
		t.Fatalf("open tutorial: %d %s", r.code, r.raw)
	}
	got := topicTitles(t, app)
	story := false
	for _, title := range got {
		story = story || title == "Story"
	}
	if !story {
		t.Fatalf("tutorial topics %v", got)
	}
}

func overviewOf(t *testing.T, app *App) map[string]any {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/project/overview", nil, nil)
	if r.code != 200 {
		t.Fatalf("overview: %d %s", r.code, r.raw)
	}
	return r.body
}

func kpiNow(ov map[string]any, key string) float64 {
	k := ov["kpis"].(map[string]any)[key].(map[string]any)
	return k["now"].(float64)
}

func TestOverviewLeavesTopicsOutOfKPIs(t *testing.T) {
	app := homeApp(t)
	p := homeCreate(t, app, "KPI study", "paper")
	before := overviewOf(t, app)
	topicTitles(t, app) // creates the 4 built-in topics (status active)
	if n := topicUnits(t, p); n != 4 {
		t.Fatalf("paper topics: %d", n)
	}
	after := overviewOf(t, app)
	for _, key := range []string{"active", "open", "blocked", "review", "overdue"} {
		if kpiNow(before, key) != kpiNow(after, key) {
			t.Errorf("KPI %s counts topics: %v -> %v", key, kpiNow(before, key), kpiNow(after, key))
		}
	}
	un := func(ov map[string]any) float64 { return ov["unassigned"].(map[string]any)["open"].(float64) }
	if un(before) != un(after) {
		t.Errorf("Team load counts topics: %v -> %v", un(before), un(after))
	}
	// The map backdrop still has every node (the Map shows topics).
	nodes := func(ov map[string]any) int { return len(ov["graph"].(map[string]any)["nodes"].([]any)) }
	if nodes(after) != nodes(before)+4 {
		t.Errorf("graph nodes %d -> %d", nodes(before), nodes(after))
	}
}

func TestOverviewAgents(t *testing.T) {
	app := homeApp(t)
	p := homeCreate(t, app, "Agent study", "")
	agents := overviewOf(t, app)["agents"].(map[string]any)
	if agents["first_at"] != nil || len(agents["recent"].([]any)) != 0 {
		t.Fatalf("no agent wrote yet: %v", agents)
	}
	ctx := context.Background()
	for i, actor := range []string{"agent:local/claude-code", "agent:local/claude-code", "human:ui", "agent:local/codex"} {
		if _, err := p.Store.CreateUnit(ctx, pyjson.NewObject("type", "note", "title", "n"+string(rune('a'+i))), actor, store.Who{}); err != nil {
			t.Fatal(err)
		}
	}
	agents = overviewOf(t, app)["agents"].(map[string]any)
	if agents["first_at"] == nil {
		t.Fatalf("first_at not set: %v", agents)
	}
	recent := agents["recent"].([]any)
	if len(recent) != 2 {
		t.Fatalf("recent agents: %v", recent)
	}
	writes := map[string]float64{}
	for _, item := range recent {
		m := item.(map[string]any)
		writes[m["actor"].(string)] = m["writes"].(float64)
		if m["last_at"] == nil {
			t.Errorf("no last_at: %v", m)
		}
	}
	if writes["agent:local/claude-code"] != 2 || writes["agent:local/codex"] != 1 {
		t.Fatalf("writes per agent: %v", writes)
	}
}

func TestSeedDatesFollowToday(t *testing.T) {
	day := func(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
	if n := seedShift("demo", day("2026-10-20")); n != 0 {
		t.Fatalf("anchor day shift %d", n)
	}
	if n := seedShift("demo", day("2027-01-01")); n != 73 {
		t.Fatalf("shift %d", n)
	}
	if n := seedShift("blank", day("2027-01-01")); n != 0 {
		t.Fatalf("blank seed shift %d", n)
	}
	// PredActor opened months after the seed was written still has only a
	// few items past due.
	saved := clock.Wall
	clock.Wall = func() time.Time { return time.Date(2027, 6, 1, 9, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { clock.Wall = saved })
	app := homeApp(t)
	homeCreate(t, app, "Example", "demo")
	ov := overviewOf(t, app)
	if n := kpiNow(ov, "overdue"); n < 1 || n > 9 {
		t.Fatalf("PredActor overdue %v on 2027-06-01", n)
	}
	if ov["today"] != "2027-06-01" {
		t.Fatalf("today %v", ov["today"])
	}
}

func seedShift(key string, now time.Time) int { return project.SeedShiftDays(key, now) }
