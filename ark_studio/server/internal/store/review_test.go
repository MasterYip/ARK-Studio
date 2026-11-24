package store

// Review requests: rounds, review_note, changes since the last review,
// anchors and review groups (reviewrounds.go, anchors.go, reviewgroups.go).

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

func reviewStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir(), NewManifest(pyjson.NewObject("id", "prj_review", "name", "Review")), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func mkUnit(t *testing.T, st *Store, data *pyjson.Object, actor string) string {
	t.Helper()
	u, err := st.CreateUnit(context.Background(), data, actor, Who{})
	if err != nil {
		t.Fatal(err)
	}
	return pyjson.Str(u.Value("uid"))
}

func patch(t *testing.T, st *Store, uid string, p *pyjson.Object, actor string) {
	t.Helper()
	if _, err := st.UpdateUnit(context.Background(), uid, p, actor, Who{}); err != nil {
		t.Fatalf("update %s %v: %v", uid, p, err)
	}
}

func needsItem(t *testing.T, st *Store, uid string) *pyjson.Object {
	t.Helper()
	payload, err := st.ProjectPayload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range payload.Value("needs_you").([]any) {
		o := it.(*pyjson.Object)
		if o.Value("uid") == uid {
			return o
		}
	}
	return nil
}

func TestReviewRoundsNoteAndChanges(t *testing.T) {
	ctx := context.Background()
	st := reviewStore(t)
	agent := "agent:local/coder"
	uid := mkUnit(t, st, pyjson.NewObject("type", "experiment", "title", "Ablation", "body", "# Setup\nseed 1\n\n# Results\nacc 0.5"), agent)
	// review_note needs status review.
	if _, err := st.UpdateUnit(ctx, uid, pyjson.NewObject("review_note", "look"), agent, Who{}); err == nil ||
		!strings.Contains(err.Error(), "needs status review") {
		t.Fatalf("note without review: %v", err)
	}
	if _, err := st.UpdateUnit(ctx, uid, pyjson.NewObject("status", "review", "review_note", strings.Repeat("x", 501)), agent, Who{}); err == nil {
		t.Fatal("long note accepted")
	}
	patch(t, st, uid, pyjson.NewObject("status", "review", "review_note", "Check [Results](#results)"), agent)
	it := needsItem(t, st, uid)
	if it == nil || it.Value("review_round") != int64(1) || it.Value("review_note") != "Check [Results](#results)" {
		t.Fatalf("round 1: %v", it)
	}
	anchors := it.Value("review_anchors").([]any)
	if len(anchors) != 1 || anchors[0].(*pyjson.Object).Value("ok") != true || anchors[0].(*pyjson.Object).Value("slug") != "results" {
		t.Fatalf("anchors: %v", anchors)
	}
	if ch := it.Value("review_changes").(*pyjson.Object); ch.Value("added") != int64(0) {
		t.Fatalf("no change yet: %v", ch)
	}
	// The agent appends while in review: shown as a change since it entered review.
	patch(t, st, uid, pyjson.NewObject("body", "# Setup\nseed 1\n\n# Results\nacc 0.5\nacc 0.6 with seed 2"), agent)
	it = needsItem(t, st, uid)
	if ch := it.Value("review_changes").(*pyjson.Object); ch.Value("added") != int64(1) || ch.Value("removed") != int64(0) {
		t.Fatalf("changes: %v", ch)
	}
	if b := it.Value("review_base").(*pyjson.Object); b.Value("kind") != "entered" {
		t.Fatalf("base: %v", b)
	}
	// The person sends it back with a reason, then the agent fixes and re-requests.
	if _, err := st.AddComment(ctx, uid, "Add the baseline", "sent_back", nil, "local", "human:ui"); err != nil {
		t.Fatal(err)
	}
	patch(t, st, uid, pyjson.NewObject("status", "active"), "human:ui")
	if needsItem(t, st, uid) != nil {
		t.Fatal("still listed after send back")
	}
	patch(t, st, uid, pyjson.NewObject("body", "# Setup\nseed 1\n\n# Results\nacc 0.5\nacc 0.6 with seed 2\n\n# Baseline\nacc 0.4"), agent)
	if _, err := st.AddMedia(ctx, uid, "assets/files/plot.png", "image", "Plot", agent); err != nil {
		t.Fatal(err)
	}
	// An agent cannot resubmit without a note; a person can.
	var noNote *ReviewNoteError
	if _, err := st.UpdateUnit(ctx, uid, pyjson.NewObject("status", "review"), agent, Who{}); !errors.As(err, &noNote) ||
		!strings.Contains(err.Error(), "for each comment") || !strings.Contains(err.Error(), "#baseline") {
		t.Fatalf("agent resubmit without a note: %v", err)
	}
	patch(t, st, uid, pyjson.NewObject("status", "review"), "human:ui")
	it = needsItem(t, st, uid)
	if it.Value("review_round") != int64(2) || it.Value("review_note") != "" {
		t.Fatalf("round 2: %v", it)
	}
	if b := it.Value("review_base").(*pyjson.Object); b.Value("kind") != "review" {
		t.Fatalf("base after send back: %v", b)
	}
	ch := it.Value("review_changes").(*pyjson.Object)
	if ch.Value("added") != int64(3) || ch.Value("media") != int64(1) {
		t.Fatalf("since last review: %v", ch)
	}
	if focus := pyjson.Str(it.Value("review_focus")); !strings.Contains(focus, "Baseline") {
		t.Fatalf("fallback focus: %q", focus)
	}
	prev := it.Value("review_prev").(*pyjson.Object)
	comments := prev.Value("comments").([]any)
	if prev.Value("outcome") != "sent-back" || len(comments) != 1 || comments[0].(*pyjson.Object).Value("resolved") != false {
		t.Fatalf("previous round: %v", prev)
	}
	media := it.Value("media").([]any)
	if len(media) != 1 || media[0].(*pyjson.Object).Value("new") != true {
		t.Fatalf("new media flag: %v", media)
	}
	// The detail route: the diff hunks and the new attachment.
	info, err := st.ReviewInfo(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	diff := info.Value("diff").(*pyjson.Object)
	var adds []string
	for _, h := range diff.Value("hunks").([]any) {
		o := h.(*pyjson.Object)
		if o.Value("op") == "add" {
			for _, l := range o.Value("lines").([]any) {
				adds = append(adds, l.(string))
			}
		}
	}
	if !reflect.DeepEqual(adds, []string{"", "# Baseline", "acc 0.4"}) {
		t.Fatalf("added lines: %q", adds)
	}
	if len(info.Value("media_new").([]any)) != 1 || len(info.Value("rounds").([]any)) != 2 {
		t.Fatalf("info: %v", info)
	}
	// A note set while in review replaces the round's note, no new round.
	patch(t, st, uid, pyjson.NewObject("review_note", "See [Baseline](#baseline) and [x](#nope)"), agent)
	it = needsItem(t, st, uid)
	if it.Value("review_round") != int64(2) || !strings.Contains(pyjson.Str(it.Value("review_note")), "Baseline") {
		t.Fatalf("note update: %v", it)
	}
	warnings, err := st.ReviewNoteWarnings(ctx, uid, pyjson.Str(it.Value("review_note")))
	if err != nil || len(warnings) != 1 || !strings.Contains(pyjson.Str(warnings[0].(*pyjson.Object).Value("problem")), "nope") {
		t.Fatalf("warnings: %v %v", warnings, err)
	}
	// Accept closes the round; the next review compares with the accepted body.
	patch(t, st, uid, pyjson.NewObject("status", "done"), "human:ui")
	info, _ = st.ReviewInfo(ctx, uid)
	rounds := info.Value("rounds").([]any)
	if last := rounds[len(rounds)-1].(*pyjson.Object); last.Value("outcome") != "accepted" || last.Value("closed_by") != "human:ui" {
		t.Fatalf("closed round: %v", last)
	}
}

func TestReviewLegacyUnitWithoutRounds(t *testing.T) {
	st := reviewStore(t)
	// Created in review before review rounds: drop the table to simulate.
	uid := mkUnit(t, st, pyjson.NewObject("type", "artifact", "title", "Old", "status", "review",
		"body", "intro\n\n## Latest\nnew numbers"), "human:ui")
	if err := st.DB.Write(context.Background(), func(q sqlitedb.Querier) error {
		_, err := q.ExecContext(context.Background(), "DROP TABLE review_rounds")
		return err
	}, nil); err != nil {
		t.Fatal(err)
	}
	it := needsItem(t, st, uid)
	if it.Value("review_round") != int64(1) || it.Value("review_note") != "" || it.Value("review_changes") != nil {
		t.Fatalf("legacy item: %v", it)
	}
	if focus := pyjson.Str(it.Value("review_focus")); !strings.Contains(focus, "Latest") {
		t.Fatalf("legacy focus: %q", focus)
	}
}

func TestAnchorSlugsAndParsing(t *testing.T) {
	cases := map[string]string{"Results": "results", "时间重排 (v2)": "时间重排-v2", "  A -- b_c ": "a-b-c", "**Bold** `x`": "bold-x"}
	for in, want := range cases {
		if got := AnchorSlug(plainHeading(in)); got != want {
			t.Errorf("slug %q = %q, want %q", in, got, want)
		}
	}
	body := "# Results\n```\n# not a heading\n```\n## Results\nText {#key-claim}\n### [Link](x) title\n"
	if got := BodyAnchors(body); !reflect.DeepEqual(got, []string{"results", "results-1", "key-claim", "link-title"}) {
		t.Fatalf("body anchors: %v", got)
	}
	note := "Look at [时间重排](#时间重排), [T](T-0042#setup), [unit](E-0001) and media:421@0:08-0:12, [clip](media:7@1:02)"
	var got []string
	for _, a := range ParseAnchors(note) {
		got = append(got, a.Kind+":"+a.Unit+":"+a.Slug)
	}
	if !reflect.DeepEqual(got, []string{"heading::时间重排", "heading:T-0042:setup", "unit:E-0001:", "media::", "media::"}) {
		t.Fatalf("parsed: %v", got)
	}
	as := ParseAnchors(note)
	if as[3].Media != 7 || as[3].Start != 62 || as[4].Media != 421 || as[4].Start != 8 || as[4].End != 12 {
		t.Fatalf("media anchors: %+v %+v", as[3], as[4])
	}
}

func TestReviewGroupLifecycle(t *testing.T) {
	ctx := context.Background()
	st := reviewStore(t)
	agent := "agent:local/coder"
	task := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "HOI-215 B2", "status", "active"), agent)
	e1 := mkUnit(t, st, pyjson.NewObject("type", "evidence", "title", "Contact rate 0.67", "body", "# Claim\nrate 0.67\n独立评审：PASS。细节"), agent)
	e2 := mkUnit(t, st, pyjson.NewObject("type", "evidence", "title", "Penetration 2.1 mm", "status", "review", "body", "# Claim\n2.1 mm",
		"review_note", "Check the penetration figure in [Claim](#claim)."), agent)
	stats0, _ := st.ProjectPayload(ctx)
	units0 := stats0.Value("stats").(*pyjson.Object).Value("units")
	// One member is not a group.
	if _, err := st.CreateReviewGroup(ctx, pyjson.NewObject("title", "x", "members", []any{e1}), agent); err == nil ||
		!strings.Contains(err.Error(), "review_note") {
		t.Fatalf("single member: %v", err)
	}
	g, err := st.CreateReviewGroup(ctx, pyjson.NewObject("title", "HOI-215 B2 evidence", "summary", "Two claims",
		"review_note", "Start with [the rate](E-0001#claim) and [bad](E-0001#missing)",
		"members", []any{pyjson.NewObject("uid", e1, "note", "[claim](#claim)"), e2, e2}), agent)
	if err != nil {
		t.Fatal(err)
	}
	ref := pyjson.Str(g.Value("id"))
	if !strings.HasPrefix(ref, "RG-") || len(g.Value("members").([]any)) != 2 || len(g.Value("warnings").([]any)) != 1 {
		t.Fatalf("group: %v", g)
	}
	if u, _ := st.GetUnit(ctx, e1); len(u.Value("review_groups").([]any)) != 1 {
		t.Fatalf("unit review_groups: %v", u.Value("review_groups"))
	}
	if u, _ := st.GetUnit(ctx, task); u.Has("review_groups") {
		t.Fatal("review_groups on a unit in no group")
	}
	if v := g.Value("members").([]any)[0].(*pyjson.Object).Value("verdict"); v != "PASS" {
		t.Fatalf("verdict: %v", v)
	}
	// One Needs you card; not a unit anywhere else.
	payload, _ := st.ProjectPayload(ctx)
	found := false
	for _, it := range payload.Value("needs_you").([]any) {
		if it.(*pyjson.Object).Value("uid") == ref {
			found = true
		}
	}
	if !found {
		t.Fatal("group not in needs_you")
	}
	if payload.Value("stats").(*pyjson.Object).Value("units") != units0 {
		t.Fatal("group counted as a unit")
	}
	list, err := st.ListUnits(ctx, Filters{Limit: 100}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pyjson.Dumps(list)), "HOI-215 B2 evidence") {
		t.Fatal("group listed among units")
	}
	// Comments go on the group.
	if _, err := st.AddReviewGroupComment(ctx, ref, "Which seeds?", "local", "human:ui"); err != nil {
		t.Fatal(err)
	}
	// Send back e2 only; e1 accepted.
	out, err := st.DecideReviewGroup(ctx, ref, pyjson.NewObject("action", "accept", "note", "Penetration needs the baseline",
		"members", pyjson.NewObject(e2, "send_back")), "local", "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Value("status") != "sent-back" || out.Value("pending") != int64(0) {
		t.Fatalf("after partial: %v", out)
	}
	if u, _ := st.GetUnit(ctx, e2); u.Value("status") != "active" {
		t.Fatalf("e2 in review is sent back: %v", u.Value("status"))
	}
	cs, _ := st.Comments(ctx, e2)
	if len(cs) != 1 || !strings.Contains(pyjson.Str(cs[0].(*pyjson.Object).Value("body")), "review group "+ref) {
		t.Fatalf("send back comment: %v", cs)
	}
	if needsItem(t, st, ref) != nil {
		t.Fatal("sent-back group still waits on the person")
	}
	// Resubmit: round 2, e2 pending again, e1 stays accepted.
	patch(t, st, e2, pyjson.NewObject("body", "# Claim\n2.1 mm\nbaseline 3.0 mm"), agent)
	var noNote *ReviewNoteError
	if _, err := st.UpdateReviewGroup(ctx, ref, pyjson.NewObject("status", "review"), agent); !errors.As(err, &noNote) {
		t.Fatalf("resubmit without a note: %v", err)
	}
	if _, err := st.UpdateReviewGroup(ctx, ref, pyjson.NewObject("status", "review",
		"review_note", "Start with [the rate](E-0001#claim) and [bad](E-0001#missing)"), agent); !errors.As(err, &noNote) {
		t.Fatalf("resubmit with the old note: %v", err)
	}
	out, err = st.UpdateReviewGroup(ctx, ref, pyjson.NewObject("status", "review",
		"review_note", "Added the baseline (3.0 mm) to [the penetration claim](E-0002#claim) as asked."), agent)
	if err != nil {
		t.Fatal(err)
	}
	if out.Value("round") != int64(2) || out.Value("pending") != int64(1) {
		t.Fatalf("resubmitted: %v", out)
	}
	for _, m := range out.Value("members").([]any) {
		o := m.(*pyjson.Object)
		if o.Value("uid") == e2 && o.Value("changes").(*pyjson.Object).Value("added") != int64(1) {
			t.Fatalf("e2 changes since send back: %v", o.Value("changes"))
		}
	}
	out, err = st.DecideReviewGroup(ctx, ref, pyjson.NewObject("action", "accept"), "local", "human:ui", Who{})
	if err != nil || out.Value("closed") != true {
		t.Fatalf("accept all: %v %v", out, err)
	}
	if _, err := st.ReviewGroup(ctx, ref); err == nil {
		t.Fatal("closed group still readable")
	}
	// The record stays: rounds on the members and the activity log.
	info, _ := st.ReviewInfo(ctx, e1)
	rounds := info.Value("rounds").([]any)
	if len(rounds) != 1 || rounds[0].(*pyjson.Object).Value("outcome") != "accepted" ||
		!strings.Contains(pyjson.Str(rounds[0].(*pyjson.Object).Value("note")), "HOI-215 B2 evidence") {
		t.Fatalf("e1 rounds: %v", rounds)
	}
	act, _ := st.Activity(ctx, 50)
	if !strings.Contains(string(pyjson.Dumps(act)), "review.group.close") {
		t.Fatal("no close record")
	}
	_ = task
}

func TestReviewDeliveryAndVerdict(t *testing.T) {
	body := "HOI-215 B2（agent/HOI-215-b2-gpusim @ 0035966，gpu-3）。独立评审：接受需修改，8 项必改已在 0035966 修正。\n\nnworld=1"
	d := deliveryOf("Latency 4.7 ms", body).(*pyjson.Object)
	if d.Value("branch") != "agent/HOI-215-b2-gpusim" || d.Value("commit") != "0035966" || d.Value("label") != "HOI-215 B2" {
		t.Fatalf("delivery: %v", d)
	}
	if v := reviewVerdict(body); v != "接受需修改" {
		t.Fatalf("verdict: %q", v)
	}
	b7 := deliveryOf("x", "HOI-215 B7（demo-repo PR #2，exp/b7-gpu-sims @ 243d2e1，gpu-3）。").(*pyjson.Object)
	if b7.Value("label") != "HOI-215 B7" || b7.Value("branch") != "exp/b7-gpu-sims" || b7.Value("commit") != "243d2e1" {
		t.Fatalf("delivery with text before the branch: %v", b7)
	}
	if deliveryOf("x", "## Summary\nno branch here") != nil {
		t.Fatal("delivery without branch or commit")
	}
}
