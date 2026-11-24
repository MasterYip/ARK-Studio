package server

import (
	"strings"
	"testing"
)

// A device without a task space: paths report it missing; the decision
// helper opens a Needs-you decision; the person's answer writes setup for
// that device, and paths then resolve.
func TestTaskSpaceDecisionFlow(t *testing.T) {
	app, _ := localGPUApp(t)
	call := func(method, path string, body any, h map[string]string) gpuReply {
		t.Helper()
		r := gpuCall(t, app, method, path, body, h)
		if r.code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, r.code, r.raw)
		}
		return r
	}
	pc := map[string]string{"X-ARK-Host": "a1b2c3d4e5f6"}
	r := call("GET", setupPathsPath, nil, pc)
	if miss, _ := r.body["missing"].([]any); len(miss) == 0 || miss[0] != "task_space" || r.body["task_space"] != nil {
		t.Fatalf("paths before: %s", r.raw)
	}
	// Without a device id the helper refuses (it would not know whose device).
	if bad := gpuCall(t, app, "POST", "/api/skills/setup/task-space-decision", map[string]any{
		"options": []any{map[string]any{"path": "D:/ARK-tasks"}}}, nil); bad.code != 400 {
		t.Fatalf("no host: %d %s", bad.code, bad.raw)
	}
	r = call("POST", "/api/skills/setup/task-space-decision", map[string]any{
		"options": []any{
			map[string]any{"path": "D:/ARK-tasks", "free_bytes": 812e9, "note": "largest drive"},
			map[string]any{"path": "E:/src/ARK-tasks", "free": "120 GB", "note": "next to the repo"},
		}, "recommended": 0}, pc)
	unit, _ := r.body["unit"].(map[string]any)
	uid, _ := unit["uid"].(string)
	body, _ := unit["body"].(string)
	if uid == "" || unit["type"] != "decision" || !strings.Contains(body, "## Options") ||
		!strings.Contains(body, "- `D:/ARK-tasks` — 812 GB free — largest drive (recommended)") {
		t.Fatalf("decision: %s", r.raw)
	}
	agent := map[string]string{"X-ARK-Host": "a1b2c3d4e5f6", "Authorization": "Bearer " + app.AgentToken()}
	if r = call("GET", agentSetupPathsPath, nil, agent); r.body["task_space"] != nil {
		t.Fatalf("agent paths: %s", r.raw)
	}
	r = call("GET", setupPathsPath, nil, pc)
	if d, _ := r.body["decision"].(map[string]any); d["uid"] != uid {
		t.Fatalf("paths shows no decision: %s", r.raw)
	}
	// The person answers in Needs you (comment kind "decided", then done).
	call("POST", "/api/units/"+uid+"/comments", map[string]any{"body": "E:/src/ARK-tasks — 120 GB free", "kind": "decided"}, nil)
	r = call("GET", setupPathsPath, nil, pc)
	ts, _ := r.body["task_space"].(map[string]any)
	if ts == nil || ts["path"] != "E:/src/ARK-tasks" {
		t.Fatalf("paths after: %s", r.raw)
	}
	r = call("GET", "/api/units/"+uid, nil, nil)
	if u, _ := r.body["unit"].(map[string]any); !strings.Contains(u["body"].(string), "Saved: the task space") {
		t.Fatalf("decision body: %s", r.raw)
	}
	// Another device of the same person still has none; this one now refuses a second decision.
	r = call("GET", setupPathsPath, nil, map[string]string{"X-ARK-Host": "ffeeddccbbaa"})
	if r.body["task_space"] != nil {
		t.Fatalf("other device: %s", r.raw)
	}
	if again := gpuCall(t, app, "POST", "/api/skills/setup/task-space-decision", map[string]any{
		"options": []any{map[string]any{"path": "D:/x/tasks"}}}, pc); again.code != 409 {
		t.Fatalf("second decision: %d %s", again.code, again.raw)
	}
}

// Relocate: dry run lists, apply rewrites bodies (code blocks kept) and
// setup in one go, a second apply changes nothing, undo restores.  The
// logical pass turns task-space paths into task: links.
func TestRelocateAndLogical(t *testing.T) {
	app, _ := localGPUApp(t)
	call := func(method, path string, body any, h map[string]string) gpuReply {
		t.Helper()
		r := gpuCall(t, app, method, path, body, h)
		if r.code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, r.code, r.raw)
		}
		return r
	}
	pc := map[string]string{"X-ARK-Host": "a1b2c3d4e5f6"}
	call("POST", "/api/skills/setup/resource", map[string]any{"layer": "project", "resource": map[string]any{
		"id": "tasks", "kind": "task_space", "name": "Tasks", "details": map[string]any{"kind": "ark", "access": "read-write"}}}, pc)
	call("POST", "/api/skills/setup/location", map[string]any{"resource": "project/tasks", "path": "E:/old/tasks", "scope": "project"}, pc)
	text := "## Resource paths\n- [Task folder](file:///E:/old/tasks/20261124_T-1/)\n```\nE:/old/tasks/keep\n```\n"
	u := call("POST", "/api/units", map[string]any{"type": "task", "title": "T", "body": text}, nil)
	uid := u.body["unit"].(map[string]any)["uid"].(string)

	dry := call("POST", "/api/skills/setup/relocate", map[string]any{"from": "E:/old/tasks", "to": "F:/tasks"}, pc)
	tot := dry.body["totals"].(map[string]any)
	if tot["units"] != float64(1) || tot["locations"] != float64(1) || dry.body["applied"] != false {
		t.Fatalf("dry run: %s", dry.raw)
	}
	if got := call("GET", "/api/units/"+uid, nil, nil).body["unit"].(map[string]any)["body"]; got != text {
		t.Fatal("dry run wrote")
	}
	ap := call("POST", "/api/skills/setup/relocate", map[string]any{"from": "E:/old/tasks", "to": "F:/tasks", "apply": true}, pc)
	id, _ := ap.body["relocation"].(string)
	body := call("GET", "/api/units/"+uid, nil, nil).body["unit"].(map[string]any)["body"].(string)
	if id == "" || !strings.Contains(body, "file:///F:/tasks/20261124_T-1/") || !strings.Contains(body, "E:/old/tasks/keep") {
		t.Fatalf("apply: %s\n%s", ap.raw, body)
	}
	paths := call("GET", setupPathsPath, nil, pc)
	if paths.body["task_space"].(map[string]any)["path"] != "F:/tasks" {
		t.Fatalf("setup not moved: %s", paths.raw)
	}
	again := call("POST", "/api/skills/setup/relocate", map[string]any{"from": "E:/old/tasks", "to": "F:/tasks", "apply": true}, pc)
	if again.body["written"] != float64(0) {
		t.Fatalf("not idempotent: %s", again.raw)
	}
	call("POST", "/api/skills/setup/relocate", map[string]any{"undo": id}, pc)
	if got := call("GET", "/api/units/"+uid, nil, nil).body["unit"].(map[string]any)["body"]; got != text {
		t.Fatalf("undo: %v", got)
	}
	if p := call("GET", setupPathsPath, nil, pc); p.body["task_space"].(map[string]any)["path"] != "E:/old/tasks" {
		t.Fatalf("undo setup: %s", p.raw)
	}
	// Absolute -> logical.
	lg := call("POST", "/api/skills/setup/relocate", map[string]any{"logical": true, "apply": true}, pc)
	body = call("GET", "/api/units/"+uid, nil, nil).body["unit"].(map[string]any)["body"].(string)
	if !strings.Contains(body, "[Task folder](task:20261124_T-1/)") || !strings.Contains(body, "E:/old/tasks/keep") {
		t.Fatalf("logical: %s\n%s", lg.raw, body)
	}
	// A whole drive is refused.
	if bad := gpuCall(t, app, "POST", "/api/skills/setup/relocate", map[string]any{"from": "E:/", "to": "F:/x"}, pc); bad.code != 400 {
		t.Fatalf("drive: %d", bad.code)
	}
}

func TestTaskSpaceRoutesListed(t *testing.T) {
	want := map[string]bool{"goonly:exact:" + agentSetupPathsPath: true, "goonly:exact:" + setupPathsPath: true}
	for _, r := range GoOnlyRoutes {
		delete(want, r.ID)
	}
	if len(want) > 0 {
		t.Fatalf("missing from GoOnlyRoutes: %v", want)
	}
}
