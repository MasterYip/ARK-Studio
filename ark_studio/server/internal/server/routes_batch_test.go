package server

// Multi-selection routes (Go only, routes_batch.go).

import "testing"

func TestBatchRoutesUpdateGroupAndUngroup(t *testing.T) {
	app, _ := chatScopeApp(t)
	root, a, b := trashCreate(t, app, "Root"), trashCreate(t, app, "A"), trashCreate(t, app, "B")
	for _, child := range []string{a, b} {
		if r := gpuCall(t, app, "POST", "/api/links", map[string]any{"source": child, "target": root, "relation": "part_of"}, nil); r.code != 200 {
			t.Fatalf("link: %d %s", r.code, r.raw)
		}
	}
	r := gpuCall(t, app, "POST", "/api/units/batch", map[string]any{
		"units": []any{a, b}, "patch": map[string]any{"status": "active", "importance": "p1"}, "tags_add": []any{"x"},
	}, nil)
	if r.code != 200 || len(r.body["updated"].([]any)) != 2 || r.body["batch"] == "" {
		t.Fatalf("batch update: %d %s", r.code, r.raw)
	}
	g := gpuCall(t, app, "GET", "/api/units/batch?units="+a+","+b, nil, nil)
	units, _ := g.body["units"].([]any)
	if g.code != 200 || len(units) != 2 {
		t.Fatalf("batch read: %d %s", g.code, g.raw)
	}
	for _, item := range units {
		u := item.(map[string]any)
		if u["status"] != "active" || u["importance"] != "p1" {
			t.Fatalf("unit after batch: %v", u)
		}
	}
	// Per-unit updates (the undo shape).
	r = gpuCall(t, app, "POST", "/api/units/batch", map[string]any{"updates": []any{
		map[string]any{"unit": a, "patch": map[string]any{"status": "open"}},
		map[string]any{"unit": b, "patch": map[string]any{"status": "blocked"}},
	}}, nil)
	if r.code != 200 || len(r.body["updated"].([]any)) != 2 {
		t.Fatalf("updates: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/units/batch", map[string]any{"units": []any{a}, "patch": map[string]any{"body": "x"}}, nil); r.code != 400 {
		t.Fatalf("body in a batch: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "POST", "/api/units/group", map[string]any{"units": []any{a, b}, "parent": root, "title": "Group"}, nil)
	unit, _ := r.body["unit"].(map[string]any)
	if r.code != 200 || unit == nil || unit["title"] != "Group" || len(r.body["links"].([]any)) != 3 {
		t.Fatalf("group: %d %s", r.code, r.raw)
	}
	group := unit["uid"].(string)
	if d := gpuCall(t, app, "GET", "/api/units/"+a, nil, nil); d.code != 200 ||
		d.body["unit"].(map[string]any)["links_out"].([]any)[0].(map[string]any)["target"].(map[string]any)["uid"] != group {
		t.Fatalf("A not under the group: %s", d.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/units/"+group+"/ungroup", map[string]any{"relation": "part_of"}, nil); r.code != 200 {
		t.Fatalf("ungroup: %d %s", r.code, r.raw)
	}
	if d := gpuCall(t, app, "GET", "/api/units/"+a, nil, nil); d.body["unit"].(map[string]any)["links_out"].([]any)[0].(map[string]any)["target"].(map[string]any)["uid"] != root {
		t.Fatalf("A not back under root: %s", d.raw)
	}
}

func TestBatchRoutesNeedWriteAccess(t *testing.T) {
	app, projectID := authDesktopApp(t)
	owner, viewer := sessionCookie(app, "owner"), sessionCookie(app, "vic")
	owner["X-ARK-Project"], viewer["X-ARK-Project"] = projectID, projectID
	r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "note", "title": "N"}, owner)
	if r.code != 200 {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	uid := r.body["unit"].(map[string]any)["uid"].(string)
	if r := gpuCall(t, app, "GET", "/api/units/batch?units="+uid, nil, viewer); r.code != 200 || len(r.body["units"].([]any)) != 1 {
		t.Fatalf("viewer read: %d %s", r.code, r.raw)
	}
	for _, path := range []string{"/api/units/batch", "/api/units/group", "/api/units/" + uid + "/ungroup"} {
		r := gpuCall(t, app, "POST", path, map[string]any{"units": []any{uid}, "patch": map[string]any{"status": "done"}}, viewer)
		if r.code != 403 {
			t.Fatalf("viewer POST %s: %d %s", path, r.code, r.raw)
		}
	}
	if r := gpuCall(t, app, "POST", "/api/units/batch", map[string]any{"units": []any{uid}, "patch": map[string]any{"status": "done"}}, owner); r.code != 200 {
		t.Fatalf("owner batch: %d %s", r.code, r.raw)
	}
}

func TestBatchGoOnlyRoutesListed(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range GoOnlyRoutes {
		ids[r.ID] = true
	}
	if !ids["goonly:exact:/api/units/batch"] || !ids["goonly:exact:/api/units/group"] || !ids[`goonly:regex:^/api/units/([^/]+)/ungroup$`] {
		t.Fatal("batch routes missing from GoOnlyRoutes")
	}
}
