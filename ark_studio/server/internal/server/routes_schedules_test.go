package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/schedules"
)

func stubScheduleRunner(t *testing.T, fn func(env *chat.Env, req chat.ScheduledRequest) (*chat.ScheduledResult, error)) {
	t.Helper()
	old := scheduleRunner
	scheduleRunner = fn
	t.Cleanup(func() { scheduleRunner = old })
}

func waitRun(t *testing.T, app *App, h map[string]string, id string) map[string]any {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		r := gpuCall(t, app, "GET", schedulesPath, nil, h)
		for _, it := range r.body["tasks"].([]any) {
			task := it.(map[string]any)
			st, _ := task["state"].(map[string]any)
			if task["id"] == id && st["last_status"] != nil && st["running_since"] == nil {
				return task
			}
		}
	}
	t.Fatal("run did not finish")
	return nil
}

// Admins save and run tasks; readers only read; a dry run files its
// proposals as one review unit; a task never runs twice at once; a failed
// run is listed.
func TestSchedulesRoutes(t *testing.T) {
	app, projectID := authDesktopApp(t)
	owner := withProject(sessionCookie(app, "owner"), projectID)
	vic := withProject(sessionCookie(app, "vic"), projectID)
	release := make(chan struct{})
	var seen chat.ScheduledRequest
	var seenActor string
	stubScheduleRunner(t, func(env *chat.Env, req chat.ScheduledRequest) (*chat.ScheduledResult, error) {
		<-release
		seen, seenActor = req, env.Actor
		if req.Prompt == "fail" {
			return nil, errors.New("model said no")
		}
		return &chat.ScheduledResult{Message: "Proposed one tag.", Steps: 2, Tokens: 900, Touched: []string{"T-0001"},
			Proposals: []any{pyjson.NewObject("op", "add_tags", "args", pyjson.NewObject("ref", "T-0001", "tags", []any{"branch:x"}))}}, nil
	})
	task := map[string]any{"name": "维护线标签", "prompt": "Tag units", "schedule": map[string]any{"kind": "daily", "at": "07:00"}, "enabled": true}
	if r := gpuCall(t, app, "POST", schedulesPath, map[string]any{"action": "save", "task": task}, vic); r.code != 403 {
		t.Fatalf("reader saves: %d %s", r.code, r.raw)
	}
	r := gpuCall(t, app, "POST", schedulesPath, map[string]any{"action": "save", "task": task}, owner)
	if r.code != 200 {
		t.Fatalf("save: %d %s", r.code, r.raw)
	}
	saved := r.body["tasks"].([]any)[0].(map[string]any)
	id := saved["id"].(string)
	if saved["owner"] != "owner" || saved["mode"] != "suggest" || saved["max_steps"] != float64(schedules.DefaultSteps) {
		t.Fatalf("saved: %v", saved)
	}
	if r := gpuCall(t, app, "GET", schedulesPath, nil, vic); r.code != 200 || len(r.body["tasks"].([]any)) != 1 || r.body["can_edit"] != false {
		t.Fatalf("reader list: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", schedulesPath, map[string]any{"action": "run", "id": id}, owner); r.code != 200 {
		t.Fatalf("run: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", schedulesPath, map[string]any{"action": "run", "id": id}, owner); r.code != 409 {
		t.Fatalf("second run while the first runs: %d %s", r.code, r.raw)
	}
	close(release)
	done := waitRun(t, app, owner, id)
	if done["state"].(map[string]any)["last_status"] != "ok" || !seen.Suggest || seenActor != scheduleActorPrefix+id {
		t.Fatalf("run: %v %+v %s", done, seen, seenActor)
	}
	runs := gpuCall(t, app, "GET", schedulesPath, nil, owner).body["runs"].([]any)
	run := runs[0].(map[string]any)
	review, _ := run["review"].(string)
	if run["status"] != "ok" || review == "" || run["proposals"] != float64(1) || run["trigger"] != "manual" {
		t.Fatalf("run log: %v", run)
	}
	unit := gpuCall(t, app, "GET", "/api/units/"+review, nil, owner)
	if u := unit.body["unit"].(map[string]any); u["status"] != "review" || u["type"] != "artifact" {
		t.Fatalf("review unit: %s", unit.raw)
	}
	if tr := gpuCall(t, app, "GET", scheduleTranscript+"?run="+run["id"].(string), nil, vic); tr.code != 200 && tr.code != 404 {
		t.Fatalf("transcript: %d %s", tr.code, tr.raw)
	}

	// A failing task is listed for Settings and Needs you.
	task["prompt"], task["id"] = "fail", id
	if r := gpuCall(t, app, "POST", schedulesPath, map[string]any{"action": "save", "task": task}, owner); r.code != 200 {
		t.Fatalf("resave: %d %s", r.code, r.raw)
	}
	gpuCall(t, app, "POST", schedulesPath, map[string]any{"action": "run", "id": id}, owner)
	waitRun(t, app, owner, id)
	failing := gpuCall(t, app, "GET", schedulesPath, nil, vic).body["failing"].([]any)
	if len(failing) != 1 || failing[0].(map[string]any)["error"] != "model said no" {
		t.Fatalf("failing: %v", failing)
	}

	// Bad input.
	for _, body := range []map[string]any{{"action": "save", "task": map[string]any{"name": "x", "prompt": "p", "schedule": map[string]any{"kind": "yearly"}}},
		{"action": "nope"}, {"action": "delete", "id": "st_missing"}} {
		if r := gpuCall(t, app, "POST", schedulesPath, body, owner); r.code == 200 {
			t.Fatalf("accepted %v", body)
		}
	}
	if r := gpuCall(t, app, "POST", schedulesPath, map[string]any{"action": "delete", "id": id}, owner); r.code != 200 || len(r.body["tasks"].([]any)) != 0 {
		t.Fatalf("delete: %d %s", r.code, r.raw)
	}
}

// The scheduler starts a due task once, as its owner; an owner who lost
// admin access makes the run fail instead of running with less access.
func TestScheduleTickCatchUpAndOwner(t *testing.T) {
	app, projectID := authDesktopApp(t)
	owner := withProject(sessionCookie(app, "owner"), projectID)
	runs := make(chan string, 8)
	stubScheduleRunner(t, func(env *chat.Env, req chat.ScheduledRequest) (*chat.ScheduledResult, error) {
		runs <- req.Name
		return &chat.ScheduledResult{Message: "ok"}, nil
	})
	task := map[string]any{"name": "hourly", "prompt": "p", "mode": "apply", "schedule": map[string]any{"kind": "hours", "every": 1}, "enabled": true}
	r := gpuCall(t, app, "POST", schedulesPath, map[string]any{"action": "save", "task": task}, owner)
	id := r.body["tasks"].([]any)[0].(map[string]any)["id"].(string)
	p := app.nudgeProject(context.Background(), projectID)
	// Last run three hours ago: due, and run once.
	if err := schedules.Update(p.Path, func(f *schedules.File) error {
		f.State[id] = schedules.State{LastRunAt: time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.scheduleTick(context.Background(), true)
	app.scheduleTick(context.Background(), false)
	waitRun(t, app, owner, id)
	app.scheduleTick(context.Background(), false)
	time.Sleep(200 * time.Millisecond)
	if n := len(runs); n != 1 {
		t.Fatalf("%d runs after a missed period", n)
	}
	// The owner is no longer admin: the next run fails.
	if err := schedules.Update(p.Path, func(f *schedules.File) error {
		f.Tasks[0].Owner = "vic"
		f.State[id] = schedules.State{LastRunAt: time.Now().UTC().Add(-3 * time.Hour).Format(time.RFC3339)}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.scheduleTick(context.Background(), false)
	done := waitRun(t, app, owner, id)
	if st := done["state"].(map[string]any); st["last_status"] != "failed" || len(runs) != 1 {
		t.Fatalf("lost access: %v", st)
	}
}

func TestScheduleGoOnlyRoutesListed(t *testing.T) {
	ids := map[string]bool{}
	for _, r := range GoOnlyRoutes {
		ids[r.ID] = true
	}
	for _, p := range []string{schedulesPath, scheduleTranscript, agentSchedulesPath} {
		if !ids["goonly:exact:"+p] {
			t.Errorf("%s not in GoOnlyRoutes", p)
		}
	}
}
