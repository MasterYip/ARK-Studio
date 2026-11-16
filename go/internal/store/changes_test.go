package store

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"arkstudio/go/internal/pyjson"
)

// applyDelta is the documented client rule (tests/python/store/test_changes.py
// apply_delta, docs/development/client-cache.md).
func applyDelta(snapshot, delta *pyjson.Object) *pyjson.Object {
	touched := map[int64]bool{}
	for _, id := range delta.Value("touched").([]any) {
		touched[id.(int64)] = true
	}
	drop := map[int64]bool{}
	for _, id := range delta.Value("deleted").([]any) {
		drop[id.(int64)] = true
	}
	units := delta.Value("units").([]any)
	for _, u := range units {
		drop[u.(*pyjson.Object).Value("id").(int64)] = true
	}
	var nodes []any
	for _, n := range anyList(snapshot.Value("nodes")) {
		if !drop[n.(*pyjson.Object).Value("id").(int64)] {
			nodes = append(nodes, n)
		}
	}
	nodes = append(nodes, units...)
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i].(*pyjson.Object), nodes[j].(*pyjson.Object)
		ta, tb := pyjson.Str(a.Value("updated_at")), pyjson.Str(b.Value("updated_at"))
		if ta != tb {
			return ta > tb
		}
		return a.Value("id").(int64) > b.Value("id").(int64)
	})
	var edges []any
	for _, e := range anyList(snapshot.Value("edges")) {
		o := e.(*pyjson.Object)
		if !touched[o.Value("source").(int64)] && !touched[o.Value("target").(int64)] {
			edges = append(edges, e)
		}
	}
	edges = append(edges, delta.Value("edges").([]any)...)
	sort.SliceStable(edges, func(i, j int) bool {
		return edges[i].(*pyjson.Object).Value("id").(int64) < edges[j].(*pyjson.Object).Value("id").(int64)
	})
	return pyjson.NewObject("nodes", orEmpty(nodes), "edges", orEmpty(edges), "total", int64(len(nodes)))
}

func comparableSnapshot(s *pyjson.Object) []byte {
	return pyjson.Dumps(pyjson.NewObject("nodes", orEmpty(anyList(s.Value("nodes"))),
		"edges", orEmpty(anyList(s.Value("edges"))), "total", s.Value("total")))
}

func TestChangesDeltaEqualsFullReload(t *testing.T) {
	st := seedStore(t)
	ctx := context.Background()
	who := Who{}
	mk := func(kv ...any) *pyjson.Object { return pyjson.NewObject(kv...) }
	steps := []func() error{
		func() error { _, err := st.UpdateUnit(ctx, "5", mk("title", "renamed"), "human:ui", who); return err },
		func() error { _, err := st.UpdateUnit(ctx, "50", mk("assignee", "alice"), "human:ui", who); return err },
		func() error { _, err := st.AddComment(ctx, "7", "hi", "comment", nil, "local", "human:ui"); return err },
		func() error {
			_, err := st.CreateLink(ctx, int64(3), int64(9), "supports", nil, "human:ui")
			return err
		},
		func() error { _, err := st.ArchiveUnit(ctx, "20", "human:ui"); return err },
		func() error { _, err := st.RestoreUnit(ctx, "20", "human:ui"); return err },
		func() error {
			_, err := st.CreateUnit(ctx, mk("title", "new", "type", "note", "body", strings.Repeat("x", 300)), "human:ui", who)
			return err
		},
	}
	for i, step := range steps {
		for _, bodies := range []bool{true, false} {
			before, err := st.GraphSnapshot(ctx, bodies)
			if err != nil {
				t.Fatal(err)
			}
			if bodies {
				if err := step(); err != nil {
					t.Fatalf("step %d: %v", i, err)
				}
			}
			delta, err := st.Changes(ctx, pyjson.Str(before.Value("cursor")), bodies, ChangesUnitLimit)
			if err != nil {
				t.Fatal(err)
			}
			after, err := st.GraphSnapshot(ctx, bodies)
			if err != nil {
				t.Fatal(err)
			}
			if delta.Value("reset") != false || delta.Value("cursor") != after.Value("cursor") {
				t.Fatalf("step %d: reset or cursor: %s", i, pyjson.Dumps(delta))
			}
			if got, want := comparableSnapshot(applyDelta(before, delta)), comparableSnapshot(after); !bytes.Equal(got, want) {
				t.Fatalf("step %d (bodies=%v): delta merge differs from a full reload", i, bodies)
			}
		}
	}
	cursor, err := st.ChangeCursor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	logID, _, _ := strings.Cut(cursor, ".")
	for _, bad := range []string{"", "bogus.1", logID + ".-1", logID + ".999999", "x" + cursor, logID} {
		delta, err := st.Changes(ctx, bad, true, ChangesUnitLimit)
		if err != nil {
			t.Fatal(err)
		}
		if delta.Value("reset") != true || delta.Value("reason") != "cursor" {
			t.Fatalf("cursor %q: %s", bad, pyjson.Dumps(delta))
		}
	}
	if delta, _ := st.Changes(ctx, logID+".0", true, 3); delta.Value("reason") != "size" {
		t.Fatalf("unit limit: %s", pyjson.Dumps(delta))
	}
	if _, err := st.ResetChangeLogID(ctx); err != nil {
		t.Fatal(err)
	}
	if delta, _ := st.Changes(ctx, cursor, true, ChangesUnitLimit); delta.Value("reset") != true {
		t.Fatalf("after a new log identity: %s", pyjson.Dumps(delta))
	}
	if n, err := st.PruneChangeLog(ctx, 5); err != nil || n == 0 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if delta, _ := st.Changes(ctx, fmt.Sprintf("%s.1", strings.SplitN(must(st.ChangeCursor(ctx)), ".", 2)[0]), true,
		ChangesUnitLimit); delta.Value("reason") != "cursor" {
		t.Fatalf("pruned cursor: %s", pyjson.Dumps(delta))
	}
}

func must(s string, err error) string {
	if err != nil {
		panic(err)
	}
	return s
}

func anyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []*pyjson.Object:
		out := make([]any, len(x))
		for i, o := range x {
			out[i] = o
		}
		return out
	}
	return nil
}
