package store

import (
	"context"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// batchActivity lists "action uid batch" of the activity rows written with
// a batch id, in order.
func batchActivity(t *testing.T, st *Store, batch string) []string {
	t.Helper()
	var out []string
	err := st.read(context.Background(), func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(context.Background(), q, "SELECT action, detail_json FROM activity ORDER BY id")
		for _, r := range rows {
			o := parseDetail(r.Get("detail_json"))
			if o == nil || pyjson.Str(o.Value("batch")) != batch {
				continue
			}
			uid := o.Value("uid")
			if uid == nil {
				uid = o.Value("from")
			}
			out = append(out, r.Str("action")+" "+pyjson.Str(uid))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func unitField(t *testing.T, st *Store, uid, key string) any {
	t.Helper()
	u, err := st.GetUnit(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	return u.Value(key)
}

func TestBatchUpdateAppliesOneTransactionWithBatchIDs(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	a, b, c := trashUnit(t, st, "A"), trashUnit(t, st, "B"), trashUnit(t, st, "C")
	if _, err := st.UpdateUnit(ctx, b, pyjson.NewObject("tags", []any{"keep", "drop"}), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArchiveUnit(ctx, c, "human:ui"); err != nil {
		t.Fatal(err)
	}
	items := []BatchItem{
		{Ref: a, Patch: pyjson.NewObject("status", "active"), TagsAdd: []string{"shared"}, TagsRemove: []string{"drop"}},
		{Ref: b, Patch: pyjson.NewObject("status", "active"), TagsAdd: []string{"shared"}, TagsRemove: []string{"drop"}},
		{Ref: c, Patch: pyjson.NewObject("status", "active")},
		{Ref: "N-9999", Patch: pyjson.NewObject("status", "active")},
	}
	out, err := st.BatchUpdate(ctx, items, "b_test1", "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(out.Value("updated").([]any)); got != 2 {
		t.Fatalf("updated: %d (%s)", got, pyjson.Dumps(out))
	}
	if got := len(out.Value("skipped").([]any)); got != 2 {
		t.Fatalf("skipped: %s", pyjson.Dumps(out.Value("skipped")))
	}
	for _, uid := range []string{a, b} {
		if unitField(t, st, uid, "status") != "active" {
			t.Fatalf("%s status not updated", uid)
		}
	}
	tags := strings.Join(toStrings(unitField(t, st, b, "tags")), ",")
	if !strings.Contains(tags, "keep") || !strings.Contains(tags, "shared") || strings.Contains(tags, "drop") {
		t.Fatalf("tags of %s: %s", b, tags)
	}
	got := batchActivity(t, st, "b_test1")
	if len(got) != 2 || got[0] != "unit.update "+a || got[1] != "unit.update "+b {
		t.Fatalf("batch activity: %v", got)
	}
	// Same values again: nothing changes, no new activity.
	out, err = st.BatchUpdate(ctx, items[:2], "b_test2", "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Value("unchanged").([]any)) != 2 || len(batchActivity(t, st, "b_test2")) != 0 {
		t.Fatalf("repeat: %s", pyjson.Dumps(out))
	}
}

func TestBatchUpdateSkipsInvalidUnitAndKeepsOthers(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	a, b := trashUnit(t, st, "A"), trashUnit(t, st, "B")
	if _, err := st.UpdateUnit(ctx, b, pyjson.NewObject("end_at", "2026-01-02", "start_at", "2026-01-01"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	// start_at after B's end_at is invalid for B only.
	out, err := st.BatchUpdate(ctx, []BatchItem{
		{Ref: a, Patch: pyjson.NewObject("start_at", "2026-03-01")},
		{Ref: b, Patch: pyjson.NewObject("start_at", "2026-03-01")},
	}, "b_dates", "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	skipped := out.Value("skipped").([]any)
	if len(skipped) != 1 || pyjson.Str(skipped[0].(*pyjson.Object).Value("uid")) != b {
		t.Fatalf("skipped: %s", pyjson.Dumps(skipped))
	}
	if unitField(t, st, a, "start_at") != "2026-03-01" || unitField(t, st, b, "start_at") != "2026-01-01" {
		t.Fatal("dates")
	}
	if got := batchActivity(t, st, "b_dates"); len(got) != 1 {
		t.Fatalf("activity of the rolled back unit kept: %v", got)
	}
	if _, err := st.BatchUpdate(ctx, []BatchItem{{Ref: a, Patch: pyjson.NewObject("title", "x")}}, "", "human:ui", Who{}); err == nil {
		t.Fatal("title accepted in a batch")
	}
}

func toStrings(v any) []string {
	var out []string
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		for _, item := range x {
			out = append(out, pyjson.Str(item))
		}
	}
	return out
}

func TestGroupUnderAndUngroup(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	root := trashUnit(t, st, "Root")
	a, b, c, other := trashUnit(t, st, "A"), trashUnit(t, st, "B"), trashUnit(t, st, "C"), trashUnit(t, st, "Other")
	for _, child := range []string{a, b, c} {
		trashLink(t, st, child, root, "part_of")
	}
	trashLink(t, st, a, other, "supports")
	before := trashEdges(t, st)
	// Not siblings: rejected, nothing written.
	if _, err := st.GroupUnder(ctx, []any{a, other}, root, "part_of", pyjson.NewObject("title", "G"), "", "human:ui", Who{}); err == nil {
		t.Fatal("non-siblings grouped")
	}
	if trashEdges(t, st) != before {
		t.Fatal("failed group changed links")
	}
	out, err := st.GroupUnder(ctx, []any{a, b}, root, "part_of",
		pyjson.NewObject("title", "Group", "type", "note", "tags", []any{"seq:1"}), "b_group", "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	g := pyjson.Str(out.Value("unit").(*pyjson.Object).Value("uid"))
	want := strings.Join([]string{a + " part_of " + g, a + " supports " + other, b + " part_of " + g,
		c + " part_of " + root, g + " part_of " + root}, ", ")
	if got := trashEdges(t, st); got != want {
		t.Fatalf("after group:\n got %s\nwant %s", got, want)
	}
	if len(out.Value("links").([]any)) != 3 || len(out.Value("removed_links").([]any)) != 2 {
		t.Fatalf("group reply: %s", pyjson.Dumps(out))
	}
	acts := strings.Join(batchActivity(t, st, "b_group"), "; ")
	for _, wantAct := range []string{"unit.create " + g, "unit.group " + g, "link.delete " + a, "link.create " + b} {
		if !strings.Contains(acts, wantAct) {
			t.Fatalf("group activity %q missing: %s", wantAct, acts)
		}
	}
	// Undo: children back under root, the group node in the bin.
	if _, err := st.Ungroup(ctx, g, "part_of", "b_undo", "human:ui"); err != nil {
		t.Fatal(err)
	}
	if got := trashEdges(t, st); got != before {
		t.Fatalf("after ungroup:\n got %s\nwant %s", got, before)
	}
	if uids := trashUIDs(t, st); len(uids) != 1 || uids[0] != g {
		t.Fatalf("bin: %v", uids)
	}
	// Top-level nodes group under a new top-level node.
	x, y := trashUnit(t, st, "X"), trashUnit(t, st, "Y")
	out, err = st.GroupUnder(ctx, []any{x, y}, nil, "part_of", pyjson.NewObject("title", "Top"), "", "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	top := pyjson.Str(out.Value("unit").(*pyjson.Object).Value("uid"))
	if got := trashEdges(t, st); !strings.Contains(got, x+" part_of "+top) || !strings.Contains(got, y+" part_of "+top) {
		t.Fatalf("top-level group: %s", got)
	}
	if _, err := st.GroupUnder(ctx, []any{a, x}, nil, "part_of", pyjson.NewObject("title", "Bad"), "", "human:ui", Who{}); err == nil {
		t.Fatal("a child grouped as top level")
	}
}

func TestUnitsBatchReportsTopicsAndMissing(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	a, b := trashUnit(t, st, "A"), trashUnit(t, st, "B")
	topic, err := st.CreateUnit(ctx, pyjson.NewObject("type", "topic", "title", "T", "members", []any{a}), "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := st.UnitsBatch(ctx, []any{a, b, "N-9999"})
	if err != nil {
		t.Fatal(err)
	}
	units := out.Value("units").([]any)
	if len(units) != 2 || len(out.Value("missing").([]any)) != 1 {
		t.Fatalf("batch read: %s", pyjson.Dumps(out))
	}
	first := units[0].(*pyjson.Object)
	topics := first.Value("topics").([]any)
	if pyjson.Str(first.Value("uid")) != a || len(topics) != 1 ||
		topics[0].(*pyjson.Object).Value("uid") != topic.Value("uid") {
		t.Fatalf("topics: %s", pyjson.Dumps(first.Value("topics")))
	}
	if len(units[1].(*pyjson.Object).Value("topics").([]any)) != 0 {
		t.Fatal("B is in no topic")
	}
}
