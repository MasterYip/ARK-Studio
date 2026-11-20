package server

// Import from another ARK project (Go only, routes_importark.go): read
// access on the source, write on the target, the source unchanged, the
// media reachable in the target.

import (
	"strconv"
	"strings"
	"testing"
)

func asIn(app *App, user, project string) map[string]string {
	h := sessionCookie(app, user)
	h["X-ARK-Project"] = project
	return h
}

func TestImportArkRoutes(t *testing.T) {
	app, teamID, anaID := peopleApp(t) // ana: read on Team, admin of AnaProj
	owner := asIn(app, "owner", teamID)
	idea := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "Team idea", "body": "an idea"}, owner)
	if idea.code != 200 {
		t.Fatalf("create: %d %s", idea.code, idea.raw)
	}
	uid := idea.body["unit"].(map[string]any)["uid"].(string)
	up := gpuCall(t, app, "POST", "/api/media/upload", map[string]any{"unit": uid, "filename": "dot.png", "mime": "image/png",
		"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}, owner)
	if up.code != 200 {
		t.Fatalf("upload: %d %s", up.code, up.raw)
	}
	before := gpuCall(t, app, "GET", "/api/units/"+uid, nil, owner).raw

	ana := asIn(app, "ana", anaID)
	g := gpuCall(t, app, "GET", "/api/import-ark/graph?project="+teamID+"&types=idea", nil, ana)
	if g.code != 200 || len(g.body["nodes"].([]any)) != 1 || g.body["facets"] == nil || g.body["project"].(map[string]any)["name"] != "Team" {
		t.Fatalf("source graph: %d %s", g.code, g.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/import-ark/unit?project="+teamID+"&uid="+uid, nil, ana); r.code != 200 {
		t.Fatalf("source unit: %d %s", r.code, r.raw)
	}
	body := map[string]any{"project": teamID, "units": []string{uid}, "with_children": true, "lang": "en"}
	pv := gpuCall(t, app, "POST", "/api/import-ark/preview", body, ana)
	if pv.code != 200 || pv.body["unit_count"] != float64(1) || pv.body["media"] != float64(1) {
		t.Fatalf("preview: %d %s", pv.code, pv.raw)
	}
	ap := gpuCall(t, app, "POST", "/api/import-ark/apply", body, ana)
	if ap.code != 200 || ap.body["imported"] != float64(1) {
		t.Fatalf("apply: %d %s", ap.code, ap.raw)
	}
	created := ap.body["created"].([]any)[0].(map[string]any)
	if !strings.Contains(created["body"].(string), "Imported from Team [["+teamID+"/"+uid+"]]") {
		t.Fatalf("provenance: %v", created["body"])
	}
	media := created["media"].([]any)
	if len(media) != 1 {
		t.Fatalf("media: %v", created["media"])
	}
	id := int64(media[0].(map[string]any)["id"].(float64))
	if r := gpuCall(t, app, "GET", "/api/media/"+strconv.FormatInt(id, 10)+"/raw", nil, ana); r.code != 200 {
		t.Fatalf("media in target: %d", r.code)
	}
	// The source graph now names the copy, and its reply says which project is which.
	g = gpuCall(t, app, "GET", "/api/import-ark/graph?project="+teamID, nil, ana)
	if imp, _ := g.body["imported"].(map[string]any); imp[uid] != created["uid"] ||
		g.body["target"].(map[string]any)["id"] != anaID || g.body["project"].(map[string]any)["id"] != teamID {
		t.Fatalf("imported map: %s", g.raw)
	}
	// The source is unchanged.
	if after := gpuCall(t, app, "GET", "/api/units/"+uid, nil, owner).raw; after != before {
		t.Fatalf("source changed:\n%s\n%s", before, after)
	}
	// Write on the target is needed: ana can only read Team.
	back := map[string]any{"project": anaID, "units": []string{created["uid"].(string)}}
	if r := gpuCall(t, app, "POST", "/api/import-ark/apply", back, asIn(app, "ana", teamID)); r.code != 403 {
		t.Fatalf("import into a read-only project: %d %s", r.code, r.raw)
	}
	// Read on the source is needed: ana has no grant on the owner's Private
	// project (org viewers read every project by role, members do not).
	pr := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Private"}, sessionCookie(app, "owner"))
	privateID, _ := pr.body["summary"].(map[string]any)["id"].(string)
	if r := gpuCall(t, app, "GET", "/api/import-ark/graph?project="+privateID, nil, ana); r.code != 404 {
		t.Fatalf("unreadable source: %d %s", r.code, r.raw)
	}
	// Not from the project itself.
	if r := gpuCall(t, app, "POST", "/api/import-ark/preview", map[string]any{"project": anaID, "units": []string{"I-0001"}}, ana); r.code != 400 {
		t.Fatalf("same project: %d %s", r.code, r.raw)
	}
}
