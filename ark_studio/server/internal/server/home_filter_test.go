package server

// "Home shows" (prefs.home_filter): GET /api/project/overview draws the
// signed-in person's filter for the project, query parameters override it,
// and the default is the newest 500 nodes.

import (
	"fmt"
	"testing"
)

func TestOverviewFollowsHomeFilter(t *testing.T) {
	app, p := chatScopeApp(t)
	for i := 0; i < 6; i++ {
		if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": fmt.Sprintf("t%d", i)}, nil); r.code != 200 {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
	}
	graph := func(target string) map[string]any {
		t.Helper()
		r := gpuCall(t, app, "GET", target, nil, nil)
		if r.code != 200 {
			t.Fatalf("%s: %d %s", target, r.code, r.raw)
		}
		return r.body["graph"].(map[string]any)
	}
	g := graph("/api/project/overview")
	filter := g["filter"].(map[string]any)
	if filter["limit"] != float64(500) || g["limit"] != float64(500) || g["total"] == nil || g["matched"] == nil {
		t.Fatalf("default filter: %v", g)
	}
	total := g["total"].(float64)
	r := gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": map[string]any{"home_filter": map[string]any{
		p.ID(): map[string]any{"limit": 2, "types": []string{"task"}, "statuses": []string{}, "days": 0}}}}, nil)
	if r.code != 200 {
		t.Fatalf("save prefs: %d %s", r.code, r.raw)
	}
	prefs := r.body["profile"].(map[string]any)["prefs"].(map[string]any)
	if prefs["home_filter"].(map[string]any)[p.ID()].(map[string]any)["limit"] != float64(2) {
		t.Fatalf("prefs echoed: %v", prefs)
	}
	g = graph("/api/project/overview")
	if len(g["nodes"].([]any)) != 2 || g["total"] != total || g["matched"] != float64(6) {
		t.Fatalf("saved filter applied: %v", g)
	}
	// graph_limit=0 (all) overrides the saved limit; the saved types stay.
	g = graph("/api/project/overview?graph_limit=0")
	if len(g["nodes"].([]any)) != 6 {
		t.Fatalf("query overrides the limit: %d", len(g["nodes"].([]any)))
	}
	if g = graph("/api/project/overview?graph_limit=0&graph_types=task,note"); len(g["nodes"].([]any)) != int(total) {
		t.Fatalf("and the types: %d of %v", len(g["nodes"].([]any)), total)
	}
	g = graph("/api/project/overview?graph_limit=3")
	if len(g["nodes"].([]any)) != 3 {
		t.Fatalf("graph_limit=3: %d", len(g["nodes"].([]any)))
	}
	if r := gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": map[string]any{"home_filter": map[string]any{
		p.ID(): map[string]any{"limit": -4}}}}, nil); r.code != 400 {
		t.Fatalf("invalid filter: %d %s", r.code, r.raw)
	}
	gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": map[string]any{"home_filter": map[string]any{p.ID(): nil}}}, nil)
	if g = graph("/api/project/overview"); g["limit"] != float64(500) {
		t.Fatalf("null restores the default: %v", g["limit"])
	}
}
