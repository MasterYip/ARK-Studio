package server

// Go only: "new for review" badges on GET /api/projects and POST
// /api/needs/seen (project_reviews.go, store/needscards.go, org/reviewseen.go).

import (
	"context"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/store"
)

func TestNewCardsCounting(t *testing.T) {
	at := func(s string) string { tm, _ := time.Parse(time.RFC3339, s); return store.MarkTimeOf(tm) }
	cards := []store.NeedsCard{
		{Items: []store.NeedsMark{{Key: "rg:RG-1", Since: at("2026-11-20T10:00:00Z")}}},
		// An automatic group: new while any member is.
		{Items: []store.NeedsMark{{Key: "u:T-1", Since: at("2026-11-20T09:00:00Z")}, {Key: "u:T-2", Since: at("2026-11-21T09:00:00Z")}}},
		{Items: []store.NeedsMark{{Key: "u:D-1", Since: at("2026-11-21T12:00:00Z")}}},
	}
	cases := []struct {
		name       string
		seen       store.NeedsSeen
		fresh, all int
	}{
		{"never looked", store.NeedsSeen{}, 3, 3},
		// Only opening an item in Node details counts (o: marks); looking at
		// the list (watermark) or showing a card (plain item mark) does not.
		{"list shown after all", store.NeedsSeen{Watermark: at("2026-11-22T00:00:00Z"),
			Items: map[string]string{"u:T-1": at("2026-11-20T09:00:00Z")}}, 3, 3},
		{"all opened", store.NeedsSeen{Items: map[string]string{
			"o:rg:RG-1": at("2026-11-20T10:00:00Z"), "o:u:T-1": at("2026-11-20T09:00:00Z"), "o:u:T-2": at("2026-11-21T09:00:00Z"),
			"o:u:D-1": at("2026-11-21T12:00:00Z")}}, 0, 3},
		{"one member of a group opened", store.NeedsSeen{Items: map[string]string{
			"o:rg:RG-1": at("2026-11-20T10:00:00Z"), "o:u:T-2": at("2026-11-21T09:00:00Z"), "o:u:D-1": at("2026-11-21T12:00:00Z")}}, 1, 3},
		{"opened before a new round", store.NeedsSeen{Items: map[string]string{
			"o:rg:RG-1": at("2026-11-19T10:00:00Z"), "o:u:T-1": at("2026-11-20T09:00:00Z"), "o:u:T-2": at("2026-11-21T09:00:00Z"),
			"o:u:D-1": at("2026-11-21T12:00:00Z")}}, 1, 3},
	}
	for _, c := range cases {
		fresh, all := store.NewCards(cards, c.seen)
		if fresh != c.fresh || all != c.all {
			t.Errorf("%s: got %d/%d, want %d/%d", c.name, fresh, all, c.fresh, c.all)
		}
	}
}

func TestReviewBadgesPerPerson(t *testing.T) {
	app, projectID := authDesktopApp(t)
	ctx := context.Background()
	ownerID, _ := app.Org.UserIDFor(ctx, "owner")
	anaID, _ := app.Org.UserIDFor(ctx, "ana")
	if err := app.Org.SetProjectGrant(ctx, projectID, anaID, "write", ownerID); err != nil {
		t.Fatal(err)
	}
	as := func(user string) map[string]string {
		h := sessionCookie(app, user)
		h["X-ARK-Project"] = projectID
		return h
	}
	owner := as("owner")
	// Review times are stored to the second: step the clock between phases.
	now := time.Now().UTC().Truncate(time.Second)
	step := func() { now = now.Add(2 * time.Second); pinned := now; clock.SetReplay(&pinned) }
	step()
	t.Cleanup(func() { clock.SetReplay(nil) })
	create := func(typ, title, body string) string {
		r := mustOK(t, gpuCall(t, app, "POST", "/api/units", map[string]any{"type": typ, "title": title, "body": body}, owner), "create "+title)
		return r.body["unit"].(map[string]any)["uid"].(string)
	}
	review := func(uid string) {
		mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+uid, map[string]any{"status": "review"}, owner), "review "+uid)
	}
	badge := func(user string) (any, any) {
		p := listedAs(t, app, user)[projectID]
		return p["review_new"], p["review_total"]
	}
	// Two units of one delivery (the same branch) are one card, plus one on
	// its own and an open decision: three cards.
	a := create("task", "Part A", "Done on feat/badges-test.")
	b := create("experiment", "Part B", "Measured on feat/badges-test.")
	c := create("task", "Other", "Unrelated.")
	dec := create("decision", "Pick one", "## Options\n- x\n- y")
	review(a)
	review(b)
	review(c)
	if n, total := badge("owner"); n != float64(3) || total != float64(3) {
		t.Fatalf("owner before looking: %v new of %v", n, total)
	}
	// A read-only member gets no badge.
	if n, total := badge("vic"); n != nil || total != nil {
		t.Fatalf("viewer badge: %v / %v", n, total)
	}
	// Only opening an item in the node details counts as seen (the user:
	// "看过：在属性栏点开过"): showing the list changes nothing.
	shown := mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"all": true,
		"items": []any{"u:" + a, "u:" + b, "u:" + c, "u:" + dec}}, owner), "list shown")
	if shown.body["review_new"] != float64(3) || len(shown.body["new_keys"].([]any)) != 4 {
		t.Fatalf("list shown: %s", shown.raw)
	}
	// Opening a and b clears the delivery card; c and the decision stay new.
	part := mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"opened": []any{"u:" + a, "u:" + b}}, owner), "opened a, b")
	if part.body["review_new"] != float64(2) || len(part.body["new_keys"].([]any)) != 2 {
		t.Fatalf("opened two: %s", part.raw)
	}
	if got := mustOK(t, gpuCall(t, app, "GET", "/api/needs/new", nil, owner), "new").body["new_keys"].([]any); len(got) != 2 {
		t.Fatalf("GET /api/needs/new: %v", got)
	}
	seen := mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"opened": []any{"u:" + c, "u:" + dec}}, owner), "opened all")
	if seen.body["review_new"] != float64(0) || seen.body["review_total"] != float64(3) || len(seen.body["new_keys"].([]any)) != 0 {
		t.Fatalf("seen reply: %s", seen.raw)
	}
	if n, _ := badge("owner"); n != float64(0) {
		t.Fatalf("owner after the list: %v", n)
	}
	// The owner's items are assigned to the owner: for ana they are someone
	// else's and count as seen unless she highlights others' reviews.
	if n, _ := badge("ana"); n != float64(0) {
		t.Fatalf("ana, others' items: %v", n)
	}
	mustOK(t, gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": map[string]any{"highlight_others_reviews": true}}, as("ana")), "ana pref")
	if n, _ := badge("ana"); n != float64(3) {
		t.Fatalf("ana is not affected: %v", n)
	}
	// Something new later: one new card; opening that item clears it.
	step()
	d := create("task", "Late", "Later work.")
	review(d)
	if n, total := badge("owner"); n != float64(1) || total != float64(4) {
		t.Fatalf("owner after a new review: %v of %v", n, total)
	}
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"items": []any{"u:" + d}, "opened": []any{"u:" + d}}, owner), "opened item")
	if n, _ := badge("owner"); n != float64(0) {
		t.Fatalf("owner after opening the item: %v", n)
	}
	// A new member joining the delivery's card makes the card new again
	// (counted once).
	step()
	e := create("experiment", "Part C", "Also feat/badges-test.")
	review(e)
	if n, total := badge("owner"); n != float64(1) || total != float64(4) {
		t.Fatalf("group with a new member: %v of %v", n, total)
	}
	// Seen is per person: ana opened nothing, so everything is new for her.
	anaNew := mustOK(t, gpuCall(t, app, "GET", "/api/needs/new", nil, as("ana")), "ana new").body["new_keys"].([]any)
	ownerNew := mustOK(t, gpuCall(t, app, "GET", "/api/needs/new", nil, owner), "owner new").body["new_keys"].([]any)
	if len(anaNew) <= len(ownerNew) || !containsAny(anaNew, "u:"+a) || containsAny(ownerNew, "u:"+a) {
		t.Fatalf("per person: ana %v owner %v", anaNew, ownerNew)
	}
	// Mark as unread brings it back for the owner only.
	un := mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"unopened": []any{"u:" + a}}, owner), "unread")
	if !containsAny(un.body["new_keys"].([]any), "u:"+a) {
		t.Fatalf("mark unread: %s", un.raw)
	}
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"opened": []any{"u:" + a}}, owner), "read again")
	// A new review round makes an opened item new again.
	step()
	mustOK(t, gpuCall(t, app, "PATCH", "/api/units/"+d, map[string]any{"status": "active"}, owner), "back to work")
	step()
	review(d)
	if keys := mustOK(t, gpuCall(t, app, "GET", "/api/needs/new", nil, owner), "new").body["new_keys"].([]any); !containsAny(keys, "u:"+d) {
		t.Fatalf("new round not new: %v", keys)
	}
	// Bad keys are refused.
	if r := gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"items": []any{"x"}}, owner); r.code != 400 {
		t.Fatalf("bad key: %d %s", r.code, r.raw)
	}
	if r := gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{}, owner); r.code != 400 {
		t.Fatalf("empty body: %d %s", r.code, r.raw)
	}
	// Read-only members have no review badge; they may mark their own
	// assignments read (assignee_notice_test.go), so the POST answers 0 of 0.
	if r := gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"all": true}, as("vic")); r.code != 200 ||
		r.body["review_new"] != float64(0) || r.body["review_total"] != float64(0) {
		t.Fatalf("viewer seen: %d %s", r.code, r.raw)
	}
}

func containsAny(list []any, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Review items assigned to someone else count as seen for me unless I turn
// on prefs.highlight_others_reviews.
func TestOthersReviewsSeenByDefault(t *testing.T) {
	app, projectID := authDesktopApp(t)
	ctx := context.Background()
	anaID, _ := app.Org.UserIDFor(ctx, "ana")
	ownerID, _ := app.Org.UserIDFor(ctx, "owner")
	if err := app.Org.SetProjectGrant(ctx, projectID, anaID, "write", ownerID); err != nil {
		t.Fatal(err)
	}
	as := func(user string) map[string]string {
		h := sessionCookie(app, user)
		h["X-ARK-Project"] = projectID
		return h
	}
	owner := as("owner")
	mk := func(title string, assignee any) string {
		body := map[string]any{"type": "task", "title": title, "status": "review"}
		body["assignee"] = assignee
		r := mustOK(t, gpuCall(t, app, "POST", "/api/units", body, owner), "create "+title)
		return r.body["unit"].(map[string]any)["uid"].(string)
	}
	anas := mk("For ana", "ana")
	mine := mk("For owner", "owner")
	nobody := mk("Nobody's", "")
	newFor := func(user string) []any {
		return mustOK(t, gpuCall(t, app, "GET", "/api/needs/new", nil, as(user)), "new "+user).body["new_keys"].([]any)
	}
	got := newFor("owner")
	if containsAny(got, "u:"+anas) || !containsAny(got, "u:"+mine) || !containsAny(got, "u:"+nobody) {
		t.Fatalf("owner default: %v", got)
	}
	got = newFor("ana")
	if !containsAny(got, "u:"+anas) || containsAny(got, "u:"+mine) || !containsAny(got, "u:"+nobody) {
		t.Fatalf("ana default: %v", got)
	}
	if n, _ := listedAs(t, app, "owner")[projectID]["review_new"].(float64); n != 2 {
		t.Fatalf("owner picker badge: %v", n)
	}
	// The owner turns the setting on: ana's item is new for them too.
	mustOK(t, gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": map[string]any{"highlight_others_reviews": true}}, owner), "pref on")
	if got = newFor("owner"); !containsAny(got, "u:"+anas) {
		t.Fatalf("owner with the setting: %v", got)
	}
	if n, _ := listedAs(t, app, "owner")[projectID]["review_new"].(float64); n != 3 {
		t.Fatalf("owner picker badge with the setting: %v", n)
	}
	// ana's own view does not change.
	if got = newFor("ana"); containsAny(got, "u:"+mine) {
		t.Fatalf("ana after the owner's setting: %v", got)
	}
	me := mustOK(t, gpuCall(t, app, "GET", "/api/me", nil, owner), "me")
	if !strings.Contains(string(me.raw), `"highlight_others_reviews": true`) {
		t.Fatalf("pref not read back: %s", me.raw)
	}
	if r := gpuCall(t, app, "PATCH", "/api/me", map[string]any{"prefs": map[string]any{"highlight_others_reviews": "yes"}}, owner); r.code != 400 {
		t.Fatalf("bad value: %d %s", r.code, r.raw)
	}
}
