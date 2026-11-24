package server

import (
	"context"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
)

// One agent, one row in the support panel (2026-11-24): writes made with a
// session's own token, or with another token and an X-ARK-Session header
// naming the session, are listed on that session's row; a token agent
// without either stays an external agent.
func TestSupportMergesSessionTokenAgents(t *testing.T) {
	app, _ := localGPUApp(t)
	ctx := context.Background()
	r := gpuCall(t, app, "POST", "/api/agent-sessions", map[string]any{"platform": "echo", "title": "SandBox", "prompt": "hello"}, nil)
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
	sessionSecret, _, err := app.Org.CreateSessionToken(ctx, org.LocalUsername, sid, "write", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	personal := func(label string) string {
		tok, err := app.Org.CreateAgentToken(ctx, org.LocalUsername, pyjson.NewObject("label", label, "expires_days", int64(30)))
		if err != nil {
			t.Fatal(err)
		}
		return pyjson.Str(tok.Value("token"))
	}
	pasted, other := personal("Home starter prompt"), personal("laptop")
	write := func(secret, name, session, title string) {
		h := map[string]string{"Authorization": "Bearer " + secret, "X-Agent-Name": name}
		if session != "" {
			h["X-ARK-Session"] = session
		}
		if w := gpuCall(t, app, "POST", "/api/agent/v1/units", map[string]any{"type": "task", "title": title}, h); w.code != 200 {
			t.Fatalf("write %s: %d %s", name, w.code, w.raw)
		}
	}
	write(sessionSecret, "sandbox", "", "by the session token")
	write(pasted, "claude-sandbox", sid, "pasted token, session header")
	write(pasted, "claude-sandbox", sid, "again")
	write(other, "laptop", "", "an external agent")
	write(other, "spoof", "s_doesnotexist01", "a header naming no session")

	supportCache.Range(func(k, _ any) bool { supportCache.Delete(k); return true })
	got := gpuCall(t, app, "GET", "/api/project/support", nil, nil)
	if got.code != 200 {
		t.Fatalf("support: %d %s", got.code, got.raw)
	}
	agents := got.body["agents"].(map[string]any)
	sessions := agents["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions: %s", got.raw)
	}
	linked, _ := sessions[0].(map[string]any)["linked_agents"].([]any)
	via := map[string]string{}
	writes := map[string]float64{}
	for _, x := range linked {
		m := x.(map[string]any)
		via[m["actor"].(string)], _ = m["session_via"].(string)
		writes[m["actor"].(string)] = m["writes"].(float64)
	}
	if via["agent:local/sandbox"] != "token" || via["agent:local/claude-sandbox"] != "header" || len(linked) != 2 {
		t.Fatalf("linked agents: %s", got.raw)
	}
	if writes["agent:local/claude-sandbox"] != 2 {
		t.Fatalf("writes of the linked agent: %v", writes)
	}
	ext := map[string]map[string]any{}
	for _, x := range agents["external"].([]any) {
		m := x.(map[string]any)
		ext[m["actor"].(string)] = m
	}
	if len(ext) != 2 || ext["agent:local/laptop"] == nil || ext["agent:local/spoof"] == nil {
		t.Fatalf("external agents: %s", got.raw)
	}
	if ext["agent:local/spoof"]["session_id"] != nil {
		t.Fatalf("a header naming no session linked: %v", ext["agent:local/spoof"])
	}
}

// The likely-session guess: same person, the session's task, after it
// started, and only when exactly one session fits.
func TestSupportLikelySession(t *testing.T) {
	s1 := pyjson.NewObject("id", "s_aaaaaaaa", "title", "A", "status", "running", "owner", "ann", "task_ref", "T-0038",
		"created_at", "2026-11-24T04:00:00+00:00")
	w := pyjson.NewObject("actor", "agent:ann/claude", "unit_uid", "T-0038", "last_at", "2026-11-24T05:00:00+00:00")
	if got := supportLikelySession(w, []any{s1}); got != s1 {
		t.Fatal("one fitting session")
	}
	if supportLikelySession(pyjson.NewObject("actor", "agent:bob/x", "unit_uid", "T-0038", "last_at", "2026-11-24T05:00:00+00:00"), []any{s1}) != nil {
		t.Fatal("another person")
	}
	s2 := s1.Copy().Set("id", "s_bbbbbbbb")
	if supportLikelySession(w, []any{s1, s2}) != nil {
		t.Fatal("two candidates")
	}
	if supportLikelySession(pyjson.NewObject("actor", "agent:ann/claude", "unit_uid", "T-0001", "last_at", "2026-11-24T05:00:00+00:00"), []any{s1}) != nil {
		t.Fatal("another unit")
	}
}
