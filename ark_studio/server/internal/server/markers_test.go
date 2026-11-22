package server

// Node marker groups (Go only): settings.markers over PATCH
// /api/project/settings, GET /api/agent/v1/markers, and the marker rules on
// agent writes.

import (
	"context"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func markerTagsOf(unit map[string]any) []string {
	var out []string
	for _, tag := range unit["tags"].([]any) {
		if s := tag.(string); len(s) > 7 && s[:7] == "marker:" {
			out = append(out, s)
		}
	}
	return out
}

func TestProjectMarkerSettings(t *testing.T) {
	app, _ := activityApp(t, "Markers")
	r := gpuCall(t, app, "GET", "/api/project/settings", nil, nil)
	if _, has := r.body["settings"].(map[string]any)["markers"]; r.code != 200 || has {
		t.Fatalf("markers absent until set: %d %s", r.code, r.raw)
	}
	groups := map[string]any{"groups": []any{map[string]any{"id": "mood", "name": "Mood", "exclusive": true, "markers": []any{
		map[string]any{"id": "mood-happy", "glyph": "☺", "color": "#22c55e", "label": ""},
		map[string]any{"id": "mood-sad", "glyph": "☹", "color": "#64748b", "label": "Sad"}}}}}
	r = gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"markers": groups}, nil)
	if r.code != 200 {
		t.Fatalf("PATCH: %d %s", r.code, r.raw)
	}
	stored := r.body["settings"].(map[string]any)["markers"].(map[string]any)["groups"].([]any)
	if len(stored) != 1 || stored[0].(map[string]any)["exclusive"] != true {
		t.Fatalf("stored %v", stored)
	}
	bad := map[string]any{"groups": []any{map[string]any{"id": "x", "markers": []any{map[string]any{"id": "a", "glyph": "a", "color": "blue"}}}}}
	if r := gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"markers": bad}, nil); r.code != 400 {
		t.Fatalf("bad color: %d %s", r.code, r.raw)
	}
	// The project's groups apply to writes.
	r = gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "note", "title": "A",
		"tags": []any{"marker:mood-happy", "marker:mood-sad", "marker:star-red", "marker:star-blue"}}, nil)
	if got := markerTagsOf(r.body["unit"].(map[string]any)); len(got) != 3 || got[0] != "marker:mood-sad" {
		t.Fatalf("custom rules: %v", got)
	}
	r = gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"markers": nil}, nil)
	if _, has := r.body["settings"].(map[string]any)["markers"]; r.code != 200 || has {
		t.Fatalf("null resets: %d %s", r.code, r.raw)
	}
}

func TestAgentMarkers(t *testing.T) {
	app, _ := chatScopeApp(t)
	auth := map[string]string{"Authorization": "Bearer " + app.AgentToken(), "X-Agent-Name": "marker"}
	r := gpuCall(t, app, "GET", "/api/agent/v1/markers", nil, auth)
	if r.code != 200 || r.body["custom"] != false {
		t.Fatalf("GET markers: %d %s", r.code, r.raw)
	}
	star := r.body["groups"].([]any)[0].(map[string]any)
	if star["id"] != "star" || star["exclusive"] != true || star["markers"].([]any)[0].(map[string]any)["tag"] != "marker:star-yellow" {
		t.Fatalf("star group: %v", star)
	}
	// Agent writes follow the rules: one star, the last written.
	r = gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "T",
		"tags": []any{"marker:star-yellow", "marker:star-blue", "marker:star-blue", "marker:question"}}, auth)
	if r.code != 200 {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	unit := r.body["unit"].(map[string]any)
	if got := markerTagsOf(unit); len(got) != 2 || got[0] != "marker:question" || got[1] != "marker:star-blue" {
		t.Fatalf("agent create: %v", got)
	}
	uid := unit["uid"].(string)
	r = gpuCall(t, app, "POST", "/api/agent/v1/units/"+uid, map[string]any{"tags": []any{"marker:star-blue", "marker:star-green"}}, auth)
	if got := markerTagsOf(r.body["unit"].(map[string]any)); r.code != 200 || len(got) != 1 || got[0] != "marker:star-green" {
		t.Fatalf("agent update: %d %v", r.code, got)
	}
	// A read-scope token reads the groups but cannot write markers.
	tok, err := app.Org.CreateAgentToken(context.Background(), "local", pyjson.NewObject("scope", "read"))
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]string{"Authorization": "Bearer " + pyjson.Str(tok.Value("token"))}
	if r := gpuCall(t, app, "GET", "/api/agent/v1/markers", nil, read); r.code != 200 {
		t.Fatalf("read token GET: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/agent/v1/units/"+uid, map[string]any{"tags": []any{"marker:star-red"}}, read); r.code != 403 {
		t.Fatalf("read token write: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/agent/v1/markers", nil, nil); r.code != 401 {
		t.Fatalf("no token: %d", r.code)
	}
}
