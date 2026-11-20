package server

// Agent link editing (Go only, routes_agentlinks.go): list a unit's links,
// delete by pair (+relation) or id, change a relation.

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func agentLinksSetup(t *testing.T) (*App, map[string]string, string, string, string) {
	t.Helper()
	app, _ := chatScopeApp(t)
	a, b, c := trashCreate(t, app, "Alpha"), trashCreate(t, app, "Beta"), trashCreate(t, app, "Gamma")
	auth := map[string]string{"Authorization": "Bearer " + app.AgentToken(), "X-Agent-Name": "linker"}
	for _, l := range [][3]string{{a, b, "supports"}, {a, b, "cites"}, {a, c, "part_of"}, {c, b, "relates_to"}} {
		if r := gpuCall(t, app, "POST", "/api/agent/v1/links", map[string]any{"source": l[0], "target": l[1], "relation": l[2]}, auth); r.code != 200 {
			t.Fatalf("link %v: %d %s", l, r.code, r.raw)
		}
	}
	return app, auth, a, b, c
}

func agentLinksOf(t *testing.T, app *App, auth map[string]string, uid, query string) []map[string]any {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/agent/v1/units/"+uid+"/links"+query, nil, auth)
	if r.code != 200 {
		t.Fatalf("GET links of %s: %d %s", uid, r.code, r.raw)
	}
	var out []map[string]any
	for _, item := range r.body["links"].([]any) {
		out = append(out, item.(map[string]any))
	}
	return out
}

func TestAgentUnitLinksList(t *testing.T) {
	app, auth, a, b, c := agentLinksSetup(t)
	links := agentLinksOf(t, app, auth, b, "")
	if len(links) != 3 {
		t.Fatalf("links of %s: %v", b, links)
	}
	for _, l := range links {
		if l["direction"] != "in" || l["target"] != b || l["id"] == nil {
			t.Fatalf("incoming link: %v", l)
		}
	}
	if got := agentLinksOf(t, app, auth, a, "?direction=out&relation=part_of"); len(got) != 1 ||
		got[0]["target"] != c || got[0]["other"].(map[string]any)["title"] != "Gamma" {
		t.Fatalf("filtered: %v", got)
	}
	if got := agentLinksOf(t, app, auth, a, "?direction=in"); len(got) != 0 {
		t.Fatalf("in-links of %s: %v", a, got)
	}
	if r := gpuCall(t, app, "GET", "/api/agent/v1/units/X-9999/links", nil, auth); r.code != 404 {
		t.Fatalf("unknown unit: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/agent/v1/units/"+a+"/links", nil, nil); r.code != 401 {
		t.Fatalf("no token: %d %s", r.code, r.raw)
	}
}

func TestAgentDeleteLinkByPair(t *testing.T) {
	app, auth, a, b, c := agentLinksSetup(t)
	// Two relations between a and b: ambiguous without relation.
	r := gpuCall(t, app, "DELETE", "/api/agent/v1/links", map[string]any{"source": a, "target": b}, auth)
	if r.code != 409 || r.errCode() != "LINK_AMBIGUOUS" || len(r.body["links"].([]any)) != 2 ||
		!strings.Contains(r.raw, "supports") || !strings.Contains(r.raw, "cites") {
		t.Fatalf("ambiguous: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "DELETE", "/api/agent/v1/links", map[string]any{"source": a, "target": b, "relation": "cites"}, auth)
	if r.code != 200 || r.body["deleted"] != true || r.body["relation"] != "cites" || r.body["source"] != a {
		t.Fatalf("delete cites: %d %s", r.code, r.raw)
	}
	// Now one link is left: no relation needed; the query form works too.
	r = gpuCall(t, app, "DELETE", "/api/agent/v1/links?"+url.Values{"source": {a}, "target": {b}}.Encode(), nil, auth)
	if r.code != 200 || r.body["relation"] != "supports" {
		t.Fatalf("delete by query: %d %s", r.code, r.raw)
	}
	// Not found: the message names the reverse link.
	r = gpuCall(t, app, "DELETE", "/api/agent/v1/links", map[string]any{"source": c, "target": a}, auth)
	if r.code != 404 || r.errCode() != "LINK_NOT_FOUND" || !strings.Contains(r.raw, "swap source and target") {
		t.Fatalf("missing link: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "DELETE", "/api/agent/v1/links", map[string]any{"source": a, "target": "X-9999"}, auth); r.code != 404 {
		t.Fatalf("unknown unit: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "DELETE", "/api/agent/v1/links", map[string]any{"source": a}, auth); r.code != 400 {
		t.Fatalf("no target: %d %s", r.code, r.raw)
	}
	// relates_to has no direction: either order deletes it.
	r = gpuCall(t, app, "DELETE", "/api/agent/v1/links", map[string]any{"source": b, "target": c, "relation": "relates_to"}, auth)
	if r.code != 200 || r.body["source"] != c {
		t.Fatalf("symmetric: %d %s", r.code, r.raw)
	}
	if got := agentLinksOf(t, app, auth, b, ""); len(got) != 0 {
		t.Fatalf("links left on %s: %v", b, got)
	}
	// Activity: link.delete with the agent actor.
	p, err := (&reqCtx{app: app, ctx: context.Background()}).currentProject()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p.Store.Activity(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	deletes := 0
	for _, item := range rows {
		row := item.(*pyjson.Object)
		if row.Value("action") == "link.delete" {
			deletes++
			if row.Value("actor") != "agent:linker" {
				t.Fatalf("actor: %v", row.Value("actor"))
			}
		}
	}
	if deletes != 3 {
		t.Fatalf("link.delete rows: %d", deletes)
	}
}

func TestAgentDeleteAndPatchLinkByID(t *testing.T) {
	app, auth, a, _, c := agentLinksSetup(t)
	links := agentLinksOf(t, app, auth, a, "?relation=part_of")
	id := pyjson.Str(int64(links[0]["id"].(float64)))
	r := gpuCall(t, app, "PATCH", "/api/agent/v1/links/"+id, map[string]any{"relation": "supports"}, auth)
	if r.code != 200 || r.body["updated"] != true || r.body["relation"] != "supports" || r.body["target"] != c {
		t.Fatalf("patch: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "PATCH", "/api/agent/v1/links/"+id, map[string]any{"relation": "nonsense"}, auth); r.code != 400 {
		t.Fatalf("unknown relation: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "PATCH", "/api/agent/v1/links/"+id, map[string]any{"relation": "supports"}, auth); r.code != 200 || r.body["updated"] != false {
		t.Fatalf("same relation: %d %s", r.code, r.raw)
	}
	// a already supports b: changing a->b cites into supports would collide.
	ab := agentLinksOf(t, app, auth, a, "?relation=cites")
	abID := pyjson.Str(int64(ab[0]["id"].(float64)))
	if r := gpuCall(t, app, "PATCH", "/api/agent/v1/links/"+abID, map[string]any{"relation": "supports"}, auth); r.code != 409 || r.errCode() != "LINK_EXISTS" {
		t.Fatalf("collision: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "DELETE", "/api/agent/v1/links/"+id, nil, auth)
	if r.code != 200 || r.body["deleted"] != true || r.body["relation"] != "supports" {
		t.Fatalf("delete by id: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "DELETE", "/api/agent/v1/links/"+id, nil, auth); r.code != 404 || r.errCode() != "LINK_NOT_FOUND" {
		t.Fatalf("delete twice: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "PATCH", "/api/agent/v1/links/999999", map[string]any{"relation": "cites"}, auth); r.code != 404 {
		t.Fatalf("patch missing: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "DELETE", "/api/agent/v1/links/"+abID, nil, nil); r.code != 401 {
		t.Fatalf("no token: %d %s", r.code, r.raw)
	}
}

func TestAgentLinkWritesNeedWriteScope(t *testing.T) {
	app, auth, a, b, _ := agentLinksSetup(t)
	tok, err := app.Org.CreateAgentToken(context.Background(), "local", pyjson.NewObject("scope", "read"))
	if err != nil {
		t.Fatal(err)
	}
	read := map[string]string{"Authorization": "Bearer " + pyjson.Str(tok.Value("token"))}
	if got := agentLinksOf(t, app, read, a, ""); len(got) != 3 {
		t.Fatalf("read token list: %v", got)
	}
	r := gpuCall(t, app, "DELETE", "/api/agent/v1/links", map[string]any{"source": a, "target": b, "relation": "cites"}, read)
	if r.code != 403 {
		t.Fatalf("read token delete: %d %s", r.code, r.raw)
	}
	id := pyjson.Str(int64(agentLinksOf(t, app, auth, a, "?relation=cites")[0]["id"].(float64)))
	if r := gpuCall(t, app, "DELETE", "/api/agent/v1/links/"+id, nil, read); r.code != 403 {
		t.Fatalf("read token delete by id: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "PATCH", "/api/agent/v1/links/"+id, map[string]any{"relation": "supports"}, read); r.code != 403 {
		t.Fatalf("read token patch: %d %s", r.code, r.raw)
	}
	if got := agentLinksOf(t, app, auth, a, ""); len(got) != 3 {
		t.Fatalf("links after refused writes: %v", got)
	}
}

func TestAgentLinkRoutesAreGoOnly(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range GoOnlyRoutes {
		ids[r.ID] = true
	}
	for _, r := range agentLinksGoOnly {
		if !ids[r.ID] {
			t.Errorf("%s missing from GoOnlyRoutes", r.ID)
		}
	}
}
