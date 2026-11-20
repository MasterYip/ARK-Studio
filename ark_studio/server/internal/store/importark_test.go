package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/sqlitedb"
)

func impStore(t *testing.T, id string) *Store {
	t.Helper()
	st, err := Open(t.TempDir(), NewManifest(pyjson.NewObject("id", id, "name", id)), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func impUnit(t *testing.T, st *Store, typ, title, body string) string {
	t.Helper()
	u, err := st.CreateUnit(context.Background(), pyjson.NewObject("type", typ, "title", title, "body", body), "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	return pyjson.Str(u.Value("uid"))
}

func impLink(t *testing.T, st *Store, src, dst, rel string) {
	t.Helper()
	if _, err := st.CreateLink(context.Background(), src, dst, rel, "", "human:ui"); err != nil {
		t.Fatal(err)
	}
}

func impMedia(t *testing.T, st *Store, uid, name string) int64 {
	t.Helper()
	rel := "assets/media/" + name
	path := filepath.Join(st.Path, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(path), 0o755) //nolint:errcheck
	if err := os.WriteFile(path, []byte("bytes of "+name), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := st.AddMedia(context.Background(), uid, rel, "image", name, "human:ui")
	if err != nil {
		t.Fatal(err)
	}
	return m.Value("id").(int64)
}

func impRow(t *testing.T, st *Store, uid string) sqlitedb.Row {
	t.Helper()
	var row sqlitedb.Row
	if err := st.read(context.Background(), func(q sqlitedb.Querier) error {
		var err error
		row, err = unitRow(context.Background(), q, uid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return row
}

func impEdges(t *testing.T, st *Store) string {
	t.Helper()
	var out []string
	st.read(context.Background(), func(q sqlitedb.Querier) error { //nolint:errcheck
		rows, err := sqlitedb.Query(context.Background(), q, `SELECT s.uid su, l.relation, t.uid tu FROM unit_links l
                   JOIN units s ON s.id=l.source_id JOIN units t ON t.id=l.target_id`)
		for _, r := range rows {
			out = append(out, r.Str("su")+" "+r.Str("relation")+" "+r.Str("tu"))
		}
		return err
	})
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// source A: idea I-0001 with child I-0002 (part_of) that embeds a picture
// of I-0003 (not imported) and references it; target B.
type impFixture struct {
	src, dst            *Store
	parent, child, rest string
	media               int64
	req                 ImportRequest
	srcEdges            string
}

func newImpFixture(t *testing.T, targetUnits int) *impFixture {
	f := &impFixture{src: impStore(t, "prj_a"), dst: impStore(t, "prj_b")}
	f.parent = impUnit(t, f.src, "idea", "Parent idea", "root idea")
	f.rest = impUnit(t, f.src, "idea", "Other idea", "stays")
	f.media = impMedia(t, f.src, f.rest, "pic.png")
	f.child = impUnit(t, f.src, "idea", "Child idea", "see [[I-0001]] and [["+f.rest+"]]\n\n![pic](media:"+pyjson.Str(f.media)+")")
	impLink(t, f.src, f.child, f.parent, "part_of")
	impLink(t, f.src, f.rest, f.parent, "relates_to")
	if _, err := f.src.AddComment(context.Background(), f.child, "a comment", "comment", nil, "alice", "human:alice"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < targetUnits; i++ {
		impUnit(t, f.dst, "idea", "B idea", "")
	}
	f.req = ImportRequest{SourceID: "prj_a", SourceName: "Project A", Units: []string{f.parent}, WithChildren: true, Lang: "zh"}
	f.srcEdges = impEdges(t, f.src)
	return f
}

func TestImportKeepsUIDsAndRecordsProvenance(t *testing.T) {
	ctx := context.Background()
	f := newImpFixture(t, 0)
	plan, err := f.dst.ImportFrom(ctx, f.src, f.req, true, "human:bob")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Value("unit_count") != int64(2) || plan.Value("renumbered") != int64(0) || plan.Value("media") != int64(1) || plan.Value("links") != int64(1) {
		t.Fatalf("plan: %s", pyjson.Dumps(plan))
	}
	out, err := f.dst.ImportFrom(ctx, f.src, f.req, false, "human:bob")
	if err != nil {
		t.Fatal(err)
	}
	if out.Value("imported") != int64(2) || len(out.Value("created").([]any)) != 2 {
		t.Fatalf("import: %s", pyjson.Dumps(out))
	}
	child := impRow(t, f.dst, f.child)
	body := child.Str("body")
	if !strings.Contains(body, "see [[I-0001]] and [[prj_a/"+f.rest+"]]") || !strings.HasSuffix(body, "导入自 Project A [[prj_a/"+f.child+"]]") {
		t.Fatalf("body: %q", body)
	}
	if child.Str("origin") != "ark-import" || child.Str("source_ref") != "ark:prj_a/"+f.child {
		t.Fatalf("provenance columns: %v", child)
	}
	from, _ := parseDetail(child.Get("meta_json")).Value("imported_from").(*pyjson.Object)
	if from == nil || from.Value("uid") != f.child {
		t.Fatalf("imported_from: %v", child.Get("meta_json"))
	}
	// The embedded picture was copied, re-pointed and is a file here.
	var url string
	var id int64
	f.dst.read(ctx, func(q sqlitedb.Querier) error { //nolint:errcheck
		r, _, err := sqlitedb.QueryOne(ctx, q, "SELECT id, url FROM unit_media")
		url, id = r.Str("url"), r.Int("id")
		return err
	})
	if !strings.Contains(body, "media:"+pyjson.Str(id)) {
		t.Fatalf("media ref not rewritten: %q", body)
	}
	if data, err := os.ReadFile(filepath.Join(f.dst.Path, filepath.FromSlash(url))); err != nil || string(data) != "bytes of pic.png" {
		t.Fatalf("media file: %v %q", err, data)
	}
	if got := impEdges(t, f.dst); got != f.child+" part_of "+f.parent {
		t.Fatalf("links: %s", got)
	}
	// No comments come along; the source is unchanged.
	if c, _ := f.dst.Comments(ctx, f.child); len(c) != 0 {
		t.Fatalf("comments copied: %v", c)
	}
	if got := impEdges(t, f.src); got != f.srcEdges {
		t.Fatalf("source links changed: %s", got)
	}
	if r := impRow(t, f.src, f.child); r.Int("archived") != 0 || strings.Contains(r.Str("body"), "导入自") {
		t.Fatalf("source unit changed: %v", r)
	}
	// Importing again skips what is already here.
	again, err := f.dst.ImportFrom(ctx, f.src, f.req, false, "human:bob")
	if err != nil || again.Value("imported") != int64(0) || again.Value("skipped") != int64(2) {
		t.Fatalf("second import: %s %v", pyjson.Dumps(again), err)
	}
}

func TestImportRenumbersOnClash(t *testing.T) {
	ctx := context.Background()
	f := newImpFixture(t, 2) // B has I-0001, I-0002
	out, err := f.dst.ImportFrom(ctx, f.src, f.req, false, "human:bob")
	if err != nil {
		t.Fatal(err)
	}
	if out.Value("renumbered") != int64(2) {
		t.Fatalf("renumbered: %s", pyjson.Dumps(out))
	}
	// Parent I-0001 -> I-0003, Child I-0003 -> I-0004 (batch order by source id).
	child := impRow(t, f.dst, "I-0004")
	if child.Str("title") != "Child idea" || !strings.Contains(child.Str("body"), "see [[I-0003]] and [[prj_a/"+f.rest+"]]") {
		t.Fatalf("renumbered child: %v", child)
	}
	if got := impEdges(t, f.dst); got != "I-0004 part_of I-0003" {
		t.Fatalf("links: %s", got)
	}
	if next := impUnit(t, f.dst, "idea", "next", ""); next != "I-0005" {
		t.Fatalf("next uid: %s", next)
	}
}

func TestImportWithoutChildrenAndRollback(t *testing.T) {
	ctx := context.Background()
	f := newImpFixture(t, 0)
	f.req.WithChildren = false
	f.req.Units = []string{f.child}
	importFailHook = func() error { return errors.New("injected") }
	if _, err := f.dst.ImportFrom(ctx, f.src, f.req, false, "human:bob"); err == nil {
		t.Fatal("import should fail")
	}
	importFailHook = nil
	if entries, _ := os.ReadDir(filepath.Join(f.dst.Path, "assets", "media")); len(entries) != 0 {
		t.Fatalf("files left after failure: %v", entries)
	}
	out, err := f.dst.ImportFrom(ctx, f.src, f.req, false, "human:bob")
	if err != nil || out.Value("imported") != int64(1) {
		t.Fatalf("import: %v %v", out, err)
	}
	// The parent was not imported: the reference points into the source.
	if body := impRow(t, f.dst, f.child).Str("body"); !strings.Contains(body, "see [[prj_a/I-0001]]") {
		t.Fatalf("body: %q", body)
	}
}
