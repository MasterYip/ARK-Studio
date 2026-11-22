package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

func moveStore(t *testing.T, id string) *Store {
	t.Helper()
	st, err := Open(t.TempDir(), NewManifest(pyjson.NewObject("id", id, "name", id)), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func moveUnitOf(t *testing.T, st *Store, typ, title, body string) string {
	t.Helper()
	u, err := st.CreateUnit(context.Background(), pyjson.NewObject("type", typ, "title", title, "body", body), "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	return pyjson.Str(u.Value("uid"))
}

func moveMediaOf(t *testing.T, st *Store, uid, name, content string) int64 {
	t.Helper()
	rel := "assets/media/" + name
	path := filepath.Join(st.Path, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := st.AddMedia(context.Background(), uid, rel, "image", name, "human:ui")
	if err != nil {
		t.Fatal(err)
	}
	return m.Value("id").(int64)
}

func moveRow(t *testing.T, st *Store, uid string) sqlitedb.Row {
	t.Helper()
	var row sqlitedb.Row
	err := st.read(context.Background(), func(q sqlitedb.Querier) error {
		var err error
		row, err = unitRow(context.Background(), q, uid)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func moveCount(t *testing.T, st *Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	err := st.read(context.Background(), func(q sqlitedb.Querier) error {
		r, _, err := sqlitedb.QueryOne(context.Background(), q, query, args...)
		n = r.Int("n")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type moveFixture struct {
	src, dst                  *Store
	parent, child, stay, note string
	ownMedia, stayMedia       int64
	req                       MoveRequest
}

// newMoveFixture: source A with a task tree (parent <- child, part_of), a
// staying task linked to the parent, and a note in the tree whose body
// references both and two media rows; target B already holds T-0001 and
// T-0002, so a move has to renumber.
func newMoveFixture(t *testing.T, targetHasUnits bool) *moveFixture {
	t.Helper()
	ctx := context.Background()
	f := &moveFixture{src: moveStore(t, "prj_a"), dst: moveStore(t, "prj_b")}
	f.parent = moveUnitOf(t, f.src, "task", "Parent", "root")
	f.child = moveUnitOf(t, f.src, "task", "Child", "see [[T-0001]] and `T-0003`")
	f.stay = moveUnitOf(t, f.src, "task", "Stays", "stays behind")
	f.ownMedia = moveMediaOf(t, f.src, f.parent, "own.png", "own-bytes")
	f.stayMedia = moveMediaOf(t, f.src, f.stay, "stay.png", "stay-bytes")
	body := "child [[" + f.child + "]], stays [[" + f.stay + "]]\n" +
		"![own](media:" + pyjson.Str(f.ownMedia) + ") ![other](/api/media/" + pyjson.Str(f.stayMedia) + "/raw)\n" +
		"```\n[[" + f.child + "]] media:" + pyjson.Str(f.ownMedia) + "\n```\n"
	f.note = moveUnitOf(t, f.src, "note", "Note", body)
	trashLink(t, f.src, f.child, f.parent, "part_of")
	trashLink(t, f.src, f.note, f.parent, "part_of")
	trashLink(t, f.src, f.parent, f.stay, "relates_to")
	trashLink(t, f.src, f.stay, f.child, "supports")
	c, err := f.src.AddComment(ctx, f.parent, "look at media:"+pyjson.Str(f.ownMedia)+" and [["+f.child+"]]", "comment", nil, "alice", "human:alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.src.AddComment(ctx, f.parent, "reply", "comment", pyjson.Str(c.Value("id")), "bob", "human:bob"); err != nil {
		t.Fatal(err)
	}
	if targetHasUnits {
		moveUnitOf(t, f.dst, "task", "B one", "")
		moveUnitOf(t, f.dst, "task", "B two", "")
	}
	f.req = MoveRequest{Units: []string{f.parent}, WithChildren: true, SourceID: "prj_a", SourceName: "A", TargetID: "prj_b", TargetName: "B"}
	return f
}

func (f *moveFixture) plan(t *testing.T) *pyjson.Object {
	t.Helper()
	plan, err := f.src.PlanMove(context.Background(), f.dst, f.req)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestMoveRenumbersRewritesAndAliases(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, true)
	plan := f.plan(t)
	if plan.Value("unit_count") != int64(3) || plan.Value("renumbered") != int64(2) || plan.Value("links") != int64(2) {
		t.Fatalf("plan: %s", pyjson.Dumps(plan))
	}
	if cross := plan.Value("cross_links").([]any); len(cross) != 2 {
		t.Fatalf("cross links: %s", pyjson.Dumps(cross))
	}
	if plan.Value("media") != int64(2) || plan.Value("media_borrowed") != int64(1) || plan.Value("files") != int64(2) {
		t.Fatalf("media plan: %s", pyjson.Dumps(plan))
	}
	out, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin")
	if err != nil {
		t.Fatal(err)
	}
	moveID := pyjson.Str(out.Value("move_id"))
	// T-0001, T-0002 are taken in B, so the batch is renumbered: Parent,
	// Child become T-0003, T-0004; N-0001 comes out the same.
	uids := map[string]string{}
	for _, u := range out.Value("units").([]any) {
		o := u.(*pyjson.Object)
		uids[pyjson.Str(o.Value("uid"))] = pyjson.Str(o.Value("new_uid"))
	}
	if uids[f.parent] != "T-0003" || uids[f.child] != "T-0004" || uids[f.note] != "N-0001" {
		t.Fatalf("uids: %v", uids)
	}
	note := moveRow(t, f.dst, uids[f.note])
	body := note.Str("body")
	if !strings.Contains(body, "child [[T-0004]]") || !strings.Contains(body, "stays [[prj_a/"+f.stay+"]]") {
		t.Fatalf("refs not rewritten: %q", body)
	}
	// Code blocks are left alone.
	if !strings.Contains(body, "```\n[["+f.child+"]] media:"+pyjson.Str(f.ownMedia)+"\n```") {
		t.Fatalf("fenced code rewritten: %q", body)
	}
	// Media: own and borrowed rows copied, references point at the new ids,
	// files exist in B.
	rows := []sqlitedb.Row{}
	f.dst.read(ctx, func(q sqlitedb.Querier) error {
		var err error
		rows, err = sqlitedb.Query(ctx, q, "SELECT m.id, m.url, u.uid FROM unit_media m JOIN units u ON u.id=m.unit_id ORDER BY m.id")
		return err
	})
	if len(rows) != 2 {
		t.Fatalf("media rows: %v", rows)
	}
	for _, r := range rows {
		if _, err := os.Stat(filepath.Join(f.dst.Path, filepath.FromSlash(r.Str("url")))); err != nil {
			t.Fatalf("file missing in target: %v", err)
		}
		if !strings.Contains(body, "media:"+pyjson.Str(r.Get("id"))) && !strings.Contains(body, "/api/media/"+pyjson.Str(r.Get("id"))+"/raw") {
			t.Fatalf("body does not reference media %v: %q", r.Get("id"), body)
		}
	}
	if rows[1].Str("uid") != uids[f.note] {
		t.Fatalf("borrowed media should hang on the referencing unit: %v", rows[1])
	}
	// Links: two internal part_of; cross links recorded on both ends.
	if got := trashEdges(t, f.dst); got != "N-0001 part_of T-0003, T-0004 part_of T-0003" {
		t.Fatalf("target links: %s", got)
	}
	parentMeta := parseDetail(moveRow(t, f.dst, "T-0003").Get("meta_json"))
	ext, _ := parentMeta.Value("external_links").([]any)
	if len(ext) != 1 || pyjson.Str(ext[0].(*pyjson.Object).Value("uid")) != f.stay {
		t.Fatalf("external links on moved parent: %s", pyjson.Dumps(parentMeta))
	}
	stayMeta := parseDetail(moveRow(t, f.src, f.stay).Get("meta_json"))
	if ext, _ := stayMeta.Value("external_links").([]any); len(ext) != 2 {
		t.Fatalf("external links on staying unit: %s", pyjson.Dumps(stayMeta))
	}
	// Comments: copied with the reply chain and rewritten references.
	cm, err := f.dst.Comments(ctx, "T-0003")
	if err != nil || len(cm) != 2 {
		t.Fatalf("comments: %v %v", cm, err)
	}
	first := cm[0].(*pyjson.Object)
	if !strings.Contains(pyjson.Str(first.Value("body")), "[[T-0004]]") || cm[1].(*pyjson.Object).Value("reply_to") != first.Value("id") {
		t.Fatalf("comments not remapped: %s", pyjson.Dumps(cm))
	}
	// Source: tombstones in the bin, aliases both sides, writes refused.
	if r := moveRow(t, f.src, f.parent); r.Int("archived") != 1 || movedTo(r) == nil {
		t.Fatalf("source parent not a tombstone: %v", r)
	}
	for _, st := range []*Store{f.src, f.dst} {
		p, u, err := st.Alias(ctx, "prj_a", f.child)
		if err != nil || p != "prj_b" || u != "T-0004" {
			t.Fatalf("alias: %s %s %v", p, u, err)
		}
	}
	_, err = f.src.UpdateUnit(ctx, f.parent, pyjson.NewObject("title", "x"), "agent:bot", Who{})
	var moved *MovedError
	if !errors.As(err, &moved) || moved.NewUID != "T-0003" {
		t.Fatalf("write to tombstone: %v", err)
	}
	if _, err := f.src.AddComment(ctx, f.parent, "hi", "agent_reply", nil, "", "agent:bot"); !errors.As(err, &moved) {
		t.Fatalf("comment on tombstone: %v", err)
	}
	if _, err := f.src.RestoreUnit(ctx, f.parent, "human:ui"); !errors.As(err, &moved) {
		t.Fatalf("plain restore of tombstone: %v", err)
	}
	if got := trashEdges(t, f.src); got != "" {
		t.Fatalf("source keeps links of moved units: %s", got)
	}
	// Counters: the next task in B follows the moved ones.
	if next := moveUnitOf(t, f.dst, "task", "next", ""); next != "T-0005" {
		t.Fatalf("next uid in target: %s", next)
	}
	if j, err := LoadMoveJournal(f.src.Path, moveID); err != nil || j.State != "done" {
		t.Fatalf("journal: %+v %v", j, err)
	}
}

func TestMoveKeepsUIDsInEmptyTarget(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, false)
	f.req.NewProject = true
	plan := f.plan(t)
	if plan.Value("renumbered") != int64(0) {
		t.Fatalf("plan: %s", pyjson.Dumps(plan))
	}
	if _, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin"); err != nil {
		t.Fatal(err)
	}
	body := moveRow(t, f.dst, f.note).Str("body")
	if !strings.Contains(body, "child [["+f.child+"]]") {
		t.Fatalf("kept uid rewritten: %q", body)
	}
	// The source's counters came along: a new task does not reuse T-0003.
	if next := moveUnitOf(t, f.dst, "task", "next", ""); next != "T-0004" {
		t.Fatalf("next uid: %s", next)
	}
}

func TestMoveUndo(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, true)
	before := trashEdges(t, f.src)
	plan := f.plan(t)
	out, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.src.UndoMove(ctx, f.dst, pyjson.Str(out.Value("move_id")), false, "human:admin"); err != nil {
		t.Fatal(err)
	}
	if n := moveCount(t, f.dst, "SELECT COUNT(*) n FROM units"); n != 2 {
		t.Fatalf("target units after undo: %d", n)
	}
	if n := moveCount(t, f.dst, "SELECT COUNT(*) n FROM unit_media"); n != 0 {
		t.Fatalf("target media after undo: %d", n)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.dst.Path, "assets", "media")); len(entries) != 0 {
		t.Fatalf("target files after undo: %v", entries)
	}
	if got := trashEdges(t, f.src); got != before {
		t.Fatalf("source links after undo: %s, want %s", got, before)
	}
	if r := moveRow(t, f.src, f.parent); r.Int("archived") != 0 || movedTo(r) != nil {
		t.Fatalf("source parent after undo: %v", r)
	}
	if ext := parseDetail(moveRow(t, f.src, f.stay).Get("meta_json")).Value("external_links"); ext != nil {
		t.Fatalf("external links after undo: %v", ext)
	}
	if p, _, _ := f.src.Alias(ctx, "prj_a", f.parent); p != "" {
		t.Fatal("alias kept after undo")
	}
}

func TestMoveRollsBackOnFailure(t *testing.T) {
	for _, step := range []string{"files", "source", "target"} {
		t.Run(step, func(t *testing.T) {
			ctx := context.Background()
			f := newMoveFixture(t, true)
			before := trashEdges(t, f.src)
			plan := f.plan(t)
			moveFailHook = func(s string) error {
				if s == step {
					return errors.New("injected failure at " + s)
				}
				return nil
			}
			defer func() { moveFailHook = nil }()
			if _, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin"); err == nil {
				t.Fatal("apply should fail")
			}
			if n := moveCount(t, f.dst, "SELECT COUNT(*) n FROM units"); n != 2 {
				t.Fatalf("target units: %d", n)
			}
			if entries, _ := os.ReadDir(filepath.Join(f.dst.Path, "assets", "media")); len(entries) != 0 {
				t.Fatalf("target files left: %v", entries)
			}
			if n := moveCount(t, f.src, "SELECT COUNT(*) n FROM units WHERE archived=1"); n != 0 {
				t.Fatalf("source tombstones left: %d", n)
			}
			if got := trashEdges(t, f.src); got != before {
				t.Fatalf("source links: %s, want %s", got, before)
			}
		})
	}
}

func TestMoveRefusesStalePlan(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, true)
	plan := f.plan(t)
	if _, err := f.src.UpdateUnit(ctx, f.child, pyjson.NewObject("title", "changed"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	_, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin")
	var stale *MoveStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("stale plan: %v", err)
	}
}

func TestMoveMergeCarriesEverything(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, true)
	// Text history on the parent: two revisions.
	if _, err := f.src.SetNodeHistory(ctx, f.parent, true, "human:ui"); err != nil {
		t.Skipf("node history unavailable: %v", err)
	}
	if _, err := f.src.UpdateUnit(ctx, f.parent, pyjson.NewObject("body", "root v2"), "agent:bot", Who{}); err != nil {
		t.Fatal(err)
	}
	f.req = MoveRequest{All: true, SourceID: "prj_a", SourceName: "A", TargetID: "prj_b", TargetName: "B"}
	plan := f.plan(t)
	if plan.Value("unit_count") != int64(4) || len(plan.Value("cross_links").([]any)) != 0 || plan.Value("mode") != "merge" {
		t.Fatalf("merge plan: %s", pyjson.Dumps(plan))
	}
	if _, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin"); err != nil {
		t.Fatal(err)
	}
	// T-0001, T-0002 clash in B, so the whole batch is renumbered (D-0015):
	// Parent T-0003, Child T-0004, Stays T-0005, the note N-0001.  Every
	// link comes along.
	if got := trashEdges(t, f.dst); got != "N-0001 part_of T-0003, T-0003 relates_to T-0005, T-0004 part_of T-0003, T-0005 supports T-0004" {
		t.Fatalf("merged links: %s", got)
	}
	if n := moveCount(t, f.src, "SELECT COUNT(*) n FROM units WHERE archived=0"); n != 0 {
		t.Fatalf("source keeps %d live units", n)
	}
	// The parent's history came along and its head resolves in B.
	hist, err := f.dst.NodeHistory(ctx, "T-0003", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := hist.Value("versions").([]any); len(v) < 2 {
		t.Fatalf("history after merge: %s", pyjson.Dumps(hist))
	}
	// The source is read-only once marked merged.
	if err := f.src.SetMerged(pyjson.NewObject("project", "prj_b", "project_name", "B")); err != nil {
		t.Fatal(err)
	}
	var merged *MergedError
	if _, err := f.src.CreateUnit(ctx, pyjson.NewObject("type", "note", "title", "late"), "human:ui", Who{}); !errors.As(err, &merged) {
		t.Fatalf("create in merged project: %v", err)
	}
}

func TestMoveDropsCrossLinks(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, false)
	f.req.CrossLinks = "drop"
	plan := f.plan(t)
	if _, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin"); err != nil {
		t.Fatal(err)
	}
	if ext := parseDetail(moveRow(t, f.src, f.stay).Get("meta_json")).Value("external_links"); ext != nil {
		t.Fatalf("dropped links recorded in source: %v", ext)
	}
	if ext := parseDetail(moveRow(t, f.dst, f.parent).Get("meta_json")).Value("external_links"); ext != nil {
		t.Fatalf("dropped links recorded in target: %v", ext)
	}
}

func TestSplitClusters(t *testing.T) {
	ctx := context.Background()
	st := moveStore(t, "prj_c")
	// A hub with two branches (each a task with two results) linked once,
	// and a third branch tied to the first by three links.
	hub := moveUnitOf(t, st, "task", "Main", "")
	var roots []string
	for _, name := range []string{"A", "B", "C"} {
		root := moveUnitOf(t, st, "task", name, "")
		trashLink(t, st, root, hub, "derived_from")
		for i := 0; i < 2; i++ {
			r := moveUnitOf(t, st, "artifact", name+" result", "")
			trashLink(t, st, r, root, "part_of")
		}
		roots = append(roots, root)
	}
	trashLink(t, st, roots[0], roots[1], "relates_to")
	for i := 0; i < 3; i++ {
		n := moveUnitOf(t, st, "note", "C note", "")
		trashLink(t, st, n, roots[2], "part_of")
		trashLink(t, st, n, roots[0], "relates_to")
	}
	out, err := st.SplitClusters(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hubs := out.Value("hubs").([]any)
	if len(hubs) != 1 || pyjson.Str(hubs[0].(*pyjson.Object).Value("uid")) != hub {
		t.Fatalf("hubs: %s", pyjson.Dumps(out))
	}
	// Each branch is denser inside than towards the others: three groups.
	clusters := out.Value("clusters").([]any)
	cut := map[string]any{}
	for _, c := range clusters {
		o := c.(*pyjson.Object)
		cut[pyjson.Str(o.Value("roots").([]any)[0].(*pyjson.Object).Value("uid"))] = o.Value("cut_links")
	}
	if len(clusters) != 3 || cut[roots[0]] != int64(4) || cut[roots[1]] != int64(1) || cut[roots[2]] != int64(3) {
		t.Fatalf("clusters: %s", pyjson.Dumps(clusters))
	}
	// Splitting the weak branch moves its subtree (derived_from and part_of children).
	plan, err := st.PlanMove(ctx, nil, MoveRequest{Units: []string{roots[1]}, WithChildren: true, NewProject: true, SourceID: "prj_c"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Value("unit_count") != int64(3) || len(plan.Value("cross_links").([]any)) != 2 {
		t.Fatalf("split plan: %s", pyjson.Dumps(plan))
	}
}
