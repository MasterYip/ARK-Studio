package store

// Move and import plans with decision nodes, and what makes a plan stale
// (moveplandeps.go): the approval decision a plan opens must not take a uid
// the plan needs, edits elsewhere and new units in the target must not
// invalidate it, edits to what it covers must, and a refresh recovers it.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func staleOf(t *testing.T, err error) *MoveStaleError {
	t.Helper()
	var se *MoveStaleError
	if !errors.As(err, &se) {
		t.Fatalf("want a stale plan, got %v", err)
	}
	return se
}

func hasReason(se *MoveStaleError, code, uid string) bool {
	for _, r := range se.Reasons {
		if r.Code != code {
			continue
		}
		if uid == "" {
			return true
		}
		for _, u := range r.Units {
			if u == uid {
				return true
			}
		}
	}
	return false
}

// decisionFixture: source A holds a decision tree (D-0001 <- D-0002, part_of)
// and an unrelated task; target B already holds decisions D-0001..D-0003, so
// an import or merge renumbers into the D range, where the plan's own
// approval decision is made too.
type decisionFixture struct {
	src, dst                 *Store
	root, child, other, task string
}

func newDecisionFixture(t *testing.T) *decisionFixture {
	f := &decisionFixture{src: moveStore(t, "prj_a"), dst: moveStore(t, "prj_b")}
	f.root = moveUnitOf(t, f.src, "decision", "Root decision", "decide")
	f.child = moveUnitOf(t, f.src, "decision", "Child decision", "see [["+f.root+"]]")
	f.other = moveUnitOf(t, f.src, "decision", "Unrelated decision", "stays")
	f.task = moveUnitOf(t, f.src, "task", "Unrelated task", "stays")
	trashLink(t, f.src, f.child, f.root, "part_of")
	for i := 0; i < 3; i++ {
		moveUnitOf(t, f.dst, "decision", fmt.Sprintf("B decision %d", i), "")
	}
	return f
}

func (f *decisionFixture) importPlan(t *testing.T) *MovePlan {
	t.Helper()
	p := &MovePlan{CreatedBy: "agent:bot", Lang: "en", Request: MoveRequest{SourceID: "prj_a", SourceName: "A",
		TargetID: "prj_b", TargetName: "B", Units: []string{f.root}, WithChildren: true}}
	if err := f.dst.ProposeImport(context.Background(), f.src, p, true); err != nil {
		t.Fatal(err)
	}
	if p.Decision == "" || p.Deps == nil {
		t.Fatalf("plan: %s", pyjson.Dumps(p.Public()))
	}
	loaded, err := LoadMovePlan(f.dst.Path, p.ID)
	if err != nil || loaded.Deps == nil || loaded.Hash != p.Hash {
		t.Fatalf("loaded: %+v %v", loaded, err)
	}
	return loaded
}

func approve(t *testing.T, st *Store, p *MovePlan) {
	t.Helper()
	if err := st.DecidePlan(context.Background(), p, true, "human:ana", ""); err != nil {
		t.Fatal(err)
	}
}

// The regression: D-0001's copy was numbered D-0004 by the preview, then the
// approval decision took D-0004 and every apply was refused as stale.
func TestImportPlanWithDecisionNodesApplies(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture(t)
	p := f.importPlan(t)
	if p.Decision != "D-0004" {
		t.Fatalf("approval decision: %s", p.Decision)
	}
	for _, x := range p.Summary.Value("units").([]any) {
		if pyjson.Str(x.(*pyjson.Object).Value("new_uid")) == p.Decision {
			t.Fatalf("preview numbers a copy as the approval decision: %s", pyjson.Dumps(p.Summary))
		}
	}
	if err := f.dst.CheckPlan(ctx, f.src, p); err != nil {
		t.Fatalf("fresh plan is stale: %v", err)
	}
	approve(t, f.dst, p)
	out, err := f.dst.ApplyImport(ctx, f.src, p, "human:ana")
	if err != nil || out.Value("imported") != int64(2) {
		t.Fatalf("apply: %v %v", out, err)
	}
	if strings.Join(p.Created, ",") != "D-0005,D-0006" {
		t.Fatalf("copies: %v", p.Created)
	}
	if moveRow(t, f.dst, p.Decision).Str("title") == "Root decision" {
		t.Fatal("the approval decision was overwritten")
	}
}

func TestSplitPlanWithDecisionNodesApplies(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture(t)
	p := &MovePlan{CreatedBy: "agent:bot", Lang: "en", NewName: "Decisions",
		Request: MoveRequest{SourceID: "prj_a", SourceName: "A", TargetName: "Decisions", Units: []string{f.root}}}
	if err := f.src.ProposeMove(ctx, nil, p, true); err != nil {
		t.Fatal(err)
	}
	if p.Decision == "" || p.Summary.Value("unit_count") != int64(2) {
		t.Fatalf("plan: %s", pyjson.Dumps(p.Public()))
	}
	// Unrelated work in the source while the plan waits.
	moveUnitOf(t, f.src, "decision", "Another question", "")
	if _, err := f.src.UpdateUnit(ctx, f.other, pyjson.NewObject("title", "edited elsewhere"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	approve(t, f.src, p)
	if err := f.src.CheckPlan(ctx, nil, p); err != nil {
		t.Fatalf("stale after unrelated edits: %v", err)
	}
	// As the route does: the new project is made at apply time.
	dst := moveStore(t, "prj_c")
	req := p.Request
	req.TargetID, req.PlanID = "prj_c", p.ID
	out, err := f.src.ApplyMove(ctx, dst, req, p.Hash, "human:ana")
	if err != nil || out.Value("unit_count") != int64(2) {
		t.Fatalf("apply: %v %v", out, err)
	}
	if moveRow(t, dst, f.root).Str("title") != "Root decision" || moveRow(t, dst, f.child).Str("title") != "Child decision" {
		t.Fatal("decisions not moved with their uids")
	}
	if moveRow(t, f.src, p.Decision).Int("archived") != 0 {
		t.Fatal("the approval decision moved")
	}
}

func TestMergePlanWithDecisionNodesApplies(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture(t)
	p := &MovePlan{CreatedBy: "agent:bot", Lang: "en",
		Request: MoveRequest{SourceID: "prj_a", SourceName: "A", TargetID: "prj_b", TargetName: "B", All: true}}
	if err := f.src.ProposeMove(ctx, f.dst, p, true); err != nil {
		t.Fatal(err)
	}
	if p.Summary.Value("unit_count") != int64(4) {
		t.Fatalf("plan: %s", pyjson.Dumps(p.Summary))
	}
	// The target keeps working while the plan waits: its new units take the
	// numbers the preview showed.
	moveUnitOf(t, f.dst, "decision", "B decision while waiting", "")
	moveUnitOf(t, f.dst, "task", "B task while waiting", "")
	approve(t, f.src, p)
	if err := f.src.CheckPlan(ctx, f.dst, p); err != nil {
		t.Fatalf("stale after target edits: %v", err)
	}
	req := p.Request
	req.PlanID = p.ID
	out, err := f.src.ApplyMove(ctx, f.dst, req, p.Hash, "human:ana")
	if err != nil || out.Value("unit_count") != int64(4) {
		t.Fatalf("apply: %v %v", out, err)
	}
	if got := moveCount(t, f.dst, "SELECT COUNT(*) n FROM units WHERE archived=0"); got != 3+2+4 {
		t.Fatalf("units in B: %d", got)
	}
	if moveCount(t, f.dst, "SELECT COUNT(*) n FROM units WHERE title='Root decision'") != 1 {
		t.Fatal("merge did not carry the decisions")
	}
}

func TestImportPlanIgnoresUnrelatedEdits(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture(t)
	p := f.importPlan(t)
	// Elsewhere in the source, and new and edited units in the target.
	if _, err := f.src.UpdateUnit(ctx, f.task, pyjson.NewObject("title", "edited", "status", "done"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	trashLink(t, f.src, f.task, f.other, "relates_to")
	moveUnitOf(t, f.src, "decision", "new in A", "")
	for i := 0; i < 3; i++ {
		moveUnitOf(t, f.dst, "decision", "new in B", "")
	}
	if _, err := f.dst.UpdateUnit(ctx, "D-0001", pyjson.NewObject("title", "edited in B"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	if err := f.dst.CheckPlan(ctx, f.src, p); err != nil {
		t.Fatalf("stale after unrelated edits: %v", err)
	}
	approve(t, f.dst, p)
	if _, err := f.dst.ApplyImport(ctx, f.src, p, "human:ana"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(p.Created) != 2 {
		t.Fatalf("copies: %v", p.Created)
	}
}

func TestPlanStaleOnPlannedEditAndRefresh(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture(t)
	p := f.importPlan(t)
	approve(t, f.dst, p)
	if _, err := f.src.UpdateUnit(ctx, f.child, pyjson.NewObject("body", "changed"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	se := staleOf(t, f.dst.CheckPlan(ctx, f.src, p))
	if !hasReason(se, "edited", f.child) || hasReason(se, "edited", f.root) {
		t.Fatalf("reasons: %v", se.Reasons)
	}
	_, err := f.dst.ApplyImport(ctx, f.src, p, "human:ana")
	if se := staleOf(t, err); strings.Join(se.Units(), ",") != f.child {
		t.Fatalf("units: %v", se.Units())
	}
	if moveCount(t, f.dst, "SELECT COUNT(*) n FROM units WHERE origin='ark-import'") != 0 {
		t.Fatal("a stale plan copied")
	}
	// A new child joins the plan.
	extra := moveUnitOf(t, f.src, "decision", "New child", "")
	trashLink(t, f.src, extra, f.root, "part_of")
	if se := staleOf(t, f.dst.CheckPlan(ctx, f.src, p)); !hasReason(se, "added", extra) {
		t.Fatalf("reasons: %v", se.Reasons)
	}
	// Refresh: planned again, waiting for approval, same decision (open).
	decision := p.Decision
	if err := f.dst.RefreshPlan(ctx, f.src, p, "agent:bot"); err != nil {
		t.Fatal(err)
	}
	if p.State != "proposed" || p.ApprovedBy != "" || p.Decision != decision || p.Summary.Value("unit_count") != int64(3) {
		t.Fatalf("refreshed: %s", pyjson.Dumps(p.Public()))
	}
	d := moveRow(t, f.dst, decision)
	if d.Str("status") != "open" || !strings.Contains(d.Str("body"), "needs approval again") {
		t.Fatalf("decision: %v", d)
	}
	if err := f.dst.CheckPlan(ctx, f.src, p); err != nil {
		t.Fatalf("refreshed plan is stale: %v", err)
	}
	approve(t, f.dst, p)
	if _, err := f.dst.ApplyImport(ctx, f.src, p, "human:ana"); err != nil || len(p.Created) != 3 {
		t.Fatalf("apply: %v %v", err, p.Created)
	}
	// Only a waiting or approved plan refreshes.
	p.State = "applied" // as the route records it
	if err := f.dst.RefreshPlan(ctx, f.src, p, "agent:bot"); err == nil {
		t.Fatal("refreshed an applied plan")
	}
}

func TestMovePlanStaleOnPlannedEditAndDeletion(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, true)
	p := &MovePlan{CreatedBy: "human:ana", Lang: "en", NewName: "Tree",
		Request: MoveRequest{SourceID: "prj_a", SourceName: "A", TargetName: "Tree", Units: []string{f.parent}}}
	if err := f.src.ProposeMove(ctx, nil, p, false); err != nil {
		t.Fatal(err)
	}
	// The staying unit is linked to the tree; its own edits do not count.
	if _, err := f.src.UpdateUnit(ctx, f.stay, pyjson.NewObject("title", "renamed"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	if err := f.src.CheckPlan(ctx, nil, p); err != nil {
		t.Fatalf("stale after editing a staying unit: %v", err)
	}
	if _, err := f.src.ArchiveUnit(ctx, f.note, "human:ui"); err != nil {
		t.Fatal(err)
	}
	if se := staleOf(t, f.src.CheckPlan(ctx, nil, p)); !hasReason(se, "gone", f.note) {
		t.Fatalf("reasons: %v", se.Reasons)
	}
	if err := f.src.RefreshPlan(ctx, nil, p, "human:ana"); err != nil {
		t.Fatal(err)
	}
	if err := f.src.CheckPlan(ctx, nil, p); err != nil || p.Summary.Value("unit_count") != int64(2) {
		t.Fatalf("refreshed: %v %s", err, pyjson.Dumps(p.Summary))
	}
}

// Units made in the target while the move runs (between the plan and the
// target transaction) do not stop it unless they take one of its uids.
func TestMoveTargetWritesDuringApply(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, typ string
		stale     bool
	}{{"unrelated type", "idea", false}, {"takes a planned uid", "task", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMoveFixture(t, true)
			plan := f.plan(t)
			moveFailHook = func(step string) error {
				if step == "files" {
					moveUnitOf(t, f.dst, tc.typ, "made during the move", "")
				}
				return nil
			}
			defer func() { moveFailHook = nil }()
			_, err := f.src.ApplyMove(ctx, f.dst, f.req, pyjson.Str(plan.Value("plan_hash")), "human:admin")
			if !tc.stale {
				if err != nil {
					t.Fatalf("apply: %v", err)
				}
				return
			}
			if se := staleOf(t, err); !hasReason(se, "ids_taken", "") {
				t.Fatalf("reasons: %v", se.Reasons)
			}
			if moveRow(t, f.src, f.parent).Int("archived") != 0 {
				t.Fatal("source not rolled back")
			}
		})
	}
}

// Units created in the target at the same time as an import is applied.
func TestImportConcurrentTargetCreation(t *testing.T) {
	ctx := context.Background()
	f := newDecisionFixture(t)
	p := f.importPlan(t)
	approve(t, f.dst, p)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := f.dst.CreateUnit(ctx, pyjson.NewObject("type", "decision", "title", "concurrent"), "human:ui", Who{}); err != nil {
					errs <- err
				}
			}
		}()
	}
	_, err := f.dst.ApplyImport(ctx, f.src, p, "human:ana")
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent create: %v", e)
	}
	if err != nil || len(p.Created) != 2 {
		t.Fatalf("apply: %v %v", err, p.Created)
	}
	if got := moveCount(t, f.dst, "SELECT COUNT(*) n FROM units"); got != 3+1+40+2 {
		t.Fatalf("units in B: %d", got)
	}
}
