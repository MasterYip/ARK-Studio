package setup

import (
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func pathsOf(t *testing.T, e *Env, v *Viewer) *obj {
	t.Helper()
	p, err := e.Paths(v)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func missingOf(p *obj) map[string]bool {
	out := map[string]bool{}
	for _, x := range listOr(p.Value("missing")) {
		out[str(x)] = true
	}
	return out
}

// Paths resolve per device: the same task space has its own path on each
// device; a device without one reports it missing.
func TestPathsPerDevice(t *testing.T) {
	e := testEnv(t)
	mustSave(t, e, admin, "project", resourceRaw("tasks", "task_space", "Tasks",
		map[string]any{"kind": "ark", "access": "read-write"}, "E:/work/tasks"), true)
	mustSave(t, e, admin, "project", resourceRaw("repo", "repository", "Repo",
		map[string]any{"role": "primary", "access": "read-write"}, "E:/src/repo"), true)
	laptop := NewViewer(Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "laptop-1", HostSource: "header"})
	if _, err := e.AddLocation(laptop, "project/tasks", "/home/ana/tasks", "agent:ana", "", "laptop", ""); err != nil {
		t.Fatal(err)
	}
	p := pathsOf(t, e, admin)
	if ts := mapOf(p.Value("task_space")); ts == nil || ts.Value("path") != "E:/work/tasks" || len(missingOf(p)) != 0 {
		t.Fatalf("host-a: %s", pyjson.Dumps(p))
	}
	if r := mapOf(listOr(p.Value("repositories"))[0]); r.Value("path") != "E:/src/repo" {
		t.Fatalf("repo: %s", pyjson.Dumps(r))
	}
	p = pathsOf(t, e, laptop)
	if ts := mapOf(p.Value("task_space")); ts == nil || ts.Value("path") != "/home/ana/tasks" {
		t.Fatalf("laptop: %s", pyjson.Dumps(p))
	}
	if m := missingOf(p); !m["project/repo"] || m["task_space"] {
		t.Fatalf("laptop missing: %v", m)
	}
	// bob's new desktop has nothing.
	bobPC := NewViewer(Viewer{User: "bob", CanWrite: true, HostID: "bob-pc", HostSource: "header"})
	p = pathsOf(t, e, bobPC)
	if p.Value("task_space") != nil || !missingOf(p)["task_space"] {
		t.Fatalf("bob: %s", pyjson.Dumps(p))
	}
}

// Choosing an option writes the device's task space into the person's own
// layer; nobody else can answer for them; answering twice changes nothing.
func TestTaskSpaceDecisionWritesOwnLayer(t *testing.T) {
	e := testEnv(t)
	confirmBoth(t, e)
	pc := NewViewer(Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "ana-pc", HostSource: "header"})
	key, need, err := e.TaskSpaceDecisionTarget(pc)
	if err != nil || !need || key != "" {
		t.Fatalf("target %q %v %v", key, need, err)
	}
	opts := []TaskSpaceOption{{Path: "D:/ARK-tasks", Free: "812 GB"}, {Path: "E:/src/ARK-tasks", Free: "120 GB"}}
	if err := CheckTaskSpaceOptions(opts, 0); err != nil {
		t.Fatal(err)
	}
	if err := CheckTaskSpaceOptions([]TaskSpaceOption{{Path: "relative"}}, 0); err == nil {
		t.Fatal("relative path accepted")
	}
	if err := e.RegisterTaskSpaceDecision(pc, "D-0001", key, "ana-pc", opts, 0); err != nil {
		t.Fatal(err)
	}
	if d := mapOf(pathsOf(t, e, pc).Value("decision")); d == nil || d.Value("uid") != "D-0001" {
		t.Fatal("paths does not show the open decision")
	}
	// bob (another person, even a project member) cannot answer it.
	bob := NewViewer(Viewer{User: "bob", CanWrite: true, HostID: "ana-pc"})
	if _, err := e.ApplyTaskSpaceChoice(bob, "D-0001", "D:/ARK-tasks", "human:bob"); err == nil {
		t.Fatal("bob wrote ana's device layer")
	}
	// Not a task-space decision: nothing to do.
	if r, err := e.ApplyTaskSpaceChoice(pc, "D-0099", "x", "human:ana"); r != nil || err != nil {
		t.Fatal("unknown decision handled")
	}
	// The answer comes from ana's phone: the decision's device counts, not the answering one.
	phone := NewViewer(Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "ana-phone"})
	r, err := e.ApplyTaskSpaceChoice(phone, "D-0001", "`D:/ARK-tasks` — 812 GB free (recommended)", "human:ana")
	if err != nil || !pyjson.Truthy(r.Value("changed")) || r.Value("path") != "D:/ARK-tasks" {
		t.Fatalf("apply: %v %s", err, pyjson.Dumps(r))
	}
	p := pathsOf(t, e, pc)
	if ts := mapOf(p.Value("task_space")); ts == nil || ts.Value("path") != "D:/ARK-tasks" || ts.Value("key") != "u:ana/task-space" {
		t.Fatalf("after: %s", pyjson.Dumps(p))
	}
	if state, _ := layerState(t, e, "user", "ana"); state != "confirmed" {
		t.Fatalf("ana's layer is %s, want confirmed", state)
	}
	if pathsOf(t, e, phone).Value("task_space") != nil {
		t.Fatal("the phone got the PC's path")
	}
	r, err = e.ApplyTaskSpaceChoice(pc, "D-0001", "E:/src/ARK-tasks", "human:ana")
	if err != nil || pyjson.Truthy(r.Value("changed")) {
		t.Fatal("a second answer changed setup")
	}
	// A second device's decision fills the existing task space (location only).
	lap := NewViewer(Viewer{User: "ana", Admin: true, CanWrite: true, HostID: "ana-lap", HostSource: "header"})
	key, need, _ = e.TaskSpaceDecisionTarget(lap)
	if !need || key != "u:ana/task-space" {
		t.Fatalf("laptop target %q", key)
	}
	_ = e.RegisterTaskSpaceDecision(lap, "D-0002", key, "lap", []TaskSpaceOption{{Path: "/home/ana/tasks"}}, 0)
	if _, err := e.ApplyTaskSpaceChoice(lap, "D-0002", "1", "human:ana"); err != nil {
		t.Fatal(err)
	}
	if ts := mapOf(pathsOf(t, e, lap).Value("task_space")); ts == nil || ts.Value("path") != "/home/ana/tasks" {
		t.Fatal("laptop path")
	}
}

// Relocating setup locations: plan, apply, undo; only the viewer's layers.
func TestRelocateLocations(t *testing.T) {
	e := testEnv(t)
	mustSave(t, e, admin, "project", resourceRaw("tasks", "task_space", "Tasks",
		map[string]any{"kind": "ark", "access": "read-write"}, "E:/old/tasks"), true)
	mustSave(t, e, member, "user", resourceRaw("mine", "storage", "Mine",
		map[string]any{"kind": "artifacts"}, "E:/old/tasks/bob-art"), false)
	// bob may move only his own layer's location.
	plan, err := e.PlanLocationMoves(member, "", "E:/old/tasks", "F:/tasks")
	if err != nil || len(plan) != 1 || plan[0].Layer != "user" || plan[0].After != "F:/tasks/bob-art" {
		t.Fatalf("bob plan %v %+v", err, plan)
	}
	plan, _ = e.PlanLocationMoves(admin, "host-a", "E:/old/tasks", "F:/tasks")
	if len(plan) != 1 || plan[0].Layer != "project" {
		t.Fatalf("admin plan %+v", plan)
	}
	if _, err := e.ApplyLocationMoves(member, plan, "agent:bob", false); err == nil {
		t.Fatal("bob moved a project location")
	}
	done, err := e.ApplyLocationMoves(admin, plan, "agent:ana", false)
	if err != nil || len(done) != 1 {
		t.Fatal(err)
	}
	if ts := mapOf(pathsOf(t, e, admin).Value("task_space")); ts.Value("path") != "F:/tasks" {
		t.Fatal("not moved")
	}
	if again, _ := e.ApplyLocationMoves(admin, plan, "agent:ana", false); len(again) != 0 {
		t.Fatal("applied twice")
	}
	if _, err := e.ApplyLocationMoves(admin, plan, "human:ana", true); err != nil {
		t.Fatal(err)
	}
	if ts := mapOf(pathsOf(t, e, admin).Value("task_space")); ts.Value("path") != "E:/old/tasks" {
		t.Fatal("undo")
	}
	roots, _ := e.LogicalRoots(admin)
	if len(roots) != 1 || roots[0].Path != "E:/old/tasks" || roots[0].Space != "" {
		t.Fatalf("roots %+v", roots)
	}
}
