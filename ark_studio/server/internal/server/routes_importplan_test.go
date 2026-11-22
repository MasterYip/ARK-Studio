package server

// Agent import plans (routes_projectmove.go /api/agent/v1/import-ark): an
// agent reads the source, proposes a copy (a decision in Needs you), cannot
// approve or apply it before a person does; the person approves, the agent
// applies, undo puts the copies in the recycle bin.  The source stays.

import (
	"strings"
	"testing"
)

func issueTestToken(t *testing.T, app *App, user string) string {
	t.Helper()
	r := gpuCall(t, app, "POST", "/api/personal/agent-tokens", map[string]any{"label": "test", "scope": "write"}, sessionCookie(app, user))
	tok, _ := r.body["token"].(string)
	if r.code != 200 || tok == "" {
		t.Fatalf("token for %s: %d %s", user, r.code, r.raw)
	}
	return tok
}

func TestAgentImportPlanFlow(t *testing.T) {
	app, teamID, anaID := peopleApp(t) // ana: read on Team, admin of AnaProj
	owner := asIn(app, "owner", teamID)
	idea := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "idea", "title": "Team idea", "body": "an idea"}, owner)
	uid := idea.body["unit"].(map[string]any)["uid"].(string)
	before := gpuCall(t, app, "GET", "/api/units/"+uid, nil, owner).raw

	agent := map[string]string{"Authorization": "Bearer " + issueTestToken(t, app, "ana"), "X-ARK-Project": anaID, "X-Agent-Name": "copier"}
	base := "/api/agent/v1/import-ark"
	if r := gpuCall(t, app, "GET", base+"/graph?project="+teamID, nil, agent); r.code != 200 || len(r.body["nodes"].([]any)) == 0 {
		t.Fatalf("source graph: %d %s", r.code, r.raw)
	}
	r := gpuCall(t, app, "POST", base+"/plans", map[string]any{"project": teamID, "units": []string{uid}, "note": "the team idea", "lang": "en"}, agent)
	if r.code != 200 {
		t.Fatalf("propose: %d %s", r.code, r.raw)
	}
	plan := r.body["plan"].(map[string]any)
	id, decision := plan["id"].(string), plan["decision"].(string)
	if plan["mode"] != "import" || plan["state"] != "proposed" || decision == "" {
		t.Fatalf("plan: %s", r.raw)
	}
	d := gpuCall(t, app, "GET", "/api/units/"+decision, nil, asIn(app, "ana", anaID))
	if d.body["unit"].(map[string]any)["status"] != "open" || !strings.Contains(d.raw, "?move_plan="+id) {
		t.Fatalf("decision: %s", d.raw)
	}
	// Nothing is copied before a person approves; the agent cannot approve.
	if r := gpuCall(t, app, "POST", base+"/plans/"+id+"/apply", map[string]any{}, agent); r.code != 409 || moveErrCode(r) != "MOVE_NOT_APPROVED" {
		t.Fatalf("apply before approval: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", base+"/plans/"+id+"/approve", map[string]any{}, agent); r.code != 403 {
		t.Fatalf("agent approves: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/project-move/plans/"+id+"/approve", map[string]any{}, asIn(app, "ana", anaID)); r.code != 200 {
		t.Fatalf("approve: %d %s", r.code, r.raw)
	}
	r = gpuCall(t, app, "POST", base+"/plans/"+id+"/apply", map[string]any{}, agent)
	if r.code != 200 || r.body["import"].(map[string]any)["imported"] != float64(1) {
		t.Fatalf("apply: %d %s", r.code, r.raw)
	}
	created := r.body["plan"].(map[string]any)["created"].([]any)
	copyUID := created[0].(string)
	u := gpuCall(t, app, "GET", "/api/units/"+copyUID, nil, asIn(app, "ana", anaID))
	if !strings.Contains(u.raw, "Imported from Team [["+teamID+"/"+uid+"]]") {
		t.Fatalf("copy: %s", u.raw)
	}
	if after := gpuCall(t, app, "GET", "/api/units/"+uid, nil, owner).raw; after != before {
		t.Fatal("source changed")
	}
	// The plan is readable under either agent base; undo puts the copy in the bin.
	if r := gpuCall(t, app, "GET", "/api/agent/v1/project-move/plans/"+id, nil, agent); r.code != 200 || r.body["plan"].(map[string]any)["state"] != "applied" {
		t.Fatalf("plan: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", base+"/plans/"+id+"/undo", map[string]any{}, agent); r.code != 200 {
		t.Fatalf("undo: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/trash", nil, asIn(app, "ana", anaID)); !strings.Contains(r.raw, copyUID) {
		t.Fatalf("copy not in the recycle bin: %s", r.raw)
	}
	// Write access here is needed: ana's agent only reads Team.
	agentInTeam := map[string]string{"Authorization": agent["Authorization"], "X-ARK-Project": teamID}
	if r := gpuCall(t, app, "POST", base+"/plans", map[string]any{"project": anaID, "units": []string{copyUID}}, agentInTeam); r.code != 403 {
		t.Fatalf("import into a read-only project: %d %s", r.code, r.raw)
	}
}
