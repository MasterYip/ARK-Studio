package server

// Go only: "new for review" badges on GET /api/projects and POST
// /api/needs/seen (project_reviews.go, store/needscards.go, org/reviewseen.go).

import (
	"context"
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
		{"list opened in between", store.NeedsSeen{Watermark: at("2026-11-21T00:00:00Z")}, 2, 3},
		{"list opened after all", store.NeedsSeen{Watermark: at("2026-11-22T00:00:00Z")}, 0, 3},
		{"same second as the list stays new", store.NeedsSeen{Watermark: at("2026-11-21T12:00:00Z")}, 1, 3},
		{"one member opened", store.NeedsSeen{Watermark: at("2026-11-21T00:00:00Z"),
			Items: map[string]string{"u:T-2": at("2026-11-21T09:00:00Z")}}, 1, 3},
		{"opened before a new round", store.NeedsSeen{Items: map[string]string{
			"rg:RG-1": at("2026-11-19T10:00:00Z"), "u:T-1": at("2026-11-20T09:00:00Z"), "u:T-2": at("2026-11-21T09:00:00Z"),
			"u:D-1": at("2026-11-21T12:00:00Z")}}, 1, 3},
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
	// Opening the list (it showed every card) clears it for the owner only.
	seen := mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"all": true,
		"items": []any{"u:" + a, "u:" + b, "u:" + c, "u:" + dec}}, owner), "seen all")
	if seen.body["review_new"] != float64(0) || seen.body["review_total"] != float64(3) {
		t.Fatalf("seen reply: %s", seen.raw)
	}
	if n, _ := badge("owner"); n != float64(0) {
		t.Fatalf("owner after the list: %v", n)
	}
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
	mustOK(t, gpuCall(t, app, "POST", "/api/needs/seen", map[string]any{"items": []any{"u:" + d}}, owner), "seen item")
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
