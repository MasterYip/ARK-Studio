package server

import (
	"context"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

// GET /api/project/support (Go only): the Board and Home support panel's
// one read — sessions, recent agents, hosts, library and deadlines.
func TestProjectSupportLiveData(t *testing.T) {
	app, projectPath := localGPUApp(t)
	registerGPU(t, projectPath, "a")
	ctx := context.Background()
	p, err := (&reqCtx{app: app, ctx: ctx}).currentProject()
	if err != nil || p == nil {
		t.Fatalf("no project: %v", err)
	}

	empty := gpuCall(t, app, "GET", "/api/project/support", nil, nil)
	if empty.code != 200 || empty.body["can_write"] != true {
		t.Fatalf("support: %d %s", empty.code, empty.raw)
	}
	agents := empty.body["agents"].(map[string]any)
	if len(agents["sessions"].([]any)) != 0 || len(agents["external"].([]any)) != 0 {
		t.Fatalf("fresh project has agents: %s", empty.raw)
	}
	supportCache.Range(func(k, _ any) bool { supportCache.Delete(k); return true })

	// An ARK session, an agent's write, a token that expires soon, a
	// review that has waited for days and a device that read setup.
	r := gpuCall(t, app, "POST", "/api/agent-sessions", map[string]any{"platform": "echo", "title": "Stub run", "prompt": "hello"}, nil)
	if r.code != 200 {
		t.Fatalf("start session: %d %s", r.code, r.raw)
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
	w := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": "Agent task"},
		map[string]string{"X-ARK-Token": app.AgentToken(), "X-Agent-Name": "claude"})
	if w.code != 200 {
		t.Fatalf("agent write: %d %s", w.code, w.raw)
	}
	taskUID := w.body["unit"].(map[string]any)["uid"].(string)
	tok, err := app.Org.CreateAgentToken(ctx, org.LocalUsername, pyjson.NewObject("label", "laptop agent", "expires_days", int64(30)))
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	tokenID := pyjson.Str(tok.Value("item").(*pyjson.Object).Value("id"))
	if _, _, err := app.Org.CreateSessionToken(ctx, org.LocalUsername, "s_x", "read", 24*time.Hour); err != nil {
		t.Fatalf("session token: %v", err)
	}
	soon := clock.Format(time.Now().Add(5 * 24 * time.Hour))
	if err := app.Org.DB.Write(ctx, func(q sqlitedb.Querier) error {
		_, err := q.ExecContext(ctx, "UPDATE agent_tokens SET expires_at=? WHERE id=?", soon, tokenID)
		return err
	}, nil); err != nil {
		t.Fatal(err)
	}
	rv := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "Waiting review", "status": "review"}, nil)
	if rv.code != 200 {
		t.Fatalf("review unit: %d %s", rv.code, rv.raw)
	}
	reviewUID := rv.body["unit"].(map[string]any)["uid"].(string)
	old := clock.Format(time.Now().Add(-5 * 24 * time.Hour))
	if err := p.Store.DB.Write(ctx, func(q sqlitedb.Querier) error {
		res, err := q.ExecContext(ctx, "UPDATE review_rounds SET opened_at=? WHERE unit_id=(SELECT id FROM units WHERE uid=?)", old, reviewUID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			_, err = q.ExecContext(ctx, "INSERT INTO review_rounds(unit_id,round,opened_at,opened_by,base_at) "+
				"SELECT id,1,?,'local',? FROM units WHERE uid=?", old, old, reviewUID)
		}
		return err
	}, nil); err != nil {
		t.Fatal(err)
	}
	if g := gpuCall(t, app, "GET", "/api/skills/setup/state", nil, map[string]string{"X-ARK-Host": "a1b2c3d4e5f6"}); g.code != 200 {
		t.Fatalf("setup state: %d %s", g.code, g.raw)
	}

	got := gpuCall(t, app, "GET", "/api/project/support", nil, nil)
	if got.code != 200 {
		t.Fatalf("support: %d %s", got.code, got.raw)
	}
	agents = got.body["agents"].(map[string]any)
	sessions := agents["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions: %s", got.raw)
	}
	s := sessions[0].(map[string]any)
	if s["id"] != sid || s["title"] != "Stub run" || s["platform"] != "echo" || s["device"] != "ark" {
		t.Fatalf("session item: %v", s)
	}
	if counts := agents["session_counts"].(map[string]any); counts["total"] != 1.0 {
		t.Fatalf("session counts: %v", counts)
	}
	external := agents["external"].([]any)
	if len(external) != 1 {
		t.Fatalf("external agents: %s", got.raw)
	}
	e := external[0].(map[string]any)
	if e["actor"] != "agent:claude" || e["label"] != "claude" || e["unit_uid"] != taskUID || e["writes"].(float64) < 1 {
		t.Fatalf("external agent: %v", e)
	}

	hosts := got.body["hosts"].([]any)
	kinds := map[string]map[string]any{}
	for _, h := range hosts {
		m := h.(map[string]any)
		kinds[m["kind"].(string)+"|"+m["label"].(string)] = m
	}
	var ark, gpuA, device map[string]any
	for k, m := range kinds {
		switch {
		case m["kind"] == "ark":
			ark = m
		case k == "gpu-server|a":
			gpuA = m
		case m["device_id"] == "a1b2c3d4e5f6":
			device = m
		}
	}
	if ark == nil || ark["state"] != "online" {
		t.Fatalf("ARK server host: %s", got.raw)
	}
	if gpuA == nil || gpuA["state"] != "unknown" || gpuA["gpu"] != nil {
		t.Fatalf("GPU server (never probed): %v", gpuA)
	}
	if _, ok := kinds["gpu-server|b"]; ok {
		t.Fatalf("an unregistered GPU server is listed: %s", got.raw)
	}
	if device == nil || device["state"] != "online" || device["last_seen"] == nil {
		t.Fatalf("setup device: %s", got.raw)
	}

	lib := got.body["library"].(map[string]any)
	if lib["compute"] != 1.0 || lib["total"] != 1.0 {
		t.Fatalf("library: %v", lib)
	}

	var token, review map[string]any
	for _, d := range got.body["deadlines"].([]any) {
		m := d.(map[string]any)
		switch m["kind"] {
		case "token":
			if token != nil {
				t.Fatalf("session token listed: %s", got.raw)
			}
			token = m
		case "review":
			review = m
		}
	}
	if token == nil || token["id"] != tokenID || token["label"] != "laptop agent" || token["at"] != soon {
		t.Fatalf("expiring token: %s", got.raw)
	}
	if _, leaked := token["token"]; leaked {
		t.Fatal("a secret in the reply")
	}
	if review == nil || review["uid"] != reviewUID {
		t.Fatalf("review wait: %s", got.raw)
	}

	// Kept for a few seconds: the same answer without a new read.
	again := gpuCall(t, app, "GET", "/api/project/support", nil, nil)
	if again.body["generated_at"] != got.body["generated_at"] {
		t.Fatal("a second read within the cache window rebuilt the reply")
	}
}
