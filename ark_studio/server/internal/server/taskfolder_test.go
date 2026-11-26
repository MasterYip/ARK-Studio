package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/sessionstart"
	"arkstudio/go/internal/setup"
)

// The exact task folder (T-0128): a task names its own folder, a unit that
// belongs to a task (part_of / derived_from, through a middle unit) names
// that task's folder, an orphan names none.  GET setup/paths lists the
// writable places and, with ?unit=, the folder; an existing folder of the
// task is reused.  Task-space options in the OS temp dir are refused.
func TestTaskFolderResolution(t *testing.T) {
	app, _ := localGPUApp(t)
	ctx := context.Background()
	host := setup.MachineFingerprint()
	pc := map[string]string{"X-ARK-Host": host}
	call := func(method, path string, body any) gpuReply {
		t.Helper()
		r := gpuCall(t, app, method, path, body, pc)
		if r.code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, r.code, r.raw)
		}
		return r
	}
	// Options in the OS temp dir or a session folder are refused.
	for _, bad := range []string{filepath.ToSlash(filepath.Join(os.TempDir(), "ark-tasks")), "/data/ark/agent-sessions/s_1/workspace/tasks"} {
		r := gpuCall(t, app, "POST", "/api/skills/setup/task-space-decision", map[string]any{
			"options": []any{map[string]any{"path": bad}, map[string]any{"path": "/srv/ark-tasks"}}, "recommended": 1}, pc)
		if r.code != 400 || !strings.Contains(r.raw, "cannot be a task space") {
			t.Fatalf("option %s: %d %s", bad, r.code, r.raw)
		}
	}
	// A task space for this machine, chosen through the decision.
	root := filepath.ToSlash(filepath.Join(t.TempDir(), "tasks"))
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	r := call("POST", "/api/skills/setup/task-space-decision", map[string]any{
		"options": []any{map[string]any{"path": "/srv/ark-tasks"}, map[string]any{"path": "/srv/other-tasks"}}, "recommended": 0})
	dec := r.body["unit"].(map[string]any)["uid"].(string)
	call("POST", "/api/units/"+dec+"/comments", map[string]any{"body": root, "kind": "decided"})

	unit := func(typ, title string, links ...map[string]any) string {
		t.Helper()
		body := map[string]any{"type": typ, "title": title, "status": "active", "body": "x"}
		if len(links) > 0 {
			l := []any{}
			for _, x := range links {
				l = append(l, x)
			}
			body["links"] = l
		}
		return call("POST", "/api/units", body).body["unit"].(map[string]any)["uid"].(string)
	}
	q := unit("question", "Do the sensors drift?")
	task := unit("task", "Line-3 drift: analysis", map[string]any{"target": q, "relation": "part_of"})
	claim := unit("claim", "Drift above 2 %", map[string]any{"target": task, "relation": "part_of"})
	result := unit("experiment", "Drift per sensor", map[string]any{"target": claim, "relation": "derived_from"})
	orphan := unit("experiment", "Loose measurement", map[string]any{"target": q, "relation": "part_of"})

	paths := call("GET", setupPathsPath+"?unit="+result, nil).body
	w, _ := paths["writable"].([]any)
	if len(w) == 0 || w[0].(map[string]any)["path"] != root || w[0].(map[string]any)["kind"] != "task_space" {
		t.Fatalf("writable: %v", paths["writable"])
	}
	tf := paths["task_folder"].(map[string]any)
	path, _ := tf["path"].(string)
	if tf["task"] != task || tf["unit"] != result || tf["unit_type"] != "experiment" || !strings.HasPrefix(path, root+"/") ||
		!strings.Contains(path, "_"+task+"-line-3-drift-analysis") || tf["exists"] != true ||
		!strings.Contains(tf["note"].(string), "not a task; it belongs to task "+task) {
		t.Fatalf("result folder: %v", tf)
	}
	if !strings.Contains(paths["next"].(string), path) {
		t.Fatalf("next: %v", paths["next"])
	}
	own := call("GET", setupPathsPath+"?unit="+task, nil).body["task_folder"].(map[string]any)
	if own["path"] != path || own["task"] != task {
		t.Fatalf("task folder: %v", own)
	}
	// A unit with no task above it gets its own folder (T-0132).
	none := call("GET", setupPathsPath+"?unit="+orphan, nil).body["task_folder"].(map[string]any)
	op, _ := none["path"].(string)
	if none["task"] != nil || none["kind"] != "unit" || none["owner"] != orphan || !strings.HasPrefix(op, root+"/") ||
		!strings.Contains(op, "_"+orphan+"-loose-measurement") || !strings.Contains(none["note"].(string), "with no task above it") {
		t.Fatalf("orphan: %v", none)
	}
	qf := call("GET", setupPathsPath+"?unit="+q, nil).body["task_folder"].(map[string]any)
	if qf["kind"] != "unit" || !strings.Contains(qf["path"].(string), "_"+q+"-do-the-sensors-drift") {
		t.Fatalf("question: %v", qf)
	}
	// An existing folder of the task is reused (asking made one; here the
	// person renamed it).
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	old := root + "/20200101_" + task + "-old-name"
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	again := call("GET", setupPathsPath+"?unit="+claim, nil).body["task_folder"].(map[string]any)
	if again["path"] != old || again["exists"] != true {
		t.Fatalf("existing folder: %v", again)
	}
	// The session start names it, and its Codex roots include the task space.
	p, err := (&reqCtx{app: app, ctx: ctx}).currentProject()
	if err != nil {
		t.Fatal(err)
	}
	f := (&reqCtx{app: app, ctx: ctx}).sessionFolder(p, "", result)
	if f == nil || f.Path != old || f.Task != task || len(f.Writable) == 0 || f.Writable[0] != root {
		t.Fatalf("session folder: %+v", f)
	}
	st := sessionstart.Compose(sessionstart.Facts{Setup: sessionstart.Setup{Known: true, ProjectState: "confirmed"},
		Task: &sessionstart.Task{UID: result, Type: "experiment", Title: "Drift per sensor", Status: "active"}, Folder: f})
	if !strings.Contains(st.Text, "Start: continue the experiment "+result) ||
		!strings.Contains(st.Text, "The folder for "+result+" is "+task+"'s folder "+old) || !strings.Contains(st.Text, "setup/paths?unit=<UID>") {
		t.Fatalf("start:\n%s", st.Text)
	}
	// No unit at all: no folder (a free session has none until it picks a node).
	if none := (&reqCtx{app: app, ctx: ctx}).sessionFolder(p, "", ""); none == nil || none.Path != "" || none.TaskSpace != root {
		t.Fatalf("no unit: %+v", none)
	}
	// A session working on two tasks one after another gets two folders,
	// each made when asked for.
	second := unit("task", "Line-4 noise check", map[string]any{"target": q, "relation": "part_of"})
	f2 := call("GET", setupPathsPath+"?unit="+second, nil).body["task_folder"].(map[string]any)
	p2, _ := f2["path"].(string)
	if p2 == "" || p2 == old || f2["task"] != second || f2["exists"] != true || !strings.Contains(p2, "_"+second+"-line-4-noise-check") {
		t.Fatalf("second task: %v", f2)
	}
	if st, err := os.Stat(p2); err != nil || !st.IsDir() {
		t.Fatalf("second folder not made: %v", err)
	}
}
