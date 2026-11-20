package store

import (
	"context"
	"sort"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

func trashStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir(), NewManifest(pyjson.NewObject("id", "prj_trash", "name", "Trash")), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func trashUnit(t *testing.T, st *Store, title string) string {
	t.Helper()
	u, err := st.CreateUnit(context.Background(), pyjson.NewObject("type", "note", "title", title), "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	return pyjson.Str(u.Value("uid"))
}

func trashLink(t *testing.T, st *Store, src, dst, rel string) {
	t.Helper()
	if _, err := st.CreateLink(context.Background(), src, dst, rel, "n", "human:ui"); err != nil {
		t.Fatal(err)
	}
}

// trashEdges lists the live links as "SRC relation DST", sorted.
func trashEdges(t *testing.T, st *Store) string {
	t.Helper()
	var out []string
	err := st.read(context.Background(), func(q sqlitedb.Querier) error {
		rows, err := sqlitedb.Query(context.Background(), q, `SELECT s.uid su, l.relation, t.uid tu FROM unit_links l
                   JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id`)
		for _, r := range rows {
			out = append(out, r.Str("su")+" "+r.Str("relation")+" "+r.Str("tu"))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

func trashUIDs(t *testing.T, st *Store) []string {
	t.Helper()
	out, err := st.Trash(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var uids []string
	for _, item := range out.Value("units").([]any) {
		uids = append(uids, pyjson.Str(item.(*pyjson.Object).Value("uid")))
	}
	return uids
}

func TestTrashListsDeletedUnitsAndRestoreBringsLinksBack(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	a, b, c := trashUnit(t, st, "Parent"), trashUnit(t, st, "Child"), trashUnit(t, st, "Other")
	trashLink(t, st, b, a, "part_of")
	trashLink(t, st, c, b, "supports")
	if got := trashUIDs(t, st); len(got) != 0 {
		t.Fatalf("fresh bin: %v", got)
	}
	// The "archived" status is not the bin.
	if _, err := st.UpdateUnit(ctx, c, pyjson.NewObject("status", "archived"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArchiveUnit(ctx, b, "human:alice"); err != nil {
		t.Fatal(err)
	}
	// A second delete changes nothing (and keeps the recorded links).
	if _, err := st.ArchiveUnit(ctx, b, "human:bob"); err != nil {
		t.Fatal(err)
	}
	out, err := st.Trash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	items := out.Value("units").([]any)
	if len(items) != 1 || out.Value("total") != int64(1) {
		t.Fatalf("bin: %s", pyjson.Dumps(out))
	}
	item := items[0].(*pyjson.Object)
	if item.Value("uid") != b || item.Value("title") != "Child" || item.Value("type") != "note" ||
		item.Value("deleted_by") != "human:alice" || item.Value("links") != int64(2) || pyjson.Str(item.Value("deleted_at")) == "" {
		t.Fatalf("bin item: %s", pyjson.Dumps(item))
	}
	if got := trashEdges(t, st); got != "" {
		t.Fatalf("links after delete: %q", got)
	}
	if _, err := st.RestoreUnit(ctx, b, "human:ui"); err != nil {
		t.Fatal(err)
	}
	if got, want := trashEdges(t, st), b+" part_of "+a+", "+c+" supports "+b; got != want {
		t.Fatalf("links after restore: %q, want %q", got, want)
	}
	if got := trashUIDs(t, st); len(got) != 0 {
		t.Fatalf("bin after restore: %v", got)
	}
	// A later restore of a live unit does not replay old links.
	if _, err := st.ArchiveUnit(ctx, c, "human:ui"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RestoreUnit(ctx, c, "human:ui"); err != nil {
		t.Fatal(err)
	}
	if got, want := trashEdges(t, st), b+" part_of "+a+", "+c+" supports "+b; got != want {
		t.Fatalf("links after second round: %q", got)
	}
}

func TestRestoreSkipsLinksToBinnedUnitsAndSecondParents(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	a, b, c, d := trashUnit(t, st, "A"), trashUnit(t, st, "B"), trashUnit(t, st, "C"), trashUnit(t, st, "D")
	trashLink(t, st, b, a, "part_of")
	trashLink(t, st, d, a, "part_of")
	trashLink(t, st, c, a, "relates_to")
	// B goes to the bin first, then A: the B-A link was recorded with B.
	for _, uid := range []string{b, a} {
		if _, err := st.ArchiveUnit(ctx, uid, "human:ui"); err != nil {
			t.Fatal(err)
		}
	}
	if got := trashUIDs(t, st); len(got) != 2 {
		t.Fatalf("bin: %v", got)
	}
	// D gets a new parent while A is in the bin.
	trashLink(t, st, d, c, "part_of")
	if _, err := st.RestoreUnit(ctx, a, "human:ui"); err != nil {
		t.Fatal(err)
	}
	if got, want := trashEdges(t, st), c+" relates_to "+a+", "+d+" part_of "+c; got != want {
		t.Fatalf("after restoring A: %q, want %q", got, want)
	}
	if _, err := st.RestoreUnit(ctx, b, "human:ui"); err != nil {
		t.Fatal(err)
	}
	if got, want := trashEdges(t, st), b+" part_of "+a+", "+c+" relates_to "+a+", "+d+" part_of "+c; got != want {
		t.Fatalf("after restoring B: %q, want %q", got, want)
	}
}

func TestPurgeAndEmptyTrash(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	a, b, c := trashUnit(t, st, "Keep"), trashUnit(t, st, "Gone"), trashUnit(t, st, "Also gone")
	trashLink(t, st, b, a, "part_of")
	if _, err := st.AddMedia(ctx, b, "https://example.org/x.png", "image", "pic", "human:ui"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddComment(ctx, b, "hello", "comment", nil, "local", "human:ui"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PurgeUnit(ctx, a, "human:ui"); err == nil || !strings.Contains(err.Error(), "not in the recycle bin") {
		t.Fatalf("purge of a live unit: %v", err)
	}
	if _, err := st.ArchiveUnit(ctx, b, "human:ui"); err != nil {
		t.Fatal(err)
	}
	out, err := st.PurgeUnit(ctx, b, "human:ui")
	if err != nil || out.Value("deleted") != true || out.Value("uid") != b {
		t.Fatalf("purge: %v %v", out, err)
	}
	count := func(sql string) int64 {
		var n int64
		if err := st.read(ctx, func(q sqlitedb.Querier) error {
			r, _, err := sqlitedb.QueryOne(ctx, q, sql)
			n = r.Int("n")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("SELECT COUNT(*) n FROM units WHERE uid='" + b + "'"); n != 0 {
		t.Fatalf("unit row left: %d", n)
	}
	for _, table := range []string{"unit_media", "unit_comments", "unit_links"} {
		if n := count("SELECT COUNT(*) n FROM " + table); n != 0 {
			t.Fatalf("%s rows left: %d", table, n)
		}
	}
	if n := count("SELECT COUNT(*) n FROM activity WHERE action='unit.purge'"); n != 1 {
		t.Fatalf("purge activity rows: %d", n)
	}
	if _, err := st.PurgeUnit(ctx, b, "human:ui"); err == nil {
		t.Fatal("second purge succeeded")
	}
	for _, uid := range []string{a, c} {
		if _, err := st.ArchiveUnit(ctx, uid, "human:ui"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RestoreUnit(ctx, a, "human:ui"); err != nil {
		t.Fatal(err)
	}
	emptied, err := st.EmptyTrash(ctx, "human:ui")
	if err != nil || emptied.Value("deleted") != int64(1) {
		t.Fatalf("empty: %v %v", emptied, err)
	}
	if got := trashUIDs(t, st); len(got) != 0 {
		t.Fatalf("bin after empty: %v", got)
	}
	if n := count("SELECT COUNT(*) n FROM units"); n != 1 {
		t.Fatalf("units left: %d", n)
	}
	// A new unit never reuses a purged uid.
	if uid := trashUnit(t, st, "New"); uid == b || uid == c {
		t.Fatalf("uid reused: %s", uid)
	}
}

// Units deleted before the links were recorded (unit.archive rows with only
// links_deleted, or no detail at all) stay listable and restore without links.
func TestTrashOldArchiveRowsWithoutLinks(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	a, b, c := trashUnit(t, st, "A"), trashUnit(t, st, "B"), trashUnit(t, st, "C")
	trashLink(t, st, b, a, "part_of")
	trashLink(t, st, c, a, "relates_to")
	for _, uid := range []string{b, c} {
		if _, err := st.ArchiveUnit(ctx, uid, "human:ui"); err != nil {
			t.Fatal(err)
		}
	}
	err := st.write(ctx, func(w *wtx) error {
		if _, err := w.q.ExecContext(ctx, `UPDATE activity SET detail_json='{"uid":"x","links_deleted":1}'
                   WHERE action='unit.archive' AND unit_id=(SELECT id FROM units WHERE uid=?)`, b); err != nil {
			return err
		}
		_, err := w.q.ExecContext(ctx, `UPDATE activity SET detail_json='not json'
                   WHERE action='unit.archive' AND unit_id=(SELECT id FROM units WHERE uid=?)`, c)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := st.Trash(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range out.Value("units").([]any) {
		if n := item.(*pyjson.Object).Value("links"); n != int64(0) {
			t.Fatalf("old row counts links: %v", item)
		}
	}
	for _, uid := range []string{b, c} {
		if _, err := st.RestoreUnit(ctx, uid, "human:ui"); err != nil {
			t.Fatalf("restore %s: %v", uid, err)
		}
	}
	if got := trashEdges(t, st); got != "" {
		t.Fatalf("old rows restored links: %q", got)
	}
	if got := trashUIDs(t, st); len(got) != 0 {
		t.Fatalf("bin after restore: %v", got)
	}
}
