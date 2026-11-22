package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// A restart between the source and the target transaction leaves the
// source's units as tombstones; RecoverMove puts them back and removes
// what the move made in the target.
func TestMoveRecoversAfterRestart(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, true)
	before := trashEdges(t, f.src)
	plan := f.plan(t)
	moveFailHook = func(s string) error {
		if s == "crash" {
			return errors.New("process stopped")
		}
		return nil
	}
	_, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin")
	moveFailHook = nil
	if err == nil {
		t.Fatal("apply should stop")
	}
	journals := MoveJournals(f.src.Path)
	if len(journals) != 1 || !journals[0].Interrupted() {
		t.Fatalf("journal: %+v", journals)
	}
	if n := moveCount(t, f.src, "SELECT COUNT(*) n FROM units WHERE archived=1"); n == 0 {
		t.Fatal("the source should hold tombstones before recovery")
	}
	if err := f.src.RecoverMove(ctx, f.dst, journals[0]); err != nil {
		t.Fatal(err)
	}
	if got := trashEdges(t, f.src); got != before {
		t.Fatalf("source links after recovery: %s, want %s", got, before)
	}
	if n := moveCount(t, f.src, "SELECT COUNT(*) n FROM units WHERE archived=1"); n != 0 {
		t.Fatalf("tombstones left: %d", n)
	}
	if n := moveCount(t, f.dst, "SELECT COUNT(*) n FROM units"); n != 2 {
		t.Fatalf("target units after recovery: %d", n)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.dst.Path, "assets", "media")); len(entries) != 0 {
		t.Fatalf("target files after recovery: %v", entries)
	}
	j, _ := LoadMoveJournal(f.src.Path, journals[0].ID)
	if j.State != "failed" || !strings.Contains(j.Error, "restart") {
		t.Fatalf("journal after recovery: %+v", j)
	}
	// Recovering again does nothing.
	if err := f.src.RecoverMove(ctx, f.dst, j); err != nil {
		t.Fatal(err)
	}
}

// After the undo window the move cannot be undone and its backups go.
func TestMoveUndoWindowAndBackupCleanup(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, true)
	plan := f.plan(t)
	out, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin")
	if err != nil {
		t.Fatal(err)
	}
	id := pyjson.Str(out.Value("move_id"))
	backup := filepath.Join(moveDir(f.src.Path, id), "source.sqlite3")
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if n := CleanMoveBackups(f.src.Path, time.Now()); n != 0 {
		t.Fatalf("fresh backups cleaned: %d", n)
	}
	if n := CleanMoveBackups(f.src.Path, time.Now().Add(31*24*time.Hour)); n != 1 {
		t.Fatalf("old backups cleaned: %d", n)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moveDir(f.src.Path, id), "journal.json")); err != nil {
		t.Fatalf("journal removed: %v", err)
	}
	old := MoveUndoWindow
	MoveUndoWindow = -time.Second
	defer func() { MoveUndoWindow = old }()
	if _, err := f.src.UndoMove(ctx, f.dst, id, false, "human:admin"); err == nil || !strings.Contains(err.Error(), "can no longer be undone") {
		t.Fatalf("undo after the window: %v", err)
	}
}

// An import plan previews, is refused when what it would copy changed, and
// applies and undoes (the copies go to the recycle bin; the source stays).
func TestImportPlanApplyAndUndo(t *testing.T) {
	ctx := context.Background()
	f := newImpFixture(t, 0)
	p := &MovePlan{CreatedBy: "agent:bot", Lang: "en",
		Request: MoveRequest{SourceID: "prj_a", SourceName: "Project A", TargetID: "prj_b", TargetName: "B",
			Units: []string{f.parent}, WithChildren: true}}
	if err := f.dst.ProposeImport(ctx, f.src, p, true); err != nil {
		t.Fatal(err)
	}
	if p.Mode != "import" || p.Decision == "" || p.Summary.Value("unit_count") != int64(2) {
		t.Fatalf("plan: %s", pyjson.Dumps(p.Public()))
	}
	d := impRow(t, f.dst, p.Decision)
	if d.Str("type") != "decision" || d.Str("status") != "open" || !strings.Contains(d.Str("body"), "?move_plan="+p.ID) {
		t.Fatalf("decision: %v", d)
	}
	loaded, err := LoadMovePlan(f.dst.Path, p.ID)
	if err != nil || loaded.Mode != "import" || !loaded.Request.WithChildren || loaded.Request.SourceID != "prj_a" {
		t.Fatalf("loaded: %+v %v", loaded, err)
	}
	// A new unit here takes I-0001: the batch would be renumbered, so the
	// plan is stale.
	impUnit(t, f.dst, "idea", "here first", "")
	if _, err := f.dst.ApplyImport(ctx, f.src, loaded, "human:bob"); !errors.As(err, new(*MoveStaleError)) {
		t.Fatalf("stale import: %v", err)
	}
	p2 := &MovePlan{CreatedBy: "agent:bot", Request: loaded.Request}
	if err := f.dst.ProposeImport(ctx, f.src, p2, false); err != nil {
		t.Fatal(err)
	}
	out, err := f.dst.ApplyImport(ctx, f.src, p2, "human:bob")
	if err != nil || out.Value("imported") != int64(2) || len(p2.Created) != 2 {
		t.Fatalf("apply: %v %v %v", out, err, p2.Created)
	}
	u, err := f.dst.UndoImport(ctx, p2, false, "human:bob")
	if err != nil || u.Value("units") != int64(2) {
		t.Fatalf("undo: %v %v", u, err)
	}
	for _, uid := range p2.Created {
		if impRow(t, f.dst, uid).Int("archived") != 1 {
			t.Fatalf("%s not in the recycle bin", uid)
		}
	}
	if impRow(t, f.src, f.parent).Int("archived") != 0 {
		t.Fatal("source changed")
	}
}
