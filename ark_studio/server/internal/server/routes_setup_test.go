package server

import (
	"strings"
	"testing"
)

func setupLayerStates(t *testing.T, r gpuReply) (string, string, string, string) {
	t.Helper()
	layers, _ := r.body["layers"].(map[string]any)
	p, _ := layers["project"].(map[string]any)
	u, _ := layers["user"].(map[string]any)
	if p == nil || u == nil {
		t.Fatalf("no layers: %d %s", r.code, r.raw)
	}
	return p["state"].(string), p["digest"].(string), u["state"].(string), u["digest"].(string)
}

// One propose call sends every changed layer; one confirm call confirms a
// list of layers; a single proposed layer confirms on its own.
func TestSetupProposeAllAndConfirmLayers(t *testing.T) {
	app, _ := localGPUApp(t)
	agent := map[string]string{"Authorization": "Bearer " + app.AgentToken()}
	res := func(id, kind string, details map[string]any, path string) map[string]any {
		return map[string]any{"id": id, "kind": kind, "name": id, "details": details, "path_or_ref": path}
	}
	call := func(method, path string, body any, h map[string]string) gpuReply {
		t.Helper()
		r := gpuCall(t, app, method, path, body, h)
		if r.code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, r.code, r.raw)
		}
		return r
	}
	call("POST", "/api/skills/setup/resource", map[string]any{"layer": "project",
		"resource": res("repo", "repository", map[string]any{"role": "primary", "access": "read-write"}, "/src/repo")}, nil)
	call("POST", "/api/skills/setup/resource", map[string]any{"layer": "user",
		"resource": res("ark-public", "repository", map[string]any{"role": "code", "access": "read-only"}, "/src/ark")}, nil)

	// One call proposes both drafts.
	r := call("POST", "/api/skills/setup/propose", map[string]any{}, nil)
	ps, pd, us, ud := setupLayerStates(t, r)
	if ps != "proposed" || us != "proposed" {
		t.Fatalf("propose all: %s %s", ps, us)
	}
	if pending, _ := r.body["pending"].([]any); len(pending) != 2 {
		t.Fatalf("pending: %s", r.raw)
	}
	// One call confirms both, each with its own digest.
	r = call("POST", "/api/skills/setup/confirm", map[string]any{"layers": []any{
		map[string]any{"layer": "project", "digest": pd}, map[string]any{"layer": "user", "digest": ud}}}, nil)
	if ps, _, us, _ = setupLayerStates(t, r); ps != "confirmed" || us != "confirmed" {
		t.Fatalf("confirm both: %s %s", ps, us)
	}
	if confirmed, _ := r.body["confirmed"].([]any); len(confirmed) != 2 {
		t.Fatalf("confirmed: %s", r.raw)
	}

	// The agent proposes a project change only; the user layer has nothing
	// pending.
	r = call("POST", "/api/agent/v1/setup/resource", map[string]any{"layer": "project", "propose": true,
		"resource": res("agent-team-tasks", "task_space", map[string]any{"kind": "agent-team-folder", "access": "read-write"}, "/tasks")},
		agent)
	if pending, _ := r.body["pending"].([]any); len(pending) != 1 || !strings.Contains(r.raw, `"proposed": [{"layer": "project"`) {
		t.Fatalf("agent propose: %s", r.raw)
	}
	r = call("GET", "/api/skills/setup/state", nil, nil)
	ps, pd, us, _ = setupLayerStates(t, r)
	if ps != "proposed" || us != "confirmed" {
		t.Fatalf("after agent: %s %s", ps, us)
	}
	if !strings.Contains(r.raw, `"key": "project/agent-team-tasks"`) || !strings.Contains(r.raw, `"change": "added"`) {
		t.Fatalf("changes: %s", r.raw)
	}
	// A stale digest refuses the whole request.
	bad := gpuCall(t, app, "POST", "/api/skills/setup/confirm", map[string]any{"layers": []any{
		map[string]any{"layer": "project", "digest": "0000"}}}, nil)
	if bad.code != 409 || bad.errCode() != "SETUP_DIGEST" {
		t.Fatalf("stale digest: %d %s", bad.code, bad.raw)
	}
	r = call("POST", "/api/skills/setup/confirm", map[string]any{"layers": []any{
		map[string]any{"layer": "project", "digest": pd}}}, nil)
	if ps, _, us, _ = setupLayerStates(t, r); ps != "confirmed" || us != "confirmed" {
		t.Fatalf("confirm project only: %s %s", ps, us)
	}

	// resource with propose: true also sends the other changed layer.
	call("POST", "/api/skills/setup/resource", map[string]any{"layer": "user",
		"resource": res("scratch", "storage", map[string]any{"kind": "artifacts"}, "/scratch")}, nil)
	r = call("POST", "/api/skills/setup/resource", map[string]any{"layer": "project", "propose": true,
		"resource": res("logs", "storage", map[string]any{"kind": "logs"}, "/logs")}, nil)
	if ps, _, us, ud = setupLayerStates(t, r); ps != "proposed" || us != "proposed" {
		t.Fatalf("propose with resource: %s %s", ps, us)
	}
	// The single-layer form still works.
	r = call("POST", "/api/skills/setup/confirm", map[string]any{"layer": "user", "digest": ud}, nil)
	if ps, _, us, _ = setupLayerStates(t, r); ps != "proposed" || us != "confirmed" {
		t.Fatalf("single-layer confirm: %s %s", ps, us)
	}
	// Agents never confirm.
	if r := gpuCall(t, app, "POST", "/api/agent/v1/setup/confirm", map[string]any{"layers": []any{
		map[string]any{"layer": "project", "digest": "x"}}}, agent); r.code != 403 {
		t.Fatalf("agent confirm: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/skills/setup/confirm", map[string]any{"layers": []any{
		map[string]any{"layer": "team", "digest": "x"}}}, nil); r.code != 400 {
		t.Fatalf("bad layer: %d %s", r.code, r.raw)
	}
}
