package store

import (
	"context"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// markerTags are a unit's marker:* tags.
func markerTags(t *testing.T, st *Store, uid string) []string {
	t.Helper()
	var out []string
	for _, tag := range unitField(t, st, uid, "tags").([]string) {
		if TagNamespace(tag) == "marker" {
			out = append(out, tag)
		}
	}
	return out
}

func sameList(a []string, b ...string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMarkersOnePerExclusiveGroup(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	u, err := st.CreateUnit(ctx, pyjson.NewObject("type", "note", "title", "A",
		"tags", []any{"marker:star-yellow", "marker:star-blue", "Marker:question", "marker:check-green", "marker:question"}), "agent:x", Who{})
	if err != nil {
		t.Fatal(err)
	}
	uid := pyjson.Str(u.Value("uid"))
	if got := markerTags(t, st, uid); !sameList(got, "marker:check-green", "marker:question", "marker:star-blue") {
		t.Fatalf("create: %v", got)
	}
	// Writing another star replaces the star; the symbols stay.
	if _, err := st.UpdateUnit(ctx, uid, pyjson.NewObject("tags", []any{"marker:star-blue", "marker:question", "marker:check-green", "marker:star-red"}), "agent:x", Who{}); err != nil {
		t.Fatal(err)
	}
	if got := markerTags(t, st, uid); !sameList(got, "marker:check-green", "marker:question", "marker:star-red") {
		t.Fatalf("update: %v", got)
	}
	// Progress: one step at a time.
	if _, err := st.UpdateUnit(ctx, uid, pyjson.NewObject("tags", []any{"marker:progress-25", "marker:progress-75"}), "agent:x", Who{}); err != nil {
		t.Fatal(err)
	}
	if got := markerTags(t, st, uid); !sameList(got, "marker:progress-75") {
		t.Fatalf("progress: %v", got)
	}
}

func TestMarkersRepairOnWrite(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	uid := trashUnit(t, st, "Legacy")
	id := unitField(t, st, uid, "id")
	// A unit from before the rule: three stars.
	err := st.write(ctx, func(w *wtx) error {
		for _, tag := range []string{"marker:star-yellow", "marker:star-blue", "marker:star-green", "marker:question"} {
			if _, err := w.q.ExecContext(ctx, "INSERT INTO unit_tags(unit_id,tag) VALUES(?,?)", id, tag); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := markerTags(t, st, uid); len(got) != 4 {
		t.Fatalf("setup: %v", got)
	}
	// Any write keeps one star (the one that sorts last) and logs the change.
	if _, err := st.UpdateUnit(ctx, uid, pyjson.NewObject("title", "Legacy 2"), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	if got := markerTags(t, st, uid); !sameList(got, "marker:question", "marker:star-yellow") {
		t.Fatalf("repaired: %v", got)
	}
}

func TestMarkersBatchAddRemove(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	a, b := trashUnit(t, st, "A"), trashUnit(t, st, "B")
	if _, err := st.UpdateUnit(ctx, a, pyjson.NewObject("tags", []any{"marker:star-yellow", "keep"}), "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	items := []BatchItem{{Ref: a, TagsAdd: []string{"marker:star-blue"}}, {Ref: b, TagsAdd: []string{"marker:star-blue"}}}
	if _, err := st.BatchUpdate(ctx, items, "b_mark1", "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{a, b} {
		if got := markerTags(t, st, uid); !sameList(got, "marker:star-blue") {
			t.Fatalf("%s after batch add: %v", uid, got)
		}
	}
	if got := batchActivity(t, st, "b_mark1"); len(got) != 2 {
		t.Fatalf("activity: %v", got)
	}
	// Adding the same marker again changes nothing.
	out, err := st.BatchUpdate(ctx, []BatchItem{{Ref: a, TagsAdd: []string{"marker:star-blue"}}}, "b_mark2", "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(out.Value("updated").([]any)); n != 0 {
		t.Fatalf("re-adding is a no-op, updated %d", n)
	}
	items = []BatchItem{{Ref: a, TagsRemove: []string{"marker:star-blue"}}, {Ref: b, TagsRemove: []string{"marker:star-blue"}}}
	if _, err := st.BatchUpdate(ctx, items, "b_mark3", "human:ui", Who{}); err != nil {
		t.Fatal(err)
	}
	if got := markerTags(t, st, a); len(got) != 0 {
		t.Fatalf("removed: %v", got)
	}
	if got := unitField(t, st, a, "tags").([]string); len(got) != 4 || got[2] != "tag:keep" {
		t.Fatalf("other tags stay: %v", got)
	}
}

func TestMarkersFollowProjectGroups(t *testing.T) {
	st := trashStore(t)
	ctx := context.Background()
	groups, _ := pyjson.Loads(`{"groups": [{"id": "mood", "exclusive": true, "markers": [
		{"id": "mood-happy", "glyph": "☺", "color": "#22c55e"}, {"id": "mood-sad", "glyph": "☹", "color": "#64748b"}]},
		{"id": "star", "exclusive": false, "markers": [{"id": "star-yellow", "glyph": "★", "color": "#f59e0b"},
		{"id": "star-blue", "glyph": "★", "color": "#3b82f6"}]}]}`)
	if _, err := st.Manifest.Mutate(func(o *pyjson.Object) error {
		o.Set("settings", pyjson.NewObject("markers", groups))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUnit(ctx, pyjson.NewObject("type", "note", "title", "A",
		"tags", []any{"marker:mood-happy", "marker:mood-sad", "marker:star-yellow", "marker:star-blue"}), "human:ui", Who{})
	if err != nil {
		t.Fatal(err)
	}
	if got := markerTags(t, st, pyjson.Str(u.Value("uid"))); !sameList(got, "marker:mood-sad", "marker:star-blue", "marker:star-yellow") {
		t.Fatalf("custom groups: %v", got)
	}
}
