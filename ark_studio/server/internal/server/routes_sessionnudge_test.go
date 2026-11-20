package server

import (
	"context"
	"strings"
	"testing"

	"arkstudio/go/internal/org"
	"arkstudio/go/internal/sessionnudge"
)

// The nudger sees a person's comment on a unit they created, through the
// same feed the agents read; the project can switch nudges off.
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
	if !d.Enabled(pid) {
		t.Fatal("nudges are on by default")
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
	if err != nil || b.Epoch != epoch || len(b.Items) == 0 || !strings.Contains(sessionnudge.Text(b.Items), "Nudge me") {
		t.Fatalf("since: %v %+v", err, b)
	}
	if r = gpuCall(t, app, "PATCH", "/api/project/settings", map[string]any{"sessions": map[string]any{"nudge_idle": false}}, nil); r.code != 200 {
		t.Fatalf("settings: %d %s", r.code, r.raw)
	}
	if d.Enabled(pid) {
		t.Fatal("switched off")
	}
}
