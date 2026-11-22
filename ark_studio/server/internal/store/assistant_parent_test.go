package store

import (
	"context"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// T-0091: tasks, results and decisions without a part_of parent are listed
// in Graph checks; the unit they derive from is offered as the parent.
func TestInsightsUnitsWithoutParent(t *testing.T) {
	st := chatStore(t)
	ctx := context.Background()
	unit := func(typ, title, status string) string {
		u, err := st.CreateUnit(ctx, pyjson.NewObject("type", typ, "title", title, "status", status), "human:ui", Who{})
		if err != nil {
			t.Fatal(err)
		}
		return pyjson.Str(u.Value("uid"))
	}
	line := unit("task", "Line: robustness", "active")
	child := unit("task", "Sweep seeds", "active") // part_of the line: fine
	impLink(t, st, child, line, "part_of")
	orphan := unit("task", "Ablate the head", "active") // only derived_from the line
	impLink(t, st, orphan, line, "derived_from")
	unit("task", "Read the paper", "open") // no links at all
	result := unit("artifact", "Audit notes", "review")
	impLink(t, st, result, child, "derived_from")
	done := unit("experiment", "Seed table", "done")
	impLink(t, st, done, child, "part_of") // C4: the result part_of its task
	decision := unit("decision", "Use 5 seeds", "done")
	gone := unit("task", "Dropped idea", "abandoned")
	_ = gone

	items, err := st.Insights(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]*pyjson.Object{}
	for _, it := range items {
		o := it.(*pyjson.Object)
		byTitle[pyjson.Str(o.Value("title"))] = o
	}
	find := func(prefix string) *pyjson.Object {
		for title, o := range byTitle {
			if strings.HasPrefix(title, prefix) {
				return o
			}
		}
		return nil
	}
	o := find("Task has no parent: Ablate the head")
	if o == nil || o.Value("kind") != "task-no-parent" {
		t.Fatalf("orphan task not listed: %v", byTitle)
	}
	if !strings.Contains(pyjson.Str(o.Value("title")), "attach to “Line: robustness”") {
		t.Fatalf("no parent offered: %v", o.Value("title"))
	}
	ops, _ := o.Value("ops").([]any)
	if len(ops) != 1 || ops[0].(*pyjson.Object).Value("relation") != "part_of" {
		t.Fatalf("ops %v", ops)
	}
	if a := find("Task has no parent: Read the paper"); a == nil || len(a.Value("ops").([]any)) != 0 {
		t.Fatalf("a task without any link: listed without an op: %v", a)
	}
	if r := find("No parent task: Audit notes"); r == nil || !strings.Contains(pyjson.Str(r.Value("title")), "Sweep seeds") {
		t.Fatalf("result: %v", r)
	}
	if find("No parent task: Use 5 seeds") == nil {
		t.Fatal("decision without a parent not listed")
	}
	for _, ok := range []string{"Task has no parent: Line", "Task has no parent: Sweep", "No parent task: Seed table",
		"Task has no parent: Dropped", "Experiment has no task: Seed table"} {
		if o := find(ok); o != nil {
			t.Fatalf("listed but has a parent or is a line: %v", o.Value("title"))
		}
	}
	_ = decision

	// Apply attaches the task; the check is gone afterwards.
	src := ops[0].(*pyjson.Object)
	if _, err := st.CreateLink(ctx, src.Value("source"), src.Value("target"), "part_of", "", "human:ui"); err != nil {
		t.Fatal(err)
	}
	items, _ = st.Insights(ctx)
	for _, it := range items {
		if strings.HasPrefix(pyjson.Str(it.(*pyjson.Object).Value("title")), "Task has no parent: Ablate") {
			t.Fatal("still listed after attaching")
		}
	}
}
