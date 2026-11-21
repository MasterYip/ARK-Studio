package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// The fixture of tests/js/review_requests.test.mjs ("fallback grouping"):
// three deliveries of one task, a summary without a branch, an unrelated
// unit and a decision.
func needsFixture() []*needsCandidate {
	at := func(hh, mm, ss int) time.Time { return time.Date(2026, 11, 19, hh, mm, ss, 0, time.UTC) }
	task := []needsRef{{uid: "T-0020", title: "HOI-215 · Large sandbox environments"}}
	type del struct{ label, branch, commit string }
	b1 := del{"HOI-215 B1", "agent/HOI-215-b1-codebase", "4cf0d04"}
	b2 := del{"HOI-215 B2", "agent/HOI-215-b2-gpusim", "0035966"}
	b3 := del{"HOI-215 B3", "agent/HOI-215-b3-bench", "c9e7ad4"}
	unit := func(uid, typ string, since time.Time, d del, parents []needsRef, links []needsLink) *needsCandidate {
		return &needsCandidate{uid: uid, typ: typ, kind: "review", title: uid + " claim 1.5 ms", since: since,
			branch: d.branch, commit: d.commit, label: d.label, parents: parents, links: links}
	}
	list := []*needsCandidate{{uid: "D-0001", typ: "decision", kind: "decision", title: "Which seed?"}}
	var summaryLinks []needsLink
	for _, uid := range []string{"E-0064", "E-0065", "E-0066", "E-0067"} {
		summaryLinks = append(summaryLinks, needsLink{uid, "derived_from", "out"})
	}
	list = append(list, unit("E-0068", "evidence", at(21, 29, 40), del{}, task, summaryLinks))
	for i, uid := range []string{"E-0064", "E-0065", "E-0066", "E-0067"} {
		list = append(list, unit(uid, "evidence", at(21, 28, 32+i), b3, task, []needsLink{{"E-0068", "derived_from", "in"}}))
	}
	for i, uid := range []string{"E-0058", "E-0059", "E-0060", "E-0061", "E-0062", "E-0063"} {
		list = append(list, unit(uid, "evidence", at(17, 45, 7+i), b1, task, nil))
	}
	list = append(list, unit("C-0007", "claim", at(17, 45, 13), b1, task, nil))
	for i, uid := range []string{"E-0049", "E-0050", "E-0051", "E-0052", "E-0053", "E-0054", "E-0055", "E-0056", "E-0057"} {
		list = append(list, unit(uid, "evidence", at(17, 30, 39+i), b2, task, nil))
	}
	list = append(list, unit("C-0006", "claim", at(17, 31, 21), b2, task, nil))
	list = append(list, unit("E-0047", "evidence", at(17, 31, 22), b2, nil, nil))
	list = append(list, unit("E-0048", "evidence", at(13, 18, 53), del{}, []needsRef{{uid: "T-0018", title: "HOI-214 · Literature"}}, nil))
	return list
}

func TestNeedsAutoGroupsOneCardPerDelivery(t *testing.T) {
	list := needsFixture()
	cards := needsAutoGroups(list)
	var summary []string
	seen := map[*pyjson.Object]bool{}
	for i, c := range list {
		card, ok := cards[i]
		if !ok {
			summary = append(summary, c.uid)
			continue
		}
		if seen[card] {
			continue
		}
		seen[card] = true
		summary = append(summary, fmt.Sprintf("%s:%d", card.Value("title"), len(card.Value("members").([]any))))
	}
	want := []string{"D-0001", "HOI-215 B3:5", "HOI-215 B1:7", "HOI-215 B2:11", "E-0048"}
	if !reflect.DeepEqual(summary, want) {
		t.Fatalf("cards %v, want %v", summary, want)
	}
	var b2, b3 *pyjson.Object
	for card := range seen {
		switch card.Value("title") {
		case "HOI-215 B2":
			b2 = card
		case "HOI-215 B3":
			b3 = card
		}
	}
	if b2.Value("primary") != "C-0006" || b2.Value("members").([]any)[0] != "C-0006" {
		t.Fatalf("a claim before its evidence: %v", b2.Value("members"))
	}
	if p := b2.Value("parent").(*pyjson.Object); p.Value("uid") != "T-0020" {
		t.Fatalf("parent %v", p)
	}
	if b2.Value("delivery") != "branch:agent/HOI-215-b2-gpusim" || b2.Value("span") != int64(1) {
		t.Fatalf("delivery %v span %v", b2.Value("delivery"), b2.Value("span"))
	}
	if b3.Value("delivery") != "" {
		t.Fatalf("the summary names no branch: %v", b3.Value("delivery"))
	}
	linked := false
	for _, r := range b3.Value("reasons").([]any) {
		if r.(*pyjson.Object).Value("link") != nil {
			linked = true
		}
	}
	if !linked {
		t.Fatal("B3 is linked")
	}
}

func TestNeedsGroupReasonRules(t *testing.T) {
	at := func(d, hh, mm int) time.Time { return time.Date(2026, 11, d, hh, mm, 0, 0, time.UTC) }
	task := []needsRef{{uid: "T-0020"}}
	mk := func(uid string, since time.Time, branch string, parents []needsRef) *needsCandidate {
		return &needsCandidate{uid: uid, typ: "evidence", kind: "review", since: since, branch: branch, parents: parents}
	}
	if needsGroupReason(mk("E-1", at(19, 10, 0), "", task), mk("E-2", at(19, 10, 20), "", task)) == nil {
		t.Fatal("same parent, 20 min apart")
	}
	if needsGroupReason(mk("E-1", at(19, 10, 0), "", task), mk("E-3", at(19, 10, 45), "", task)) != nil {
		t.Fatal("45 min apart, no delivery")
	}
	if needsGroupReason(mk("E-1", at(19, 10, 0), "b1", nil), mk("E-3", at(19, 10, 45), "b1", nil)) == nil {
		t.Fatal("same branch, no link, same day")
	}
	if needsGroupReason(mk("E-1", at(19, 10, 0), "b1", nil), mk("E-3", at(21, 10, 0), "b1", nil)) != nil {
		t.Fatal("same branch, no link, days apart")
	}
	if needsGroupReason(mk("E-1", at(19, 10, 0), "b1", task), mk("E-2", at(19, 10, 20), "b2", task)) != nil {
		t.Fatal("different deliveries")
	}
	lone := mk("E-1", at(19, 10, 0), "", nil)
	if needsGroupReason(lone, mk("E-2", at(19, 10, 20), "", nil)) != nil {
		t.Fatal("no link, no shared parent")
	}
	lone.links = []needsLink{{"E-2", "supports", "out"}}
	if needsGroupReason(lone, mk("E-2", at(19, 10, 20), "", nil)) == nil {
		t.Fatal("linked")
	}
	if len(needsAutoGroups([]*needsCandidate{lone})) != 0 {
		t.Fatal("one member is never a group")
	}
	if needsShortTitle("HOI-215 · Large sandbox") != "HOI-215" {
		t.Fatal("short title")
	}
}

// Needs you items carry the card: every member of a delivery the same one,
// a lone review item null, a decision none.
func TestNeedsYouAutoGroupField(t *testing.T) {
	st := reviewStore(t)
	ctx := context.Background()
	task := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Sandbox delivery", "status", "active"), "agent")
	head := "D1（agent/review-test @ abc1234）。独立评审：PASS。\n\n# Results\n"
	var members []string
	for _, v := range []string{"a", "b"} {
		members = append(members, mkUnit(t, st, pyjson.NewObject("type", "evidence", "title", "Latency "+v, "status", "review",
			"body", head+v, "links", []any{pyjson.NewObject("target", task, "relation", "part_of")}), "agent"))
	}
	lone := mkUnit(t, st, pyjson.NewObject("type", "experiment", "title", "Ablation", "status", "review", "body", "x"), "agent")
	mkUnit(t, st, pyjson.NewObject("type", "decision", "title", "Pick", "status", "open"), "agent")
	payload, err := st.ProjectPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byUID := map[string]*pyjson.Object{}
	for _, it := range payload.Value("needs_you").([]any) {
		o := it.(*pyjson.Object)
		byUID[pyjson.Str(o.Value("uid"))] = o
	}
	g0, _ := byUID[members[0]].Value("auto_group").(*pyjson.Object)
	g1, _ := byUID[members[1]].Value("auto_group").(*pyjson.Object)
	if g0 == nil || g1 == nil || g0.Value("key") != g1.Value("key") {
		t.Fatalf("members share a card: %v %v", g0, g1)
	}
	if g0.Value("title") != "D1" || g0.Value("delivery") != "branch:agent/review-test" {
		t.Fatalf("card %v", g0)
	}
	if v, ok := byUID[lone].Get("auto_group"); !ok || v != nil {
		t.Fatalf("lone item: %v %v", v, ok)
	}
	for uid, o := range byUID {
		if o.Value("kind") == "decision" {
			if _, ok := o.Get("auto_group"); ok {
				t.Fatalf("decision %s has auto_group", uid)
			}
		}
	}
}
