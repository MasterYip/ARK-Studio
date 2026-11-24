package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sessionstart"
)

// The start of a launched session (T-0121): a project without confirmed
// setup starts with setup; a chosen task is continued with its open
// send-back comments; an agent of the session re-reads its start in
// GET /api/agent/v1/context.
func TestSessionStartPoint(t *testing.T) {
	app, _ := localGPUApp(t)
	ctx := context.Background()
	got := gpuCall(t, app, "GET", "/api/agent-sessions/start", nil, nil)
	if got.code != 200 || got.body["mode"] != "setup" || got.body["setup"].(map[string]any)["needs_setup"] != true {
		t.Fatalf("fresh project: %d %s", got.code, got.raw)
	}
	w := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "Render", "status": "active",
		"body": "## Progress\n- 2026-11-24 10:00 UTC · shot 3 rendered\n"}, nil)
	if w.code != 200 {
		t.Fatalf("unit: %d %s", w.code, w.raw)
	}
	uid := w.body["unit"].(map[string]any)["uid"].(string)
	if r := gpuCall(t, app, "POST", "/api/units/"+uid+"/comments", map[string]any{"body": "fix the colour", "kind": "sent_back"}, nil); r.code != 200 {
		t.Fatalf("comment: %d %s", r.code, r.raw)
	}
	got = gpuCall(t, app, "GET", "/api/agent-sessions/start?task="+uid, nil, nil)
	task, _ := got.body["task"].(map[string]any)
	if got.body["mode"] != "setup" || task == nil || task["send_backs"] != 1.0 || task["uid"] != uid {
		t.Fatalf("with a task: %s", got.raw)
	}
	p, err := (&reqCtx{app: app, ctx: ctx}).currentProject()
	if err != nil {
		t.Fatal(err)
	}
	facts := (&reqCtx{app: app, ctx: ctx}).sessionTaskFacts(p, uid)
	if facts == nil || len(facts.Open) != 1 || facts.Open[0].Kind != "sent_back" || !strings.Contains(facts.LastProgress, "shot 3") {
		t.Fatalf("task facts: %+v", facts)
	}
	st := sessionstart.Compose(sessionstart.Facts{Setup: sessionstart.Setup{Known: true, ProjectState: "confirmed"}, Task: facts})
	if st.Mode != "continue" || !strings.Contains(st.Text, "send-back by local: fix the colour") {
		t.Fatalf("continue start:\n%s", st.Text)
	}

	r := gpuCall(t, app, "POST", "/api/agent-sessions", map[string]any{"platform": "echo", "title": "S", "prompt": "hi"}, nil)
	if r.code != 200 {
		t.Fatalf("session: %d %s", r.code, r.raw)
	}
	sid := r.body["session"].(map[string]any)["id"].(string)
	t.Cleanup(func() {
		gpuCall(t, app, "PATCH", "/api/agent-sessions/"+sid, map[string]any{"closed": true}, nil)
		for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
			if s, _ := app.Sessions.Get(sid); s == nil || !app.Sessions.IsActive(s) {
				return
			}
		}
	})
	s, _ := app.Sessions.Get(sid)
	app.Sessions.SetMeta(s, "start", pyjson.NewObject("mode", "continue", "summary", "continue "+uid, "text", st.Text))
	h := map[string]string{"X-ARK-Token": app.AgentToken(), "X-ARK-Session": sid}
	c := gpuCall(t, app, "GET", "/api/agent/v1/context", nil, h)
	ss, _ := c.body["session_start"].(map[string]any)
	if c.code != 200 || ss == nil || ss["mode"] != "continue" || ss["session"] != sid {
		t.Fatalf("context: %d %v", c.code, c.body["session_start"])
	}
	delete(h, "X-ARK-Session")
	if c := gpuCall(t, app, "GET", "/api/agent/v1/context", nil, h); c.body["session_start"] != nil {
		t.Fatal("session_start without a session")
	}
}
