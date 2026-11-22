package store

import (
	"context"
	"sort"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// A decision or review unit whose status is closed (done, abandoned,
// archived) leaves Needs you, the project picker cards and the automatic
// groups alike, whether it was created closed or closed later.
func TestNeedsYouSkipsClosedStatuses(t *testing.T) {
	st := reviewStore(t)
	ctx := context.Background()
	want := map[string]bool{}
	decisions := map[string]string{}
	for _, status := range KnownStatuses {
		uid := mkUnit(t, st, pyjson.NewObject("type", "decision", "title", "Decide "+status, "status", status), "agent")
		decisions[uid] = status
		want[uid] = status == "review" || IsOpenDecision("decision", status)
	}
	// Closed after the fact, the way the user archived D-0007 and friends.
	for _, status := range ClosedStatuses {
		uid := mkUnit(t, st, pyjson.NewObject("type", "decision", "title", "Later "+status, "status", "open"), "agent")
		patch(t, st, uid, pyjson.NewObject("status", status), "user")
		decisions[uid] = status
		want[uid] = false
	}
	// Two review units of one delivery form an automatic group; once both
	// are archived or abandoned, neither they nor the group show.
	task := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Delivery", "status", "active"), "agent")
	head := "D1（agent/needs-closed @ abc1234）。\n\n# Results\n"
	var reviews []string
	for i, closed := range []string{"archived", "abandoned"} {
		uid := mkUnit(t, st, pyjson.NewObject("type", "evidence", "title", "Result "+string(rune('a'+i)), "status", "review",
			"body", head+closed, "links", []any{pyjson.NewObject("target", task, "relation", "part_of")}), "agent")
		reviews = append(reviews, uid)
	}
	if item := needsItem(t, st, reviews[0]); item == nil || item.Value("auto_group") == nil {
		t.Fatalf("review %s not grouped while open: %v", reviews[0], item)
	}
	for i, closed := range []string{"archived", "abandoned"} {
		patch(t, st, reviews[i], pyjson.NewObject("status", closed), "user")
		want[reviews[i]] = false
	}

	payload, err := st.ProjectPayload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, it := range payload.Value("needs_you").([]any) {
		o := it.(*pyjson.Object)
		listed[pyjson.Str(o.Value("uid"))] = true
		if g, _ := o.Value("auto_group").(*pyjson.Object); g != nil {
			t.Fatalf("auto group left behind: %v", g)
		}
	}
	var cardKeys []string
	err = st.read(ctx, func(q sqlitedb.Querier) error {
		cards, err := NeedsCards(ctx, q)
		for _, c := range cards {
			for _, m := range c.Items {
				cardKeys = append(cardKeys, m.Key)
			}
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	carded := map[string]bool{}
	for _, k := range cardKeys {
		carded[strings.TrimPrefix(k, "u:")] = true
	}
	var bad []string
	for uid, in := range want {
		if listed[uid] != in || carded[uid] != in {
			bad = append(bad, uid+" ("+decisions[uid]+")")
		}
		u, err := st.GetUnit(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		pending := needsPending(pyjson.Str(u.Value("type")), pyjson.Str(u.Value("status")), false) != ""
		if pending != in {
			bad = append(bad, uid+" needsPending")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Fatalf("Needs you, cards and needsPending disagree with the closed statuses: %v (listed %v, cards %v)", bad, listed, cardKeys)
	}
}

func TestOpenDecisionHelpers(t *testing.T) {
	for _, s := range KnownStatuses {
		closed := s == "done" || s == "abandoned" || s == "archived"
		if IsClosedStatus(s) != closed || IsOpenDecision("decision", s) == closed || IsOpenDecision("task", s) {
			t.Fatalf("status %s: closed %v open decision %v", s, IsClosedStatus(s), IsOpenDecision("decision", s))
		}
	}
	if got := needsYouCond("u."); !strings.Contains(got, "u.status NOT IN ('done','abandoned','archived')") {
		t.Fatalf("cond %s", got)
	}
}
