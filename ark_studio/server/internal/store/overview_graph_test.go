package store

import (
	"context"
	"fmt"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

// Home's header graph (GraphFilter): the newest N live units of the chosen
// types/statuses/window, the links among them, every count reported, and
// the overview served from the cache until the project changes.
func TestOverviewGraphFilter(t *testing.T) {
	ctx := context.Background()
	st, err := Open(t.TempDir(), NewManifest(pyjson.NewObject("id", "prj_ov", "name", "Overview")), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var uids []string
	for i := 0; i < 12; i++ {
		kind := "task"
		if i%3 == 0 {
			kind = "claim"
		}
		u, err := st.CreateUnit(ctx, pyjson.NewObject("type", kind, "title", fmt.Sprintf("n%d", i)), "human:ui", Who{})
		if err != nil {
			t.Fatal(err)
		}
		uids = append(uids, pyjson.Str(u.Value("uid")))
	}
	for i := 1; i < 12; i++ {
		if _, err := st.CreateLink(ctx, uids[i], uids[i-1], "part_of", "", "human:ui"); err != nil {
			t.Fatal(err)
		}
	}
	// n0 oldest ... n11 newest; n11 is archived (not live).
	err = st.DB.Write(ctx, func(q sqlitedb.Querier) error {
		for i, uid := range uids {
			if _, err := q.ExecContext(ctx, "UPDATE units SET updated_at=? WHERE uid=?", fmt.Sprintf("2026-01-%02dT00:00:00+00:00", i+1), uid); err != nil {
				return err
			}
		}
		_, err := q.ExecContext(ctx, "UPDATE units SET archived=1 WHERE uid=?", uids[11])
		return err
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	graphOf := func(f GraphFilter) *pyjson.Object {
		t.Helper()
		data, err := st.ProjectOverview(ctx, nil, f)
		if err != nil {
			t.Fatal(err)
		}
		return data.Value("graph").(*pyjson.Object)
	}
	ids := func(g *pyjson.Object) []int64 {
		var out []int64
		for _, n := range g.Value("nodes").([]any) {
			out = append(out, n.(*pyjson.Object).Value("id").(int64))
		}
		return out
	}
	g := graphOf(GraphFilter{Limit: 4})
	if got := ids(g); fmt.Sprint(got) != "[11 10 9 8]" {
		t.Fatalf("newest four live nodes, newest first: %v", got)
	}
	if g.Value("total") != int64(11) || g.Value("matched") != int64(11) || g.Value("limit") != int64(4) {
		t.Fatalf("counts: %v %v %v", g.Value("total"), g.Value("matched"), g.Value("limit"))
	}
	edges := g.Value("edges").([]any)
	if len(edges) != 3 {
		t.Fatalf("links among the shown nodes only: %d", len(edges))
	}
	// degree counts every live link (n11 is archived: n10 has one link).
	for _, n := range g.Value("nodes").([]any) {
		o := n.(*pyjson.Object)
		want := int64(2)
		if o.Value("id") == int64(11) {
			want = 1
		}
		if o.Value("degree") != want {
			t.Fatalf("degree of %v: %v", o.Value("id"), o.Value("degree"))
		}
	}
	// Types and a time window; 0 = all.
	g = graphOf(GraphFilter{Limit: 100, Types: []string{"claim"}})
	if got := ids(g); fmt.Sprint(got) != "[10 7 4 1]" || g.Value("matched") != int64(4) {
		t.Fatalf("claims: %v matched %v", got, g.Value("matched"))
	}
	g = graphOf(GraphFilter{Since: "2026-01-09"})
	if got := ids(g); fmt.Sprint(got) != "[11 10 9]" {
		t.Fatalf("window: %v", got)
	}
	g = graphOf(GraphFilter{Statuses: []string{"done"}})
	if len(ids(g)) != 0 || g.Value("total") != int64(11) {
		t.Fatalf("no done nodes: %v", ids(g))
	}
	if all := graphOf(GraphFilter{}); len(ids(all)) != 11 || len(all.Value("edges").([]any)) != 10 {
		t.Fatalf("all: %d nodes %d edges", len(ids(all)), len(all.Value("edges").([]any)))
	}
	// Cached until a write; the caller's copy may be extended freely.
	first, _ := st.ProjectOverview(ctx, nil, GraphFilter{Limit: 4})
	first.Set("setup", "x")
	second, _ := st.ProjectOverview(ctx, nil, GraphFilter{Limit: 4})
	if second.Has("setup") || second.Value("graph") != first.Value("graph") {
		t.Fatal("a cache hit returns a fresh copy of the same overview")
	}
	if _, err := st.CreateUnit(ctx, pyjson.NewObject("type", "task", "title", "new"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	third, _ := st.ProjectOverview(ctx, nil, GraphFilter{Limit: 4})
	if third.Value("graph") == first.Value("graph") || ids(third.Value("graph").(*pyjson.Object))[0] != 13 {
		t.Fatal("a write invalidates the cached overview")
	}
}

func TestOverviewGraphReadsOnlyItsLinks(t *testing.T) {
	calls := [][]int64{}
	fetch := func(ids []int64) ([]sqlitedb.Row, error) { calls = append(calls, ids); return nil, nil }
	if _, err := overviewGraph(nil, fetch, GraphFilter{Limit: 5}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] == nil {
		t.Fatalf("a limited graph asks for its own nodes' links: %v", calls)
	}
}
