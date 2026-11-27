package store

// Lines of work (branches.go): branch:<name> tags, their stats, the stored
// topic that shows a line, the virtual topic page, the Needs you lines and
// the suggestions from the graph structure.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/sqlitedb"
)

func partOf(uid string) []any {
	return []any{pyjson.NewObject("target", uid, "relation", "part_of")}
}

func branchByName(t *testing.T, data *pyjson.Object, name string) *pyjson.Object {
	t.Helper()
	for _, it := range data.Value("items").([]any) {
		if o := it.(*pyjson.Object); o.Value("name") == name {
			return o
		}
	}
	return nil
}

func listBranches(t *testing.T, st *Store) *pyjson.Object {
	t.Helper()
	data, err := st.Branches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func pinClock(t *testing.T, at time.Time) {
	t.Helper()
	clock.SetReplay(&at)
	t.Cleanup(func() { clock.SetReplay(nil) })
}

// One line along a chain, a sub-line that forks from it, a unit on two
// lines (a merge) and a review without a line.
func TestBranchesChainForkMergeOrphan(t *testing.T) {
	st := reviewStore(t)
	root := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Pilot", "status", "active", "tags", []any{"branch:pilot"}), "user")
	step := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Step", "tags", []any{"branch:pilot"}, "links", partOf(root)), "user")
	result := mkUnit(t, st, pyjson.NewObject("type", "experiment", "title", "Result", "status", "review",
		"tags", []any{"branch:pilot"}, "links", partOf(step)), "user")
	// A sub-goal grows its own line (a split): a new tag below the old line.
	sub := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Robot use", "tags", []any{"branch:robot-use"}, "links", partOf(root)), "user")
	// One unit serves both lines (a merge).
	both := mkUnit(t, st, pyjson.NewObject("type", "decision", "title", "Shared gate", "tags", []any{"branch:pilot", "branch:robot-use"},
		"links", partOf(sub)), "user")
	orphan := mkUnit(t, st, pyjson.NewObject("type", "note", "title", "Loose", "status", "review"), "user")

	data := listBranches(t, st)
	if data.Value("prefix") != "branch" || len(data.Value("items").([]any)) != 2 {
		t.Fatalf("branches: %v", data)
	}
	pilot, robot := branchByName(t, data, "pilot"), branchByName(t, data, "robot-use")
	if pilot.Value("units") != int64(4) || pilot.Value("reviews") != int64(1) || pilot.Value("decisions") != int64(1) ||
		pilot.Value("pending") != int64(2) || pilot.Value("status") != "active" || pilot.Value("ref") != "branch:pilot" {
		t.Fatalf("pilot: %v", pilot)
	}
	if robot.Value("units") != int64(2) || robot.Value("pending") != int64(1) || robot.Value("topic") != nil {
		t.Fatalf("robot-use: %v", robot)
	}
	needs := data.Value("needs").(*pyjson.Object)
	if !reflect.DeepEqual(needs.Value(result), []any{"pilot"}) || !reflect.DeepEqual(needs.Value(both), []any{"pilot", "robot-use"}) ||
		needs.Has(orphan) || data.Value("untagged_pending") != int64(1) {
		t.Fatalf("needs: %v untagged %v", needs, data.Value("untagged_pending"))
	}
	// Needs you items carry their lines; one without a line carries none.
	if got := needsItem(t, st, both).Value("branches"); !reflect.DeepEqual(got, []any{"pilot", "robot-use"}) {
		t.Fatalf("needs item lines: %v", got)
	}
	if needsItem(t, st, orphan).Has("branches") {
		t.Fatal("an item without a line has branches")
	}
}

// Sessions are shown, never used to group: one session writing to two
// lines leaves two lines, each listing that session.
func TestBranchesSessionsDoNotMergeLines(t *testing.T) {
	st := reviewStore(t)
	ctx := WithActivitySession(context.Background(), func() (string, string) { return "s_one", "header" })
	for _, line := range []string{"a", "b"} {
		if _, err := st.CreateUnit(ctx, pyjson.NewObject("type", "task", "title", "Task "+line, "tags", []any{"branch:" + line}),
			"agent:claude", Who{}); err != nil {
			t.Fatal(err)
		}
	}
	data := listBranches(t, st)
	if len(data.Value("items").([]any)) != 2 {
		t.Fatalf("lines: %v", data.Value("items"))
	}
	for _, name := range []string{"a", "b"} {
		b := branchByName(t, data, name)
		if b.Value("units") != int64(1) || !reflect.DeepEqual(b.Value("sessions"), []any{"s_one"}) || b.Value("session_count") != int64(1) {
			t.Fatalf("line %s: %v", name, b)
		}
	}
}

func TestBranchStatus(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		open, pending int64
		last, want    string
	}{
		{0, 0, "2026-11-25T00:00:00+00:00", "done"},
		{2, 1, "2026-11-25T00:00:00+00:00", "active"},
		{2, 1, "2026-11-23T11:00:00+00:00", "stalled"},
		{2, 0, "2026-11-01T00:00:00+00:00", "quiet"},
		{2, 0, "2026-11-20T00:00:00+00:00", "active"},
		{1, 1, "", "stalled"},
	}
	for _, c := range cases {
		if got := BranchStatus(c.open, c.pending, c.last, now); got != c.want {
			t.Errorf("BranchStatus(%d, %d, %q) = %s, want %s", c.open, c.pending, c.last, got, c.want)
		}
	}
}

// A line whose review waits with no activity for three days is stalled; a
// line whose units are all closed is done.
func TestBranchesStalledAndDone(t *testing.T) {
	st := reviewStore(t)
	start := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	pinClock(t, start)
	mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Waiting", "status", "review", "tags", []any{"branch:slow"}), "user")
	mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Finished", "status", "done", "tags", []any{"branch:old"}), "user")
	if b := branchByName(t, listBranches(t, st), "slow"); b.Value("status") != "active" || b.Value("stalled") != false {
		t.Fatalf("fresh: %v", b)
	}
	pinClock(t, start.Add(4*24*time.Hour))
	data := listBranches(t, st)
	if b := branchByName(t, data, "slow"); b.Value("status") != "stalled" || b.Value("stalled") != true {
		t.Fatalf("after four days: %v", b)
	}
	if b := branchByName(t, data, "old"); b.Value("status") != "done" {
		t.Fatalf("closed line: %v", b)
	}
}

// A stored topic whose rule is exactly the line's tag shows the line; the
// virtual topic page lists the tagged units.
func TestBranchTopicMapping(t *testing.T) {
	st := reviewStore(t)
	a := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "A", "tags", []any{"branch:hoi-pilot"}), "user")
	b := mkUnit(t, st, pyjson.NewObject("type", "evidence", "title", "B", "tags", []any{"branch:hoi-pilot"}, "links", partOf(a)), "user")
	mkUnit(t, st, pyjson.NewObject("type", "task", "title", "C", "tags", []any{"branch:other"}), "user")

	page, err := st.BranchTopic(context.Background(), "hoi-pilot", false, false)
	if err != nil {
		t.Fatal(err)
	}
	topic := page.Value("topic").(*pyjson.Object)
	if topic.Value("virtual") != true || topic.Value("uid") != "branch:hoi-pilot" || topic.Value("title") != "hoi-pilot" {
		t.Fatalf("virtual topic: %v", topic)
	}
	var members []string
	for _, m := range page.Value("members").([]*pyjson.Object) {
		members = append(members, pyjson.Str(m.Value("uid")))
	}
	if !reflect.DeepEqual(members, []string{a, b}) || len(page.Value("edges").([]*pyjson.Object)) != 1 {
		t.Fatalf("members %v edges %v", members, page.Value("edges"))
	}
	if _, err := st.BranchTopic(context.Background(), "bad tag", false, false); err == nil {
		t.Fatal("a name with a space is a line")
	}

	// Pinning: a stored topic with the rule tag = branch:hoi-pilot.
	pinned := mkUnit(t, st, pyjson.NewObject("type", "topic", "title", "HOI pilot",
		"design", pyjson.NewObject("rule", pyjson.NewObject("tags", []any{"branch:hoi-pilot"}))), "user")
	// A topic whose rule adds another condition does not count.
	mkUnit(t, st, pyjson.NewObject("type", "topic", "title", "Other tasks",
		"design", pyjson.NewObject("rule", pyjson.NewObject("tags", []any{"branch:other"}, "types", []any{"task"}))), "user")
	data := listBranches(t, st)
	hoi := branchByName(t, data, "hoi-pilot")
	if tp, _ := hoi.Value("topic").(*pyjson.Object); tp == nil || tp.Value("uid") != pinned || hoi.Value("ref") != "branch:hoi-pilot" {
		t.Fatalf("mapped topic: %v", hoi)
	}
	if other := branchByName(t, data, "other"); other.Value("topic") != nil || other.Value("ref") != "branch:other" {
		t.Fatalf("other: %v", other)
	}
	// A topic carrying exactly one line tag shows that line when no rule does.
	carrier := mkUnit(t, st, pyjson.NewObject("type", "topic", "title", "Other line", "tags", []any{"branch:other"}), "user")
	if other := branchByName(t, listBranches(t, st), "other"); other.Value("ref") != "branch:other" ||
		other.Value("topic").(*pyjson.Object).Value("uid") != carrier ||
		other.Value("topic").(*pyjson.Object).Value("via") != "tag" {
		t.Fatalf("tag-carrying topic: %v", other)
	}
	stored, err := st.Topic(context.Background(), pinned, false)
	if err != nil || len(stored.Value("members").([]*pyjson.Object)) != 2 {
		t.Fatalf("pinned topic members: %v %v", err, stored)
	}
}

// Answers are kept per project revision: the second read is the cached
// answer (a copy the caller may change); any write, even a tag written
// straight into the table, changes the revision and the answer.
func TestBranchesCachedPerRevision(t *testing.T) {
	st := reviewStore(t)
	ctx := context.Background()
	uid := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "A", "tags", []any{"branch:x"}), "user")
	first := listBranches(t, st)
	key, err := st.branchCacheKey(ctx, "list")
	if err != nil || st.branches.get(key) == nil {
		t.Fatalf("not cached: %v", err)
	}
	first.Set("items", []any{}) // the caller's copy; the cache keeps its own
	if got := listBranches(t, st); len(got.Value("items").([]any)) != 1 {
		t.Fatalf("cached answer changed by the caller: %v", got.Value("items"))
	}
	if err := st.DB.Write(ctx, func(q sqlitedb.Querier) error {
		_, err := q.ExecContext(ctx, "INSERT INTO unit_tags(unit_id,tag) SELECT id,'branch:y' FROM units WHERE uid=?", uid)
		return err
	}, nil); err != nil {
		t.Fatal(err)
	}
	if got := listBranches(t, st); len(got.Value("items").([]any)) != 2 {
		t.Fatalf("after a write: %v", got.Value("items"))
	}
}

// The prefix comes from settings.branches.prefix.
func TestBranchPrefixSetting(t *testing.T) {
	st := reviewStore(t)
	st.Manifest = NewManifest(pyjson.NewObject("id", "prj_review", "settings",
		pyjson.NewObject("branches", pyjson.NewObject("prefix", "line"))))
	mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Git delivery", "tags", []any{"branch:feat/x"}), "user")
	mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Line", "tags", []any{"line:pilot"}), "user")
	data := listBranches(t, st)
	if data.Value("prefix") != "line" || len(data.Value("items").([]any)) != 1 || branchByName(t, data, "pilot") == nil {
		t.Fatalf("prefix line: %v", data)
	}
	if st.BranchTag("pilot") != "line:pilot" || st.BranchTag("line:pilot") != "line:pilot" {
		t.Fatal("BranchTag")
	}
	for _, p := range []string{"Branch", "needs", "type", "", "a b"} {
		if ValidBranchPrefix(p) {
			t.Errorf("prefix %q accepted", p)
		}
	}
}

// Suggestions follow part_of / derived_from only: units below a tagged unit
// inherit its lines; an untagged goal with enough units below it becomes a
// proposed new line; a unit below two goals is counted as shared.
func TestBranchSuggestions(t *testing.T) {
	st := reviewStore(t)
	line := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Tagged", "tags", []any{"branch:pilot"}), "user")
	child := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Child", "links", partOf(line)), "user")
	grandchild := mkUnit(t, st, pyjson.NewObject("type", "experiment", "title", "Grandchild", "links", partOf(child)), "user")
	goal := mkUnit(t, st, pyjson.NewObject("type", "question", "title", "Why is HOI unstable?"), "user")
	other := mkUnit(t, st, pyjson.NewObject("type", "question", "title", "Q2"), "user")
	var under []string
	for _, title := range []string{"T1", "T2", "T3"} {
		under = append(under, mkUnit(t, st, pyjson.NewObject("type", "task", "title", title, "links", partOf(goal)), "user"))
	}
	shared := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Shared", "links",
		[]any{pyjson.NewObject("target", goal, "relation", "derived_from"), pyjson.NewObject("target", other, "relation", "part_of")}), "user")
	// relates_to is not followed.
	mkUnit(t, st, pyjson.NewObject("type", "note", "title", "Aside", "links",
		[]any{pyjson.NewObject("target", line, "relation", "relates_to")}), "user")

	out, err := st.BranchSuggestions(context.Background(), BranchSuggestOptions{Relations: []string{"part_of", "derived_from"}})
	if err != nil {
		t.Fatal(err)
	}
	inherit := map[string]any{}
	for _, it := range out.Value("inherit").([]any) {
		o := it.(*pyjson.Object)
		inherit[pyjson.Str(o.Value("uid"))] = o.Value("tags")
	}
	if !reflect.DeepEqual(inherit, map[string]any{child: []any{"branch:pilot"}, grandchild: []any{"branch:pilot"}}) {
		t.Fatalf("inherit: %v", inherit)
	}
	lines := out.Value("new_lines").([]any)
	if len(lines) != 1 {
		t.Fatalf("new lines: %v", lines)
	}
	nl := lines[0].(*pyjson.Object)
	if nl.Value("name") != "why-is-hoi-unstable" || nl.Value("count") != int64(4) ||
		nl.Value("root").(*pyjson.Object).Value("uid") != goal {
		t.Fatalf("new line: %v", nl)
	}
	if out.Value("shared_units") != int64(1) {
		t.Fatalf("shared: %v (%s)", out.Value("shared_units"), shared)
	}
	// Depth 1 stops at the child: the grandchild's nearest tagged unit is two steps up.
	shallow, err := st.BranchSuggestions(context.Background(), BranchSuggestOptions{Depth: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range shallow.Value("inherit").([]any) {
		if it.(*pyjson.Object).Value("uid") == grandchild {
			t.Fatal("depth 1 reached the grandparent")
		}
	}
	_ = under
	// The default follows part_of only: the shared unit is under Q2 alone.
	plain, err := st.BranchSuggestions(context.Background(), BranchSuggestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Value("shared_units") != int64(0) || len(plain.Value("new_lines").([]any)) != 1 ||
		plain.Value("new_lines").([]any)[0].(*pyjson.Object).Value("count") != int64(3) {
		t.Fatalf("part_of only: %v", plain)
	}
}

// The climb ends at a topic: lines under one umbrella task stay apart.
func TestBranchSuggestionsStopAtTopics(t *testing.T) {
	st := reviewStore(t)
	umbrella := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "ARK 开发总览"), "user")
	var topics []string
	for _, title := range []string{"前端 / UI 线", "Go backend"} {
		tp := mkUnit(t, st, pyjson.NewObject("type", "topic", "title", title, "links", partOf(umbrella)), "user")
		topics = append(topics, tp)
		for i := 0; i < 3; i++ {
			mkUnit(t, st, pyjson.NewObject("type", "task", "title", title+" task", "links", partOf(tp)), "user")
		}
	}
	out, err := st.BranchSuggestions(context.Background(), BranchSuggestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int64{}
	for _, it := range out.Value("new_lines").([]any) {
		o := it.(*pyjson.Object)
		names[pyjson.Str(o.Value("name"))] = o.Value("count").(int64)
		if o.Value("root_is_topic") != true {
			t.Fatalf("root is not the topic: %v", o)
		}
	}
	if !reflect.DeepEqual(names, map[string]int64{"前端-ui-线": 3, "go-backend": 3}) {
		t.Fatalf("lines: %v", names)
	}
	// Climbing past topics folds both into the umbrella task.
	all, err := st.BranchSuggestions(context.Background(), BranchSuggestOptions{KeepClimbing: true})
	if err != nil {
		t.Fatal(err)
	}
	if lines := all.Value("new_lines").([]any); len(lines) != 1 || lines[0].(*pyjson.Object).Value("count") != int64(6) { // topics themselves get no tag
		t.Fatalf("keep climbing: %v", lines)
	}
}

// Line paths (主线/分线/分分线): validation, the line topic with and without
// the lines below it, rename with undo, a unit on lines of two subtrees.
func TestLinePaths(t *testing.T) {
	st := reviewStore(t)
	ctx := context.Background()
	for _, bad := range []string{"branch:hoi/", "branch:/hoi", "branch:hoi//pilot"} {
		if _, err := st.CreateUnit(ctx, pyjson.NewObject("type", "task", "title", "x", "tags", []any{bad}), "user", Who{}); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	for _, ok := range []string{"hoi", "hoi/pilot", "hoi/pilot/stairs", "主线/分线"} {
		if !ValidLinePath(ok) {
			t.Errorf("refused %s", ok)
		}
	}
	root := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "HOI", "tags", []any{"branch:hoi"}), "user")
	pilot := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Pilot", "tags", []any{"branch:hoi/pilot"}), "user")
	stairs := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Stairs", "tags", []any{"branch:hoi/pilot/stairs"}), "user")
	both := mkUnit(t, st, pyjson.NewObject("type", "decision", "title", "Shared", "tags", []any{"branch:hoi/pilot", "branch:robot/use"}), "user")
	other := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "Hoist", "tags", []any{"branch:hoist"}), "user")
	members := func(exact bool, name string) []string {
		page, err := st.BranchTopic(ctx, name, false, exact)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range page.Value("members").([]*pyjson.Object) {
			out = append(out, pyjson.Str(m.Value("uid")))
		}
		return out
	}
	if got := members(false, "hoi"); !reflect.DeepEqual(got, []string{root, pilot, stairs, both}) {
		t.Fatalf("hoi with the lines below: %v (hoist %s must not match)", got, other)
	}
	if got := members(true, "hoi"); !reflect.DeepEqual(got, []string{root}) {
		t.Fatalf("hoi only: %v", got)
	}
	if got := members(false, "robot"); !reflect.DeepEqual(got, []string{both}) {
		t.Fatalf("robot subtree: %v", got)
	}
	b := branchByName(t, listBranches(t, st), "hoi/pilot/stairs")
	if b.Value("parent") != "hoi/pilot" || b.Value("depth") != int64(2) {
		t.Fatalf("path fields: %v", b)
	}
	// A pinned topic with the subtree rule maps to the line.
	pinned := mkUnit(t, st, pyjson.NewObject("type", "topic", "title", "HOI",
		"design", pyjson.NewObject("rule", pyjson.NewObject("tag_trees", []any{"branch:hoi"}))), "user")
	if hb := branchByName(t, listBranches(t, st), "hoi"); hb.Value("ref") != "branch:hoi" || lineTopicUID(hb) != pinned {
		t.Fatalf("subtree topic: %v", hb)
	}
	// Rename hoi/pilot -> hoi/g1: preview, apply, undo.
	preview, err := st.RenameLine(ctx, "hoi/pilot", "hoi/g1", nil, true, "user", Who{})
	if err != nil || len(preview.Value("changes").([]any)) != 3 || preview.Value("units") != int64(3) {
		t.Fatalf("preview: %v %v", err, preview)
	}
	if b := branchByName(t, listBranches(t, st), "hoi/g1"); b != nil {
		t.Fatal("the preview wrote")
	}
	done, err := st.RenameLine(ctx, "hoi/pilot", "hoi/g1", nil, false, "user", Who{})
	if err != nil || done.Value("batch") == nil {
		t.Fatalf("rename: %v %v", err, done)
	}
	data := listBranches(t, st)
	if branchByName(t, data, "hoi/pilot") != nil || branchByName(t, data, "hoi/g1/stairs") == nil ||
		branchByName(t, data, "robot/use").Value("units") != int64(1) {
		t.Fatalf("after rename: %v", data.Value("items"))
	}
	var touched []string
	for _, c := range done.Value("changes").([]any) {
		touched = append(touched, pyjson.Str(c.(*pyjson.Object).Value("uid")))
	}
	if _, err := st.RenameLine(ctx, "hoi/g1", "hoi/pilot", touched, false, "user", Who{}); err != nil {
		t.Fatal(err)
	}
	data = listBranches(t, st)
	if branchByName(t, data, "hoi/g1") != nil || branchByName(t, data, "hoi/pilot/stairs") == nil {
		t.Fatalf("after undo: %v", data.Value("items"))
	}
	// Renaming the top moves the pinned topic's rule with it.
	if _, err := st.RenameLine(ctx, "hoi", "humanoid", nil, false, "user", Who{}); err != nil {
		t.Fatal(err)
	}
	if hb := branchByName(t, listBranches(t, st), "humanoid"); hb == nil || lineTopicUID(hb) != pinned {
		t.Fatalf("topic rule after rename: %v", hb)
	}
	if _, err := st.RenameLine(ctx, "humanoid", "bad//x", nil, true, "user", Who{}); err == nil {
		t.Fatal("bad target accepted")
	}
}

// lineTopicUID is the uid of a line's associated topic ("" without one).
func lineTopicUID(line *pyjson.Object) string {
	if tp, _ := line.Value("topic").(*pyjson.Object); tp != nil {
		return pyjson.Str(tp.Value("uid"))
	}
	return ""
}
