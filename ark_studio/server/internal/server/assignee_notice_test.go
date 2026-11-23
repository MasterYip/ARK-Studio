package server

// Go only: "Assigned to me" in Needs you and the assignee's notices
// (store/assignments.go, project_reviews.go, notifications.go).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/store"
)

type assignedUnitReply struct {
	UID  string `json:"uid"`
	Key  string `json:"key"`
	New  bool   `json:"new"`
	Type string `json:"type"`
}

type assignedReply struct {
	Batch  string              `json:"batch"`
	By     string              `json:"by"`
	ByName string              `json:"by_name"`
	Actor  string              `json:"actor"`
	New    bool                `json:"new"`
	Units  []assignedUnitReply `json:"units"`
}

func readAssigned(t *testing.T, app *App, h map[string]string) []assignedReply {
	t.Helper()
	r := mustOK(t, gpuCall(t, app, "GET", "/api/needs/assigned", nil, h), "assigned")
	var out struct {
		Items []assignedReply `json:"items"`
	}
	if err := json.Unmarshal([]byte(r.raw), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func assignedUIDs(items []assignedReply) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		for _, u := range it.Units {
			out[u.UID] = true
		}
	}
	return out
}

func assignedFeed(t *testing.T, app *App, user string) []feedItem {
	t.Helper()
	var out []feedItem
	for _, it := range readFeed(t, app, "", sessionCookie(app, user)).Items {
		if it.Kind == "assigned" {
			out = append(out, it)
		}
	}
	return out
}

// projectBadge is review_new / review_total of project id in GET /api/projects.
func projectBadge(t *testing.T, app *App, user, id string) (fresh, total float64, ok bool) {
	t.Helper()
	r := mustOK(t, gpuCall(t, app, "GET", "/api/projects", nil, sessionCookie(app, user)), "projects")
	for _, raw := range r.body["projects"].([]any) {
		p := raw.(map[string]any)
		if p["id"] == id {
			fresh, ok = p["review_new"].(float64)
			total, _ = p["review_total"].(float64)
			return fresh, total, ok
		}
	}
	return 0, 0, false
}

func TestAssigneeNotices(t *testing.T) {
	app, team := authDesktopApp(t) // ana and vic: read on Team
	ctx := context.Background()
	ownerID, _ := app.Org.UserIDFor(ctx, "owner")
	anaID, _ := app.Org.UserIDFor(ctx, "ana")
	if err := app.Org.SetProjectGrant(ctx, team, anaID, "write", ownerID); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	step := func(d time.Duration) { now = now.Add(d); pinned := now; clock.SetReplay(&pinned) }
	step(2 * time.Second)
	t.Cleanup(func() { clock.SetReplay(nil) })
	owner, ana, vic := asIn(app, "owner", team), asIn(app, "ana", team), asIn(app, "vic", team)
	create := func(h map[string]string, title string, extra map[string]any) string {
		data := map[string]any{"type": "task", "title": title}
		for k, v := range extra {
			data[k] = v
		}
		r := mustOK(t, gpuCall(t, app, "POST", "/api/units", data, h), "create "+title)
		return r.body["unit"].(map[string]any)["uid"].(string)
	}
	patch := func(h map[string]string, uid string, body map[string]any) {
		t.Helper()
		mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+uid, body, h), "patch "+uid)
		step(2 * time.Second)
	}
	var u []string
	for _, title := range []string{"One", "Two", "Three", "Four", "Five", "Six", "Seven"} {
		u = append(u, create(owner, title, nil))
	}
	// The agent update feed of ana's token, from here on.
	anaAgent := map[string]string{"Authorization": "Bearer " + issueTestToken(t, app, "ana"), "X-ARK-Project": team,
		"X-Agent-Name": "helper"}
	cursor := mustOK(t, gpuCall(t, app, "GET", "/api/agent/v1/updates", nil, anaAgent), "updates").body["cursor"].(string)

	// Creating units (assigned to their creator) and assigning yourself: nothing.
	mine := create(ana, "Ana's own", nil)
	patch(ana, mine, map[string]any{"assignee": "ana"})
	patch(owner, u[0], map[string]any{"assignee": "owner"})
	if got := readAssigned(t, app, ana); len(got) != 0 {
		t.Fatalf("self assignments: %+v", got)
	}
	if got := readAssigned(t, app, owner); len(got) != 0 {
		t.Fatalf("owner: %+v", got)
	}

	// One assignment: one notice for ana only, with who and a unit link.
	patch(owner, u[0], map[string]any{"assignee": "ana"})
	got := readAssigned(t, app, ana)
	if len(got) != 1 || got[0].By != "owner" || !got[0].New || len(got[0].Units) != 1 || got[0].Units[0].Key != "a:"+u[0] {
		t.Fatalf("assigned: %+v", got)
	}
	feed := assignedFeed(t, app, "ana")
	if len(feed) != 1 || feed[0].Title != "One" || feed[0].Count != 1 || feed[0].Link["type"] != "unit" ||
		feed[0].Link["ref"] != u[0] || feed[0].Body != "Assigned to you by owner" {
		t.Fatalf("ana's feed: %+v", feed)
	}
	if len(assignedFeed(t, app, "owner")) != 0 || len(assignedFeed(t, app, "vic")) != 0 {
		t.Fatal("assignment notified someone else")
	}
	if fresh, total, ok := projectBadge(t, app, "ana", team); !ok || fresh != 1 || total != 1 {
		t.Fatalf("ana's badge: %v %v %v", fresh, total, ok)
	}

	// More single assignments soon after join the same notice.
	patch(owner, u[4], map[string]any{"assignee": "ana"})
	patch(owner, u[5], map[string]any{"assignee": "ana"})
	if feed := assignedFeed(t, app, "ana"); len(feed) != 1 || feed[0].Count != 3 {
		t.Fatalf("assignments in a row: %+v", feed)
	}

	// A batch assignment is one notice for all its units.
	step(store.AssignBatchWindow + time.Second)
	mustOK(t, gpuCall(t, app, "POST", "/api/units/batch", map[string]any{"units": []string{u[1], u[2], u[3]},
		"patch": map[string]any{"assignee": "ana"}}, owner), "batch assign")
	step(2 * time.Second)
	feed = assignedFeed(t, app, "ana")
	if len(feed) != 2 || feed[0].Count != 3 || feed[0].Body != "owner assigned you 3 nodes" || feed[0].Link["type"] != "needs" {
		t.Fatalf("batch notice: %+v", feed)
	}
	if fresh, total, _ := projectBadge(t, app, "ana", team); fresh != 2 || total != 2 {
		t.Fatalf("badge after batch: %v %v", fresh, total)
	}

	// Opening a unit reads its notice; the list watermark does not.
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"all": true}, ana), "list seen")
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"items": []string{"u:" + u[0]}}, ana), "open unit")
	step(2 * time.Second)
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"all": true}, ana), "list seen again")
	for _, it := range readAssigned(t, app, ana) {
		for _, x := range it.Units {
			if x.New == (x.UID == u[0]) {
				t.Errorf("read state of %s: %+v", x.UID, x)
			}
		}
	}
	// Clearing from Needs you reads and hides them.
	r := mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"clear": []string{"a:" + u[4], "a:" + u[5]}}, ana), "clear")
	if r.body["review_new"].(float64) != 1 || r.body["review_total"].(float64) != 2 {
		t.Fatalf("counts after clear: %s", r.raw)
	}
	step(2 * time.Second)
	if ids := assignedUIDs(readAssigned(t, app, ana)); ids[u[4]] || ids[u[5]] || !ids[u[0]] {
		t.Fatalf("after clear: %v", ids)
	}
	if feed := assignedFeed(t, app, "ana"); len(feed) != 1 || feed[0].Count != 3 {
		t.Fatalf("feed after reading the first notice: %+v", feed)
	}

	// A cleared unit that goes through review stays cleared.
	patch(owner, u[5], map[string]any{"status": "review"})
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"items": []string{"u:" + u[0]}}, ana), "seen during review")
	patch(owner, u[5], map[string]any{"status": "active"})
	if ids := assignedUIDs(readAssigned(t, app, ana)); ids[u[5]] {
		t.Fatalf("cleared unit back after review: %v", ids)
	}

	// Reassigning moves it: gone for ana, a notice for vic (read access).
	patch(owner, u[1], map[string]any{"assignee": "vic"})
	if ids := assignedUIDs(readAssigned(t, app, ana)); ids[u[1]] {
		t.Fatalf("reassigned unit still with ana: %v", ids)
	}
	if v := readAssigned(t, app, vic); len(v) != 1 || v[0].Units[0].UID != u[1] {
		t.Fatalf("vic: %+v", v)
	}
	if feed := assignedFeed(t, app, "vic"); len(feed) != 1 || feed[0].Title != "Two" {
		t.Fatalf("vic's feed: %+v", feed)
	}
	if fresh, _, ok := projectBadge(t, app, "vic", team); !ok || fresh != 1 {
		t.Fatalf("vic's badge: %v %v", fresh, ok)
	}
	// A read-only member may mark their notice read.
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"items": []string{"u:" + u[1]}}, vic), "vic opens")
	if feed := assignedFeed(t, app, "vic"); len(feed) != 0 {
		t.Fatalf("vic's feed after opening: %+v", feed)
	}

	// Unassigning, closing, review: out of "Assigned to me".
	patch(owner, u[2], map[string]any{"assignee": nil})
	for _, status := range []string{"done", "abandoned"} {
		uid := create(owner, "Closed "+status, map[string]any{"assignee": "ana"})
		patch(owner, uid, map[string]any{"status": status})
		if assignedUIDs(readAssigned(t, app, ana))[uid] {
			t.Errorf("closed (%s) unit listed", status)
		}
	}
	patch(owner, u[3], map[string]any{"status": "review"}) // in Needs you for review already
	ids := assignedUIDs(readAssigned(t, app, ana))
	if ids[u[2]] || ids[u[3]] {
		t.Fatalf("unassigned or in review: %v", ids)
	}
	patch(owner, u[3], map[string]any{"status": "active"})
	if !assignedUIDs(readAssigned(t, app, ana))[u[3]] {
		t.Fatal("back from review: missing")
	}
	// The assignee assigning it to themselves ends the notice.
	patch(ana, u[3], map[string]any{"assignee": "ana"})
	patch(ana, u[3], map[string]any{"assignee": "owner"})
	patch(ana, u[3], map[string]any{"assignee": "ana"})
	if assignedUIDs(readAssigned(t, app, ana))[u[3]] {
		t.Fatal("self assigned unit listed")
	}

	// Assigned again after a clear: shown again.
	patch(owner, u[4], map[string]any{"assignee": "owner"})
	patch(owner, u[4], map[string]any{"assignee": "ana"})
	if !assignedUIDs(readAssigned(t, app, ana))[u[4]] {
		t.Fatal("reassigned after clear: missing")
	}

	// An agent's assignment counts as its user's.
	ownerAgent := map[string]string{"Authorization": "Bearer " + issueTestToken(t, app, "owner"), "X-ARK-Project": team,
		"X-Agent-Name": "planner"}
	step(store.AssignBatchWindow + time.Second)
	mustOK(t, gpuCall(t, app, "POST", "/api/agent/v1/units/"+u[6], map[string]any{"assignee": "ana"}, ownerAgent), "agent assign")
	step(2 * time.Second)
	var byAgent *assignedReply
	for _, it := range readAssigned(t, app, ana) {
		if it.Units[0].UID == u[6] {
			it := it
			byAgent = &it
		}
	}
	if byAgent == nil || byAgent.By != "owner" || byAgent.Actor != "agent:owner/planner" {
		t.Fatalf("agent assignment: %+v", byAgent)
	}
	// ana's agents see the assignments in their update feed.
	up := mustOK(t, gpuCall(t, app, "GET", "/api/agent/v1/updates?kinds=assigned&since="+cursor, nil, anaAgent), "ana updates")
	if n := len(up.body["items"].([]any)); n < 5 {
		t.Fatalf("assigned in the agent feed: %d %s", n, up.raw)
	}
	// Other people's lists are not readable: each person gets their own.
	if v := readAssigned(t, app, vic); assignedUIDs(v)[u[6]] || assignedUIDs(v)[u[0]] {
		t.Fatalf("vic sees ana's: %+v", v)
	}
	if r := gpuCall(t, app, "GET", "/api/needs/assigned", nil, nil); r.code != 401 && r.code != 403 {
		t.Fatalf("signed out: %d %s", r.code, r.raw)
	}
}
