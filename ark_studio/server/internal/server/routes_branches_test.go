package server

import (
	"context"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// Lines of work (routes_branches.go): read access for people and agents;
// no access, no lines.
func TestBranchRoutesPermissions(t *testing.T) {
	app, projectID := authDesktopApp(t)
	owner := withProject(sessionCookie(app, "owner"), projectID)
	r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "Pilot", "status": "review",
		"tags": []any{"branch:pilot"}}, owner)
	if r.code != 200 {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	uid := r.body["unit"].(map[string]any)["uid"].(string)

	// A reader (vic has a read grant) sees the lines and the virtual topic.
	vic := withProject(sessionCookie(app, "vic"), projectID)
	got := gpuCall(t, app, "GET", branchesPath, nil, vic)
	if got.code != 200 || len(got.body["items"].([]any)) != 1 {
		t.Fatalf("reader: %d %s", got.code, got.raw)
	}
	needs := got.body["needs"].(map[string]any)
	if lines, _ := needs[uid].([]any); len(lines) != 1 || lines[0] != "pilot" {
		t.Fatalf("needs: %v", needs)
	}
	if page := gpuCall(t, app, "GET", branchTopicPath+"?name=pilot", nil, vic); page.code != 200 ||
		len(page.body["members"].([]any)) != 1 {
		t.Fatalf("branch topic: %d %s", page.code, page.raw)
	}
	if page := gpuCall(t, app, "GET", branchTopicPath, nil, vic); page.code != 400 {
		t.Fatalf("branch topic without a name: %d %s", page.code, page.raw)
	}
	if s := gpuCall(t, app, "GET", branchSuggestPath+"?depth=3&relations=part_of", nil, vic); s.code != 200 {
		t.Fatalf("suggest: %d %s", s.code, s.raw)
	}
	if s := gpuCall(t, app, "GET", branchSuggestPath+"?relations=nonsense", nil, vic); s.code != 400 {
		t.Fatalf("suggest with an unknown relation: %d %s", s.code, s.raw)
	}

	// Renaming a line needs write access.
	if r := gpuCall(t, app, "POST", branchRenamePath, map[string]any{"from": "pilot", "to": "hoi/pilot"}, vic); r.code != 403 {
		t.Fatalf("reader renames: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", branchRenamePath, map[string]any{"from": "pilot", "to": "hoi/pilot", "dry_run": true}, owner); r.code != 200 ||
		len(r.body["changes"].([]any)) != 1 {
		t.Fatalf("rename preview: %d %s", r.code, r.raw)
	}
	// Someone without a grant on the project: refused.
	if _, err := app.Org.CreateUser(context.Background(), "owner", pyjson.NewObject("username", "eve",
		"password", desktopPassword, "role", "member")); err != nil {
		t.Fatal(err)
	}
	eveCookie := sessionCookie(app, "eve")
	if open := gpuCall(t, app, "POST", "/api/projects/open", map[string]any{"id": projectID}, eveCookie); open.code == 200 {
		t.Fatalf("eve opened the project: %s", open.raw)
	}
	if eve := gpuCall(t, app, "GET", branchesPath, nil, eveCookie); eve.code == 200 && len(eve.body["items"].([]any)) != 0 {
		t.Fatalf("no grant, yet lines: %s", eve.raw)
	}
	// Signed out: refused.
	if anon := gpuCall(t, app, "GET", branchesPath, nil, map[string]string{"X-ARK-Project": projectID}); anon.code != 401 {
		t.Fatalf("signed out: %d %s", anon.code, anon.raw)
	}
}

func TestBranchRoutesAgentAndSettings(t *testing.T) {
	app, _ := localGPUApp(t)
	agent := map[string]string{"X-ARK-Token": app.AgentToken(), "X-Agent-Name": "claude"}
	if r := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "Git work",
		"tags": []any{"branch:feat/x", "line:pilot"}}, agent); r.code != 200 {
		t.Fatalf("agent create: %d %s", r.code, r.raw)
	}
	got := gpuCall(t, app, "GET", agentBranchesPath, nil, agent)
	if got.code != 200 || got.body["prefix"] != "branch" || len(got.body["items"].([]any)) != 1 {
		t.Fatalf("agent branches: %d %s", got.code, got.raw)
	}
	// The prefix is a project setting; a bad one is refused.
	if r := gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"branches": map[string]any{"prefix": "Needs"}}, nil); r.code != 400 {
		t.Fatalf("bad prefix: %d %s", r.code, r.raw)
	}
	r := gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"branches": map[string]any{"prefix": "line"}}, nil)
	if r.code != 200 {
		t.Fatalf("set prefix: %d %s", r.code, r.raw)
	}
	if s := r.body["settings"].(map[string]any)["branches"].(map[string]any); s["prefix"] != "line" {
		t.Fatalf("settings: %v", s)
	}
	got = gpuCall(t, app, "GET", agentBranchesPath, nil, agent)
	items := got.body["items"].([]any)
	if got.body["prefix"] != "line" || len(items) != 1 || items[0].(map[string]any)["name"] != "pilot" {
		t.Fatalf("after the prefix change: %s", got.raw)
	}
	// An admin's agent sets it too.
	if r := gpuCall(t, app, "POST", agentBranchPrefix, map[string]any{"prefix": "BAD"}, agent); r.code != 400 {
		t.Fatalf("agent bad prefix: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", agentBranchPrefix, map[string]any{"prefix": "branch"}, agent); r.code != 200 || r.body["prefix"] != "branch" {
		t.Fatalf("agent prefix: %d %s", r.code, r.raw)
	}
	if got := gpuCall(t, app, "GET", agentBranchesPath, nil, agent); got.body["prefix"] != "branch" {
		t.Fatalf("after the agent's change: %s", got.raw)
	}
	if r := gpuCall(t, app, "POST", agentBranchPrefix, map[string]any{"prefix": "line"}, agent); r.code != 200 || r.body["prefix"] != "line" {
		t.Fatalf("agent prefix line: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"branches": nil}, nil); r.code != 200 ||
		r.body["settings"].(map[string]any)["branches"] != nil {
		t.Fatalf("clear prefix: %d %s", r.code, r.raw)
	}
}

func TestBranchGoOnlyRoutesListed(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range GoOnlyRoutes {
		ids[r.ID] = true
	}
	for _, p := range []string{branchesPath, branchSuggestPath, branchTopicPath, agentBranchesPath, agentBranchPrefix} {
		if !ids["goonly:exact:"+p] {
			t.Errorf("%s not in GoOnlyRoutes", p)
		}
	}
	if !mobileDevicePathAllowed(branchesPath, "GET") || !mobileDevicePathAllowed(branchTopicPath, "GET") {
		t.Fatal("paired devices cannot read the lines")
	}
}

func withProject(h map[string]string, projectID string) map[string]string {
	out := map[string]string{"X-ARK-Project": projectID}
	for k, v := range h {
		out[k] = v
	}
	return out
}
