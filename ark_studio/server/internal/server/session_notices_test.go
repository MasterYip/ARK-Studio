package server

// Go only: GET/POST /api/agent-sessions/notices (session_notices.go,
// sessions/finish.go, org/sessionseen.go) and the finish items of the phone
// feed (notifications.go).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sessions"
)

type noticesReply struct {
	Items []struct {
		ID, Title, Outcome, At, Reason string
	} `json:"items"`
	Done   int             `json:"done"`
	Errors int             `json:"errors"`
	Prefs  map[string]bool `json:"prefs"`
}

func TestUnseenFinishes(t *testing.T) {
	f := func(id, owner, outcome, at string) sessions.Finish {
		return sessions.Finish{ID: id, Owner: owner, ProjectID: "p1", Outcome: outcome, At: at, Main: true}
	}
	busy := f("s_busy0001", "ana", "done", "2026-11-26T10:00:00Z")
	busy.Busy = true
	child := f("s_child001", "ana", "done", "2026-11-26T10:00:00Z")
	child.Main = false
	closed := f("s_closed01", "ana", "done", "2026-11-26T10:00:00Z")
	closed.Closed = true
	other := f("s_other001", "ana", "done", "2026-11-26T10:00:00Z")
	other.ProjectID = "p2"
	all := []sessions.Finish{
		f("s_done0001", "ana", "done", "2026-11-26T10:00:00Z"),
		f("s_fail0001", "human:web:ana", "error", "2026-11-26T10:01:00Z"),
		f("s_seen0001", "ana", "done", "2026-11-26T09:00:00Z"),
		f("s_again001", "ana", "done", "2026-11-26T11:00:00Z"),
		f("s_bobs0001", "bob", "done", "2026-11-26T10:00:00Z"),
		f("s_none0001", "ana", "", ""),
		busy, child, closed, other,
	}
	seen := map[string]string{"s_seen0001": "2026-11-26T09:00:00Z", "s_again001": "2026-11-26T10:30:00Z"}
	got := unseenFinishes(all, "p1", "ana", true, seen)
	ids := []string{}
	for _, x := range got {
		ids = append(ids, x.ID)
	}
	want := []string{"s_again001", "s_fail0001", "s_done0001"} // newest first
	if len(ids) != len(want) {
		t.Fatalf("unseen: %v", ids)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("unseen: %v want %v", ids, want)
		}
	}
	// Without sign-in every session counts; all projects with "".
	if n := len(unseenFinishes(all, "", "", false, nil)); n != 6 {
		t.Errorf("no auth, all projects: %d", n)
	}
}

func TestSessionNoticesPerUser(t *testing.T) {
	app, team := authDesktopApp(t)
	ctx := context.Background()
	ownerID, _ := app.Org.UserIDFor(ctx, "owner")
	anaID, _ := app.Org.UserIDFor(ctx, "ana")
	if err := app.Org.SetProjectGrant(ctx, team, anaID, "write", ownerID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	pinned := now
	clock.SetReplay(&pinned)
	t.Cleanup(func() { clock.SetReplay(nil) })
	at := func(d time.Duration) string { return clock.Format(now.Add(d)) }
	in := func(user string) map[string]string {
		h := sessionCookie(app, user)
		h["X-ARK-Project"] = team
		return h
	}
	root := filepath.Join(filepath.Dir(app.Root), "sessions")
	project := map[string]any{"id": team, "name": "Team"}
	turnEnd := func(outcome, when, reason string) map[string]any {
		return map[string]any{"at": when, "outcome": outcome, "reason": reason}
	}
	writeSession(t, root, "s_ownerdone1", map[string]any{"title": "WM-HOI", "actor": "owner", "project": project,
		"status": "stopped", "exit_code": 0, "ended_at": at(-time.Minute), "turn_end": turnEnd("done", at(-time.Minute), "")})
	writeSession(t, root, "s_ownerfail1", map[string]any{"title": "SandBox", "actor": "owner", "project": project,
		"status": "errored", "ended_at": at(-2 * time.Minute), "error": "process exited with code 1",
		"turn_end": turnEnd("error", at(-2*time.Minute), "API Error: 529 overloaded")})
	writeSession(t, root, "s_anadone001", map[string]any{"title": "Hers", "actor": "ana", "project": project,
		"status": "stopped", "exit_code": 0, "ended_at": at(-time.Minute), "turn_end": turnEnd("done", at(-time.Minute), "")})
	// A session an agent started: not the main conversation.
	writeSession(t, root, "s_ownerchild", map[string]any{"title": "Helper", "actor": "owner", "project": project,
		"parent_id": "s_ownerdone1", "relation": "", "status": "stopped", "exit_code": 0, "ended_at": at(-time.Minute),
		"turn_end": turnEnd("done", at(-time.Minute), "")})
	mgr, err := sessions.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	app.Sessions = mgr

	read := func(user string) noticesReply {
		t.Helper()
		r := gpuCall(t, app, "GET", sessionNoticesPath, nil, in(user))
		if r.code != 200 {
			t.Fatalf("notices %s: %d %s", user, r.code, r.raw)
		}
		var out noticesReply
		if err := json.Unmarshal([]byte(r.raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	post := func(user string, body map[string]any) noticesReply {
		t.Helper()
		r := gpuCall(t, app, "POST", sessionNoticesPath, body, in(user))
		if r.code != 200 {
			t.Fatalf("post notices %s: %d %s", user, r.code, r.raw)
		}
		var out noticesReply
		if err := json.Unmarshal([]byte(r.raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	o := read("owner")
	if o.Done != 1 || o.Errors != 1 || len(o.Items) != 2 || o.Items[0].ID != "s_ownerdone1" ||
		o.Items[1].Reason != "API Error: 529 overloaded" || !o.Prefs["done"] || !o.Prefs["error"] {
		t.Fatalf("owner notices: %+v", o)
	}
	a := read("ana")
	if a.Done != 1 || a.Errors != 0 || a.Items[0].ID != "s_anadone001" {
		t.Fatalf("ana notices: %+v", a)
	}

	// The phone feed: the owner's finishes after the grace, with the reason.
	if f := readFeed(t, app, "", sessionCookie(app, "owner")); kinds(f.Items)["session_done"] != 1 || kinds(f.Items)["session_error"] != 1 {
		t.Fatalf("owner feed: %+v", f.Items)
	}

	// The owner opens WM-HOI: seen for the owner only.
	o = post("owner", map[string]any{"seen": []any{"s_ownerdone1"}})
	if o.Done != 0 || o.Errors != 1 {
		t.Fatalf("after seen: %+v", o)
	}
	if a := read("ana"); a.Done != 1 {
		t.Fatalf("ana is not affected: %+v", a)
	}
	// ana may not mark the owner's session (not hers to see).
	if r := gpuCall(t, app, "POST", sessionNoticesPath, map[string]any{"seen": []any{"s_ownerfail1"}}, in("ana")); r.code == 200 {
		t.Errorf("ana marked the owner's session: %s", r.raw)
	}
	if o := read("owner"); o.Errors != 1 {
		t.Fatalf("owner's error after ana's attempt: %+v", o)
	}
	f := readFeed(t, app, "", sessionCookie(app, "owner"))
	if k := kinds(f.Items); k["session_done"] != 0 || k["session_error"] != 1 {
		t.Fatalf("feed after seen: %+v", f.Items)
	}
	for _, it := range f.Items {
		if it.Kind == "session_error" && (it.Title != "SandBox" || it.Body != "API Error: 529 overloaded" || it.Link["ref"] != "s_ownerfail1") {
			t.Errorf("error item: %+v", it)
		}
	}

	// A later finish of a seen session is new again.
	writeSession(t, root, "s_ownerdone1", map[string]any{"title": "WM-HOI", "actor": "owner", "project": project,
		"status": "stopped", "exit_code": 0, "ended_at": at(time.Minute), "turn_end": turnEnd("done", at(time.Minute), "")})
	if mgr, err = sessions.Open(root); err != nil {
		t.Fatal(err)
	}
	app.Sessions = mgr
	if o := read("owner"); o.Done != 1 {
		t.Fatalf("a new finish: %+v", o)
	}
	// Within the grace the phone does not hear of it (the browser may be on it).
	pinned = now.Add(time.Minute + 5*time.Second)
	if k := kinds(readFeed(t, app, "", sessionCookie(app, "owner")).Items); k["session_done"] != 0 {
		t.Fatalf("within the grace: %v", k)
	}
	pinned = now.Add(2 * time.Minute)

	// The switches: per person, saved on the server; they mute the feed, not the bubbles.
	o = post("owner", map[string]any{"prefs": map[string]any{"done": false}})
	if o.Prefs["done"] || !o.Prefs["error"] || o.Done != 1 {
		t.Fatalf("prefs: %+v", o)
	}
	if a := read("ana"); !a.Prefs["done"] {
		t.Fatalf("ana's switches: %+v", a.Prefs)
	}
	if k := kinds(readFeed(t, app, "", sessionCookie(app, "owner")).Items); k["session_done"] != 0 || k["session_error"] != 1 {
		t.Fatalf("done switched off: %v", k)
	}
	post("owner", map[string]any{"prefs": map[string]any{"error": false}})
	if k := kinds(readFeed(t, app, "", sessionCookie(app, "owner")).Items); k["session_error"] != 0 {
		t.Fatalf("error switched off: %v", k)
	}
	if r := gpuCall(t, app, "POST", sessionNoticesPath, map[string]any{"seen": "s_x"}, in("owner")); r.code != 400 {
		t.Errorf("bad seen: %d %s", r.code, r.raw)
	}
}
