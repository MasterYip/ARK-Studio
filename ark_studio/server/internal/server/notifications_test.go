package server

// Go only: GET /api/notifications (notifications.go, sessions/alerts.go).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sessions"
)

type feedItem struct {
	ID       string         `json:"id"`
	Kind     string         `json:"kind"`
	Priority string         `json:"priority"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Count    int            `json:"count"`
	Created  string         `json:"created_at"`
	Project  map[string]any `json:"project"`
	Link     map[string]any `json:"deep_link"`
}

type feedReply struct {
	Cursor   string           `json:"cursor"`
	Scope    string           `json:"scope"`
	Projects []map[string]any `json:"projects"`
	Items    []feedItem       `json:"items"`
	Active   []string         `json:"active"`
}

func readFeed(t *testing.T, app *App, query string, headers map[string]string) feedReply {
	t.Helper()
	r := gpuCall(t, app, "GET", "/api/notifications"+query, nil, headers)
	if r.code != 200 {
		t.Fatalf("feed%s: %d %s", query, r.code, r.raw)
	}
	var out feedReply
	if err := json.Unmarshal([]byte(r.raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func kinds(items []feedItem) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it.Kind]++
	}
	return out
}

// writeSession puts a session folder like the session manager writes it.
func writeSession(t *testing.T, root, id string, meta map[string]any) {
	t.Helper()
	meta["id"] = id
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationFeed(t *testing.T) {
	app, team := authDesktopApp(t)
	ctx := context.Background()
	ownerID, _ := app.Org.UserIDFor(ctx, "owner")
	anaID, _ := app.Org.UserIDFor(ctx, "ana")
	if err := app.Org.SetProjectGrant(ctx, team, anaID, "write", ownerID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	stepBy := func(d time.Duration) { now = now.Add(d); pinned := now; clock.SetReplay(&pinned) }
	step := func() { stepBy(2 * time.Second) }
	step()
	t.Cleanup(func() { clock.SetReplay(nil) })
	in := func(user, project string) map[string]string {
		h := sessionCookie(app, user)
		h["X-ARK-Project"] = project
		return h
	}
	// A second project of the owner only.
	r := gpuCall(t, app, "POST", "/api/projects", map[string]any{"name": "Solo"}, sessionCookie(app, "owner"))
	solo, _ := r.body["summary"].(map[string]any)["id"].(string)
	if solo == "" {
		t.Fatalf("second project: %s", r.raw)
	}
	create := func(h map[string]string, typ, title, body string, extra map[string]any) string {
		data := map[string]any{"type": typ, "title": title, "body": body}
		for k, v := range extra {
			data[k] = v
		}
		r := mustOK(t, gpuCall(t, app, "POST", "/api/units", data, h), "create "+title)
		return r.body["unit"].(map[string]any)["uid"].(string)
	}
	review := func(h map[string]string, uid string) {
		mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+uid, map[string]any{"status": "review"}, h), "review "+uid)
	}
	ana := in("ana", team)
	// ana delivers two units of one branch (one card) and two decisions.
	a := create(ana, "task", "Part A", "Done on feat/notify-test.", nil)
	b := create(ana, "experiment", "Part B", "Measured on feat/notify-test.", nil)
	review(ana, a)
	review(ana, b)
	create(ana, "decision", "Pick one", "## Options\n- x\n- y", nil)
	create(ana, "decision", "Or this", "## Options\n- a", nil)
	// Closed decisions (store.ClosedStatuses) never notify.
	for _, status := range []string{"abandoned", "archived", "done"} {
		uid := create(ana, "decision", "Closed "+status, "## Options\n- z", nil)
		mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+uid, map[string]any{"status": status}, ana), "close "+uid)
	}
	// The owner's own unit put in review from the browser: not notified.
	own := create(in("owner", solo), "task", "Mine", "Own work.", nil)
	review(in("owner", solo), own)
	step()

	owner := sessionCookie(app, "owner")
	feed := readFeed(t, app, "", owner)
	got := kinds(feed.Items)
	if feed.Scope != "account" || got["review"] != 1 || got["decision"] != 2 || len(feed.Items) != 3 {
		t.Fatalf("owner feed: scope %s kinds %v items %+v", feed.Scope, got, feed.Items)
	}
	for _, it := range feed.Items {
		switch it.Kind {
		case "decision":
			if it.Priority != "high" || it.Link["type"] != "unit" || it.Project["name"] != "Team" {
				t.Errorf("decision item: %+v", it)
			}
		case "review":
			if it.Project["id"] == team && (it.Count != 2 || it.Link["type"] != "unit" || it.Link["project_id"] != team ||
				!strings.HasPrefix(it.Link["route"].(string), "#/editor/unit%3A")) {
				t.Errorf("delivery card (counted once): %+v", it)
			}
		}
		if it.Title == "Mine" {
			t.Errorf("own or someone else's item notified: %+v", it)
		}
	}
	if len(feed.Active) != 3 || feed.Cursor == "" {
		t.Fatalf("active/cursor: %v %q", feed.Active, feed.Cursor)
	}
	// Nothing new since the cursor once the overlap (notifyOverlap) passed;
	// within it the items come again with the same ids.
	if again := readFeed(t, app, "?since="+feed.Cursor, owner); len(again.Items) != 3 || again.Items[0].ID != feed.Items[0].ID {
		t.Fatalf("within the overlap: %+v", again.Items)
	}
	stepBy(notifyOverlap + time.Second)
	later := readFeed(t, app, "", owner).Cursor
	if again := readFeed(t, app, "?since="+later, owner); len(again.Items) != 0 || len(again.Active) != 3 {
		t.Fatalf("since cursor: %+v", again)
	}
	// Seen in the browser: the review items leave the feed (active too).
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"all": true}, in("owner", team)), "seen")
	step()
	after := readFeed(t, app, "", owner)
	if len(after.Items) != 0 || len(after.Active) != 0 {
		t.Fatalf("after seen: %+v", after.Items)
	}
	// A new review after the cursor shows up with since.
	stepBy(notifyOverlap + time.Second)
	cursor := readFeed(t, app, "", owner).Cursor
	step()
	late := create(ana, "task", "Late", "Later work.", nil)
	review(ana, late)
	step()
	fresh := readFeed(t, app, "?since="+cursor, owner)
	if len(fresh.Items) != 1 || fresh.Items[0].Title != "Late" {
		t.Fatalf("new since cursor: %+v", fresh.Items)
	}
	// A viewer gets no review items; bad cursors are refused.
	if v := readFeed(t, app, "", sessionCookie(app, "vic")); len(v.Items) != 0 {
		t.Errorf("viewer: %+v", v.Items)
	}
	if r := gpuCall(t, app, "GET", "/api/notifications?since=yesterday", nil, owner); r.code != 400 {
		t.Errorf("bad cursor: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "GET", "/api/notifications", nil, nil); r.code != 401 {
		t.Errorf("signed out: %d %s", r.code, r.raw)
	}

	// Sessions: the owner's errored run with a pending approval, ana's run.
	root := filepath.Join(filepath.Dir(app.Root), "sessions")
	ended := clock.Format(now)
	project := map[string]any{"id": team, "name": "Team"}
	writeSession(t, root, "s_ownerfail01", map[string]any{"title": "Fix tests", "actor": "owner", "project": project,
		"status": "errored", "error": "process exited with code 1", "ended_at": ended,
		"approvals": []any{map[string]any{"id": "ap_1", "status": "pending", "tool": "Bash", "command": "rm -rf build",
			"requested_at": ended}, map[string]any{"id": "ap_0", "status": "approved", "requested_at": ended}}})
	writeSession(t, root, "s_ownerdone01", map[string]any{"title": "Docs", "actor": "owner", "project": project,
		"status": "stopped", "exit_code": 0, "ended_at": ended})
	writeSession(t, root, "s_ownerold001", map[string]any{"title": "Old", "actor": "owner", "project": project,
		"status": "errored", "ended_at": clock.Format(now.Add(-48 * time.Hour))})
	writeSession(t, root, "s_anafailed01", map[string]any{"title": "Hers", "actor": "ana", "project": project,
		"status": "errored", "ended_at": ended})
	mgr, err := sessions.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	app.Sessions = mgr
	withRuns := readFeed(t, app, "?since="+cursor, owner)
	got = kinds(withRuns.Items)
	if got["approval"] != 1 || got["session_error"] != 1 || got["session_done"] != 1 {
		t.Fatalf("session items: %v %+v", got, withRuns.Items)
	}
	for _, it := range withRuns.Items {
		if it.Kind == "approval" && (it.Priority != "high" || it.Body != "Bash rm -rf build" || it.Link["type"] != "session" ||
			it.Link["ref"] != "s_ownerfail01") {
			t.Errorf("approval item: %+v", it)
		}
		if it.Title == "Hers" || it.Title == "Old" {
			t.Errorf("not the owner's or too old: %+v", it)
		}
	}

	// A phone signed in with the password sees the account; a QR pairing
	// only its project.
	login := gpuCall(t, app, "POST", "/api/mobile/login", map[string]any{"username": "owner", "password": desktopPassword,
		"project_id": team}, nil)
	phone, _ := login.body["mobile_token"].(string)
	if login.code != 200 || phone == "" {
		t.Fatalf("phone login: %d %s", login.code, login.raw)
	}
	if p := readFeed(t, app, "", mobileHeader(phone)); p.Scope != "account" || len(p.Projects) < 2 {
		t.Errorf("password phone: %s %v", p.Scope, p.Projects)
	}
	app.Bind("0.0.0.0", 8765) // pairing codes need a phone-reachable server
	pair := gpuCall(t, app, "POST", "/api/mobile/pair", map[string]any{"permission": "read_only"}, in("owner", solo))
	code, _ := pair.body["code"].(string)
	if pair.code != 200 || code == "" {
		t.Fatalf("pair: %d %s", pair.code, pair.raw)
	}
	claim := gpuCall(t, app, "POST", "/api/mobile/pair/claim", map[string]any{"code": code}, nil)
	qr, _ := claim.body["mobile_token"].(string)
	if claim.code != 200 || qr == "" {
		t.Fatalf("claim: %d %s", claim.code, claim.raw)
	}
	p := readFeed(t, app, "", mobileHeader(qr))
	if p.Scope != "project" || len(p.Projects) != 1 || p.Projects[0]["id"] != solo {
		t.Fatalf("QR phone scope: %s %v", p.Scope, p.Projects)
	}
	for _, it := range p.Items {
		if it.Project["id"] != solo {
			t.Errorf("QR phone sees another project: %+v", it)
		}
	}
}
