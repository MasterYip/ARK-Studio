package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/sessionnudge"
)

// The nudger sees a person's comment on a unit they created, through the
// same feed the agents read.  People's replies are not idle reminders: the
// old nudge_idle setting does not stop them (T-0109).
func TestNudgeDepsFeedAndSetting(t *testing.T) {
	app, _ := localGPUApp(t)
	r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "Nudge me"}, nil)
	if r.code != 200 {
		t.Fatalf("unit: %d %s", r.code, r.raw)
	}
	p, err := (&reqCtx{app: app, ctx: context.Background()}).currentProject()
	if err != nil || p == nil {
		t.Fatal(err)
	}
	pid := p.ID()
	d := app.NudgeDeps()
	ctx := context.Background()
	if rs := gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"sessions": map[string]any{"nudge_idle": false}}, nil); rs.code != 200 {
		t.Fatalf("settings: %d %s", rs.code, rs.raw)
	}
	if !(&reqCtx{app: app, ctx: context.Background()}).sessionHooks().WakesIdle(pid) {
		t.Fatal("sessions launched with nudge_idle off must still be woken by ARK")
	}
	epoch, top, err := d.Top(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	uid := r.body["unit"].(map[string]any)["uid"].(string)
	if r = gpuCall(t, app, "POST", "/api/units/"+uid+"/comments", map[string]any{"body": "please add a baseline"}, nil); r.code != 200 {
		t.Fatalf("comment: %d %s", r.code, r.raw)
	}
	b, err := d.Since(ctx, sessionnudge.Session{ProjectID: pid, User: org.LocalUsername}, top)
	if err != nil || b.Epoch != epoch || len(b.Items) == 0 || !strings.Contains(sessionnudge.Text(sessionnudge.Session{ID: "s", ProjectID: pid}, d.ProjectName(pid), b.Items), "Nudge me") {
		t.Fatalf("since: %v %+v", err, b)
	}
}

// stdoutOf is the session's recorded stdout lines, joined.
func stdoutOf(t *testing.T, app *App, id string) string {
	t.Helper()
	logs, err := app.Sessions.Logs(id, "stdout", 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	entries, _ := logs.Value("entries").([]any)
	for _, e := range entries {
		if o, ok := e.(*pyjson.Object); ok {
			b.WriteString(pyjson.Str(o.Value("line")) + "\n")
		}
	}
	return b.String()
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Regression (2026-11-23): a long-lived Claude Code session ARK started
// rests between turns while a background command (a training run) still
// runs.  A person's review answer reaches it then, and an answer given
// while a turn ran is still delivered after a server restart (the nudger's
// cursor is kept in the session record).
func TestNudgeReachesBusySessionAndSurvivesRestart(t *testing.T) {
	app, _ := localGPUApp(t)
	r := gpuCall(t, app, "POST", "/api/units", map[string]any{"type": "task", "title": "Wake me"}, nil)
	if r.code != 200 {
		t.Fatalf("unit: %d %s", r.code, r.raw)
	}
	uid := r.body["unit"].(map[string]any)["uid"].(string)
	p, err := (&reqCtx{app: app, ctx: context.Background()}).currentProject()
	if err != nil || p == nil {
		t.Fatal(err)
	}
	m := app.Sessions
	exe, _ := os.Executable()
	m.Presets = []*pyjson.Object{pyjson.NewObject("id", "claude", "label", "Claude Code", "available", true,
		"command", []any{exe, "-p", "{prompt}"}, "prompt_mode", "arg", "followup", false)}
	t.Setenv("ARK_FAKE_CLAUDE_STREAM", "1")
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "start background"), "http://127.0.0.1:1", org.LocalUsername)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.Close(s.ID)
		waitUntil(t, "session ended", func() bool { return !m.IsActive(s) })
	})
	m.SetMeta(s, "project", pyjson.NewObject("id", p.ID(), "name", "Nudge test"))
	waitUntil(t, "busy between turns", func() bool {
		return m.MetaValue(s, "turn_state") == "waiting" && m.Busy(s).Any()
	})

	n := &sessionnudge.Nudger{D: app.NudgeDeps()}
	ctx := context.Background()
	n.Tick(ctx) // first sight
	if r = gpuCall(t, app, "POST", "/api/units/"+uid+"/comments", map[string]any{"body": "please add a baseline"}, nil); r.code != 200 {
		t.Fatalf("comment: %d %s", r.code, r.raw)
	}
	if got := n.Tick(ctx); len(got) != 1 || got[0] != s.ID {
		t.Fatalf("a resting session with a background command is not woken: %v", got)
	}
	waitUntil(t, "the update in the session", func() bool {
		return strings.Contains(stdoutOf(t, app, s.ID), "echo: ARK UPDATE for ARK session "+s.ID)
	})
	waitUntil(t, "rest", func() bool { return m.MetaValue(s, "turn_state") == "waiting" })

	// A person answers, then the server restarts before the nudger looks:
	// the new nudger starts from the saved cursor, not from now.
	if r = gpuCall(t, app, "POST", "/api/units/"+uid+"/comments", map[string]any{"body": "and a second seed"}, nil); r.code != 200 {
		t.Fatalf("comment: %d %s", r.code, r.raw)
	}
	restarted := &sessionnudge.Nudger{D: app.NudgeDeps()}
	if got := restarted.Tick(ctx); len(got) != 1 || got[0] != s.ID {
		t.Fatalf("an answer from before the restart is lost: %v", got)
	}
	waitUntil(t, "the second update", func() bool {
		return strings.Contains(stdoutOf(t, app, s.ID), "and a second seed")
	})
	if got := restarted.Tick(ctx); len(got) != 0 {
		t.Fatalf("delivered twice: %v", got)
	}
}
