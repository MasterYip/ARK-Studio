package server

// Recycle bin routes (Go only, routes_trash.go): GET /api/trash,
// DELETE /api/trash/{ref}, DELETE /api/trash; deleting a node puts it there
// and restore takes it out with its links.

import "testing"

func trashCreate(t *testing.T, app *App, title string) string {
	t.Helper()
	r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": title}, nil)
	if r.code != 200 {
		t.Fatalf("create %s: %d %s", title, r.code, r.raw)
	}
	return r.body["unit"].(map[string]any)["uid"].(string)
}

func trashList(t *testing.T, app *App) []map[string]any {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/trash", nil, nil)
	if r.code != 200 {
		t.Fatalf("GET /api/trash: %d %s", r.code, r.raw)
	}
	var out []map[string]any
	for _, item := range r.body["units"].([]any) {
		out = append(out, item.(map[string]any))
	}
	return out
}

func TestTrashRoutes(t *testing.T) {
	app, _ := chatScopeApp(t)
	parent, child, other := trashCreate(t, app, "Parent"), trashCreate(t, app, "Child"), trashCreate(t, app, "Other")
	if r := gpuCall(t, app, "POST", "/api/links", map[string]any{"source": child, "target": parent, "relation": "part_of"}, nil); r.code != 200 {
		t.Fatalf("link: %d %s", r.code, r.raw)
	}
	if got := trashList(t, app); len(got) != 0 {
		t.Fatalf("fresh bin: %v", got)
	}
	// Both delete routes put the node in the bin.
	if r := gpuCall(t, app, "DELETE", "/api/units/"+child, nil, nil); r.code != 200 {
		t.Fatalf("DELETE unit: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/units/"+other+"/archive", map[string]any{}, nil); r.code != 200 {
		t.Fatalf("archive: %d %s", r.code, r.raw)
	}
	got := trashList(t, app)
	if len(got) != 2 {
		t.Fatalf("bin: %v", got)
	}
	byUID := map[string]map[string]any{}
	for _, item := range got {
		byUID[item["uid"].(string)] = item
	}
	if byUID[child]["title"] != "Child" || byUID[child]["links"] != float64(1) || byUID[child]["deleted_at"] == nil {
		t.Fatalf("child in bin: %v", byUID[child])
	}
	// A live unit cannot be deleted for good.
	if r := gpuCall(t, app, "DELETE", "/api/trash/"+parent, nil, nil); r.code != 400 {
		t.Fatalf("purge live unit: %d %s", r.code, r.raw)
	}
	// Restore brings the part_of link back.
	if r := gpuCall(t, app, "POST", "/api/units/"+child+"/restore", map[string]any{}, nil); r.code != 200 {
		t.Fatalf("restore: %d %s", r.code, r.raw)
	}
	r := gpuCall(t, app, "GET", "/api/units/"+child, nil, nil)
	unit, _ := r.body["unit"].(map[string]any)
	if r.code != 200 || unit == nil {
		t.Fatalf("GET restored unit: %d %s", r.code, r.raw)
	}
	if out, _ := unit["links_out"].([]any); len(out) != 1 {
		t.Fatalf("restored links_out: %v", unit["links_out"])
	}
	if r := gpuCall(t, app, "DELETE", "/api/trash/"+other, nil, nil); r.code != 200 || r.body["deleted"] != true {
		t.Fatalf("purge: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/units/"+other, nil, nil); r.code == 200 {
		t.Fatalf("purged unit still readable: %s", r.raw)
	}
	gpuCall(t, app, "DELETE", "/api/units/"+parent, nil, nil)
	if r := gpuCall(t, app, "DELETE", "/api/trash", nil, nil); r.code != 200 || r.body["deleted"] != float64(1) {
		t.Fatalf("empty: %d %s", r.code, r.raw)
	}
	if got := trashList(t, app); len(got) != 0 {
		t.Fatalf("bin after empty: %v", got)
	}
}

func TestTrashGoOnlyRoutesListed(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range GoOnlyRoutes {
		ids[r.ID] = true
	}
	if !ids["goonly:exact:/api/trash"] || !ids[`goonly:regex:^/api/trash/([^/]+)$`] {
		t.Fatalf("GoOnlyRoutes: %v", GoOnlyRoutes)
	}
	if !mobileDevicePathAllowed("/api/trash", "GET") || !mobileDevicePathAllowed("/api/trash/I-0001", "DELETE") {
		t.Fatal("paired devices cannot reach the recycle bin")
	}
}
