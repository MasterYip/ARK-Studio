package server

// Project split and merge routes (Go only, routes_projectmove.go): an agent
// reads the clusters and proposes a plan, cannot approve or apply it on its
// own, a person approves, the agent applies; the moved unit answers 409 and
// the agent's feed says where it went; undo; a merge leaves the source
// read-only until it is undone.

import (
	"strings"
	"testing"
)

func moveErrCode(r gpuReply) any {
	e, _ := r.body["error"].(map[string]any)
	return e["code"]
}

func TestProjectMoveAgentFlow(t *testing.T) {
	app, src := chatScopeApp(t)
	agent := presenceAgentHeaders(app, "splitter")
	parent, child, stay := trashCreate(t, app, "Parent"), trashCreate(t, app, "Child"), trashCreate(t, app, "Stays")
	for _, l := range [][3]string{{child, parent, "part_of"}, {parent, stay, "relates_to"}} {
		if r := gpuCall(t, app, "POST", "/api/links", map[string]any{"source": l[0], "target": l[1], "relation": l[2]}, nil); r.code != 200 {
			t.Fatalf("link: %d %s", r.code, r.raw)
		}
	}
	base := "/api/agent/v1/project-move"
	r := gpuCall(t, app, "GET", base+"/clusters", nil, agent)
	if r.code != 200 {
		t.Fatalf("clusters: %d %s", r.code, r.raw)
	}
	var cluster string
	for _, c := range r.body["clusters"].([]any) {
		o := c.(map[string]any)
		for _, root := range o["roots"].([]any) {
			if root.(map[string]any)["uid"] == parent {
				cluster = o["id"].(string)
			}
		}
	}
	if cluster == "" {
		t.Fatalf("no cluster holds %s: %s", parent, r.raw)
	}
	// A split needs a new project name.
	if r := gpuCall(t, app, "POST", base+"/plans", map[string]any{"mode": "split", "clusters": []string{cluster}}, agent); r.code != 400 {
		t.Fatalf("split without name: %d %s", r.code, r.raw)
	}
	// A cluster id resolves to its roots (here the tiny graph groups all
	// three); the plan below names the root itself.
	if r := gpuCall(t, app, "POST", base+"/plans", map[string]any{"mode": "split", "clusters": []string{"c-NOPE"},
		"new_project": map[string]any{"name": "x"}}, agent); r.code != 400 || moveErrCode(r) != "MOVE_CLUSTER_UNKNOWN" {
		t.Fatalf("unknown cluster: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "POST", base+"/plans", map[string]any{"mode": "split", "roots": []string{parent},
		"new_project": map[string]any{"name": "Split off", "description": "the parent branch"}, "note": "Parent is its own line.", "lang": "en"}, agent)
	if r.code != 200 {
		t.Fatalf("propose: %d %s", r.code, r.raw)
	}
	plan := r.body["plan"].(map[string]any)
	id, decision := plan["id"].(string), plan["decision"].(string)
	summary := plan["summary"].(map[string]any)
	if plan["state"] != "proposed" || decision == "" || summary["unit_count"] != float64(2) || len(summary["cross_links"].([]any)) != 1 {
		t.Fatalf("plan: %s", r.raw)
	}
	// The decision unit waits in Needs you with the summary and the link.
	d := gpuCall(t, app, "GET", "/api/units/"+decision, nil, nil)
	if d.code != 200 || d.body["unit"].(map[string]any)["status"] != "open" || strings.Contains(d.raw, "%!") ||
		!strings.Contains(d.body["unit"].(map[string]any)["body"].(string), "?move_plan="+id) {
		t.Fatalf("decision unit: %d %s", d.code, d.raw)
	}
	// The agent can neither apply before approval nor approve itself.
	if r := gpuCall(t, app, "POST", base+"/plans/"+id+"/apply", map[string]any{}, agent); r.code != 409 || moveErrCode(r) != "MOVE_NOT_APPROVED" {
		t.Fatalf("apply before approval: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", base+"/plans/"+id+"/approve", map[string]any{}, agent); r.code != 403 {
		t.Fatalf("agent approves: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/project-move/plans/"+id+"/approve", map[string]any{}, agent); r.code != 403 {
		t.Fatalf("agent approves through the web route: %d %s", r.code, r.raw)
	}
	// A person approves; the decision closes.
	if r := gpuCall(t, app, "POST", "/api/project-move/plans/"+id+"/approve", map[string]any{"comment": "ok"}, nil); r.code != 200 {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	if d := gpuCall(t, app, "GET", "/api/units/"+decision, nil, nil); d.body["unit"].(map[string]any)["status"] != "done" {
		t.Fatalf("decision after approval: %s", d.raw)
	}
	r = gpuCall(t, app, "POST", base+"/plans/"+id+"/apply", map[string]any{}, agent)
	if r.code != 200 {
		t.Fatalf("apply: %d %s", r.code, r.raw)
	}
	plan = r.body["plan"].(map[string]any)
	if plan["state"] != "applied" || plan["move_state"] != "done" {
		t.Fatalf("applied plan: %s", r.raw)
	}
	target := plan["request"].(map[string]any)["target"].(string)
	// The moved unit refuses the agent's write and names the new place.
	r = gpuCall(t, app, "POST", "/api/agent/v1/units/"+parent, map[string]any{"title": "x"}, agent)
	if r.code != 409 || moveErrCode(r) != "UNIT_MOVED" || !strings.Contains(r.raw, "Split off") {
		t.Fatalf("agent write to moved unit: %d %s", r.code, r.raw)
	}
	c := &reqCtx{app: app, ctx: t.Context()}
	if cur, _ := c.currentProject(); cur == nil || cur.ID() != src.ID() {
		t.Fatalf("current project changed")
	}
	split, err := c.projectByID(target)
	if err != nil || split == nil {
		t.Fatalf("target project: %v", err)
	}
	if u, err := split.Store.GetUnit(t.Context(), child); err != nil || u.Value("title") != "Child" {
		t.Fatalf("unit in target: %v %v", u, err)
	}
	if r := gpuCall(t, app, "POST", base+"/plans/"+id+"/apply", map[string]any{}, agent); r.code != 409 {
		t.Fatalf("second apply: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", base+"/plans/"+id+"/undo", map[string]any{}, agent); r.code != 200 {
		t.Fatalf("undo: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "PATCH", "/api/units/"+parent, map[string]any{"title": "back"}, nil); r.code != 200 {
		t.Fatalf("write after undo: %d %s", r.code, r.raw)
	}

	// Merge, proposed by a person and approved and applied in one step; the
	// decision unit of the first plan stays behind.
	r = gpuCall(t, app, "POST", "/api/project-move/plans", map[string]any{"mode": "merge", "target_project": split.ID()}, nil)
	if r.code != 200 || r.body["plan"].(map[string]any)["decision"] != "" {
		t.Fatalf("merge plan: %d %s", r.code, r.raw)
	}
	mid := r.body["plan"].(map[string]any)["id"].(string)
	if r := gpuCall(t, app, "POST", "/api/project-move/plans/"+mid+"/approve", map[string]any{"apply": true}, nil); r.code != 200 {
		t.Fatalf("merge: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "late"}, nil); r.code != 409 || moveErrCode(r) != "PROJECT_MERGED" {
		t.Fatalf("create in merged project: %d %s", r.code, r.raw)
	}
	// The merge renumbers its batch when an id clashes (D-0015); the alias
	// leads from the old id to the merged unit.
	if p, u, err := split.Store.Alias(t.Context(), src.ID(), stay); err != nil || p != split.ID() || u == "" {
		t.Fatalf("alias of merged unit: %s %s %v", p, u, err)
	} else if got, err := split.Store.GetUnit(t.Context(), u); err != nil || got.Value("title") != "Stays" {
		t.Fatalf("merged unit: %v %v", got, err)
	}
	if r := gpuCall(t, app, "POST", "/api/project-move/plans/"+mid+"/undo", map[string]any{}, nil); r.code != 200 {
		t.Fatalf("undo merge: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "after undo"}, nil); r.code != 200 {
		t.Fatalf("create after undoing the merge: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", base+"/plans", nil, agent); r.code != 200 || len(r.body["plans"].([]any)) != 2 {
		t.Fatalf("plans: %d %s", r.code, r.raw)
	}
}
