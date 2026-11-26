package server

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"arkstudio/go/internal/sessionnudge"
	"arkstudio/go/internal/sessions"
)

// Routing of people's updates (agentwatch routing.go): an answer on a unit
// wakes the agent that wrote the unit, not every session of the project.

// agentAs are the headers of an agent request from an ARK session (sid) or
// from an agent outside ARK with its own session id (agentSession).
func agentAs(app *App, sid, agentSession string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + app.AgentToken()}
	if sid != "" {
		h["X-ARK-Session"] = sid
	}
	if agentSession != "" {
		h["X-ARK-Agent-Session"] = agentSession
	}
	return h
}

func agentUnit(t *testing.T, app *App, headers map[string]string, body map[string]any) string {
	t.Helper()
	r := gpuCall(t, app, "POST", "/api/agent/v1/units", body, headers)
	if r.code != 200 {
		t.Fatalf("agent unit: %d %s", r.code, r.raw)
	}
	return r.body["unit"].(map[string]any)["uid"].(string)
}

func personComment(t *testing.T, app *App, uid string, body map[string]any) {
	t.Helper()
	if r := gpuCall(t, app, "POST", "/api/units/"+uid+"/comments", body, nil); r.code != 200 {
		t.Fatalf("comment: %d %s", r.code, r.raw)
	}
}

// tickRested ticks the nudger and waits until the woken sessions rest again.
func tickRested(t *testing.T, app *App, n *sessionnudge.Nudger, want ...string) {
	t.Helper()
	got := n.Tick(context.Background())
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("woken %v, want %v", got, want)
	}
	for _, id := range got {
		s, _ := app.Sessions.Get(id)
		waitUntil(t, "the update in "+id, func() bool { return strings.Contains(stdoutOf(t, app, id), "echo: ARK UPDATE") })
		waitUntil(t, "rest", func() bool { return app.Sessions.MetaValue(s, "turn_state") == "waiting" })
	}
}

func twoSessions(t *testing.T) (*jobWakeApp, *sessions.Session, *sessions.Session, *sessionnudge.Nudger) {
	w := newJobWakeApp(t)
	s1 := w.startSession(t, "hello")
	s2 := w.startSession(t, "hello")
	n := &sessionnudge.Nudger{D: w.app.NudgeDeps()}
	n.Tick(context.Background()) // first sight
	return w, s1, s2, n
}

// Two ARK sessions in one project: Accept on the unit session A handed to
// review wakes A alone, and says why; B hears nothing.
func TestUpdateOnAUnitWakesOnlyTheSessionThatWroteIt(t *testing.T) {
	w, s1, s2, n := twoSessions(t)
	uid := agentUnit(t, w.app, agentAs(w.app, s1.ID, ""), map[string]any{"type": "task", "title": "Sweep A",
		"status": "review", "review_note": "Check the sweep settings and the run list."})
	personComment(t, w.app, uid, map[string]any{"body": "Looks right", "kind": "accepted"})
	tickRested(t, w.app, n, s1.ID)
	out := stdoutOf(t, w.app, s1.ID)
	if !strings.Contains(out, "(you submitted "+uid+")") || !strings.Contains(out, "on units this session worked on") ||
		strings.Contains(out, "ignore this message") {
		t.Fatalf("wake text:\n%s", out)
	}
	if out := stdoutOf(t, w.app, s2.ID); strings.Contains(out, "ARK UPDATE") {
		t.Fatalf("the other session was woken:\n%s", out)
	}
}

// An update on a unit no agent session wrote goes to every session, as
// before; so does one on a unit whose writer session ended.
func TestUpdateWithoutOwnerFallsBackToTheProject(t *testing.T) {
	w, s1, s2, n := twoSessions(t)
	r := gpuCall(t, w.app, "POST", "/api/units", map[string]any{"type": "task", "title": "Person's task"}, nil)
	if r.code != 200 {
		t.Fatalf("unit: %d %s", r.code, r.raw)
	}
	personComment(t, w.app, r.body["unit"].(map[string]any)["uid"].(string), map[string]any{"body": "anyone?"})
	tickRested(t, w.app, n, s1.ID, s2.ID)
	if out := stdoutOf(t, w.app, s1.ID); !strings.Contains(out, "ignore this message") {
		t.Fatalf("a project-wide update must keep the ignore line:\n%s", out)
	}

	s3 := w.startSession(t, "hello")
	uid := agentUnit(t, w.app, agentAs(w.app, s3.ID, ""), map[string]any{"type": "task", "title": "Orphan"})
	n.Tick(context.Background()) // s3: first sight
	if _, err := w.app.Sessions.Close(s3.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "s3 ended", func() bool { return !w.app.Sessions.IsActive(s3) })
	personComment(t, w.app, uid, map[string]any{"body": "who takes this?"})
	tickRested(t, w.app, n, s1.ID, s2.ID)
}

// A reply goes to the session that wrote the comment it answers, not to
// the session that created the unit.
func TestCommentReplyGoesToTheCommentAuthorsSession(t *testing.T) {
	w, s1, s2, n := twoSessions(t)
	uid := agentUnit(t, w.app, agentAs(w.app, s1.ID, ""), map[string]any{"type": "task", "title": "Shared"})
	r := gpuCall(t, w.app, "POST", "/api/agent/v1/units/"+uid+"/comments", map[string]any{"body": "Which seed?"}, agentAs(w.app, s2.ID, ""))
	if r.code != 200 {
		t.Fatalf("agent comment: %d %s", r.code, r.raw)
	}
	cid := int64(r.body["comment"].(map[string]any)["id"].(float64))
	personComment(t, w.app, uid, map[string]any{"body": "Seed 2", "reply_to": cid})
	tickRested(t, w.app, n, s2.ID)
	if out := stdoutOf(t, w.app, s2.ID); !strings.Contains(out, fmt.Sprintf("(reply to your comment #%d)", cid)) {
		t.Fatalf("wake text:\n%s", out)
	}
	if out := stdoutOf(t, w.app, s1.ID); strings.Contains(out, "ARK UPDATE") {
		t.Fatalf("the unit's creator was woken for a reply to another session:\n%s", out)
	}
}

// One ARK session and one agent outside ARK (its own session id) in one
// project: each sees every update in the feed, for_you only on its own
// units; the watcher's long poll (for_you=1) wakes only for its own; the
// ARK session is woken only for its own.
func TestForYouForARKSessionAndThirdPartyAgent(t *testing.T) {
	w := newJobWakeApp(t)
	s1 := w.startSession(t, "hello")
	n := &sessionnudge.Nudger{D: w.app.NudgeDeps()}
	n.Tick(context.Background())
	ark, outside := agentAs(w.app, s1.ID, ""), agentAs(w.app, "", "cc-7f3a")
	head := gpuCall(t, w.app, "GET", "/api/agent/v1/updates", nil, outside)
	cursor, _ := head.body["cursor"].(string)
	if head.code != 200 || cursor == "" {
		t.Fatalf("feed head: %d %s", head.code, head.raw)
	}
	mineA := agentUnit(t, w.app, ark, map[string]any{"type": "task", "title": "ARK session unit"})
	mineB := agentUnit(t, w.app, outside, map[string]any{"type": "task", "title": "Outside agent unit"})

	// Only the ARK session's unit is answered: the outside watcher sleeps.
	personComment(t, w.app, mineA, map[string]any{"body": "for the session"})
	r := gpuCall(t, w.app, "GET", "/api/agent/v1/updates?mine=1&for_you=1&wait=1&since="+cursor, nil, outside)
	if items, _ := r.body["items"].([]any); r.code != 200 || len(items) != 0 {
		t.Fatalf("the outside watcher woke for the session's unit: %d %s", r.code, r.raw)
	}
	tickRested(t, w.app, n, s1.ID)

	personComment(t, w.app, mineB, map[string]any{"body": "for the outside agent"})
	r = gpuCall(t, w.app, "GET", "/api/agent/v1/updates?mine=1&for_you=1&wait=5&since="+cursor, nil, outside)
	items, _ := r.body["items"].([]any)
	if r.code != 200 || len(items) != 1 || items[0].(map[string]any)["uid"] != mineB ||
		items[0].(map[string]any)["reason"] != "you created "+mineB {
		t.Fatalf("outside watcher: %d %s", r.code, r.raw)
	}
	if got := n.Tick(context.Background()); len(got) != 0 {
		t.Fatalf("the ARK session was woken for the outside agent's unit: %v", got)
	}

	// The full feed: both updates for both agents, for_you on their own.
	check := func(who string, h map[string]string, own string) {
		t.Helper()
		r := gpuCall(t, w.app, "GET", "/api/agent/v1/updates?since="+cursor, nil, h)
		items, _ := r.body["items"].([]any)
		if r.code != 200 || len(items) != 2 || r.body["for_you_count"] != float64(1) {
			t.Fatalf("%s feed: %d %s", who, r.code, r.raw)
		}
		for _, raw := range items {
			it := raw.(map[string]any)
			if want := it["uid"] == own; it["for_you"] != want || it["audience"] != "session" {
				t.Fatalf("%s item %v: for_you %v, want %v", who, it["uid"], it["for_you"], want)
			}
		}
	}
	check("ARK session", ark, mineA)
	check("outside agent", outside, mineB)
}
