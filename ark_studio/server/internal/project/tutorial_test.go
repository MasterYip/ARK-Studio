package project

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/platform/clock"
	"arkstudio/go/internal/store"
)

// The tutorial seed (demo._seed via gen_seeds.py): English chapters
// S-0001..S-0009 (lang:en), Chinese chapters S-0010..S-0018 (lang:zh), each
// Chinese chapter linked to its English original, and every chapter body
// ending with clickable [[UID]] links to the previous and next chapter.
func TestTutorialSeedChaptersAndNavigation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(dir, store.NewManifest(demoManifest()), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := ApplySeed(ctx, st, "tutorial"); err != nil {
		t.Fatal(err)
	}
	uid := func(n int) string { return fmt.Sprintf("S-%04d", n) }
	ref := regexp.MustCompile(`\[\[([A-Za-z0-9][A-Za-z0-9_-]*)\]\]`)
	for lang, first := range map[string]int{"en": 1, "zh": 10} {
		for seq := 0; seq < 9; seq++ {
			unit, err := st.GetUnit(ctx, uid(first+seq))
			if err != nil {
				t.Fatal(err)
			}
			tags, _ := unit.Value("tags").([]string)
			for _, want := range []string{"tutorial:chapter", fmt.Sprintf("seq:%02d", seq), "lang:" + lang} {
				if !slices.Contains(tags, want) {
					t.Fatalf("%s tags %v lack %s", uid(first+seq), tags, want)
				}
			}
			body := pyjson.Str(unit.Value("body"))
			if strings.Contains(body, "{{") {
				t.Fatalf("%s has an unresolved placeholder", uid(first+seq))
			}
			_, footer, ok := strings.Cut(body, "\n---\n\n")
			if !ok {
				t.Fatalf("%s has no chapter navigation", uid(first+seq))
			}
			var refs []string
			for _, m := range ref.FindAllStringSubmatch(footer, -1) {
				refs = append(refs, m[1])
			}
			next := uid(first + (seq+1)%9)
			want := []string{next}
			if seq > 0 {
				want = []string{uid(first + seq - 1), next}
			}
			if !slices.Equal(refs, want) {
				t.Fatalf("%s navigation %v, want %v", uid(first+seq), refs, want)
			}
			if lang == "zh" {
				found := false
				for _, raw := range unit.Value("links_out").([]any) {
					link := raw.(*pyjson.Object)
					target, _ := link.Value("target").(*pyjson.Object)
					if link.Value("relation") == "relates_to" && target != nil && target.Value("uid") == uid(1+seq) {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s is not linked to its English chapter %s", uid(first+seq), uid(1+seq))
				}
			}
		}
	}
}

// The tutorial topics: TP-0002 (lang:en) and TP-0003 (lang:zh) hold their
// language's chapters on one row, left to right in reading order, and every
// example; they show part_of and inspires links only.
func TestTutorialSeedTopicGraphs(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir(), store.NewManifest(demoManifest()), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := ApplySeed(ctx, st, "tutorial"); err != nil {
		t.Fatal(err)
	}
	num := func(v any) float64 {
		switch n := v.(type) {
		case int64:
			return float64(n)
		case float64:
			return n
		}
		t.Fatalf("not a number: %#v", v)
		return 0
	}
	for topicUID, spec := range map[string]struct {
		lang  string
		first int
	}{"TP-0002": {"en", 1}, "TP-0003": {"zh", 10}} {
		unit, err := st.GetUnit(ctx, topicUID)
		if err != nil {
			t.Fatal(err)
		}
		tags, _ := unit.Value("tags").([]string)
		if !slices.Contains(tags, "tutorial:topic") || !slices.Contains(tags, "lang:"+spec.lang) {
			t.Fatalf("%s tags %v", topicUID, tags)
		}
		data, err := st.Topic(ctx, topicUID, false)
		if err != nil {
			t.Fatal(err)
		}
		x := map[string]float64{}
		for _, m := range data.Value("members").([]*pyjson.Object) {
			member := m.Value("member").(*pyjson.Object)
			x[pyjson.Str(m.Value("uid"))] = num(member.Value("x"))
			if strings.HasPrefix(pyjson.Str(m.Value("uid")), "S-") && num(member.Value("y")) != 0 {
				t.Fatalf("%s: chapter %v is off the row", topicUID, m.Value("uid"))
			}
		}
		if len(x) < 9+30 {
			t.Fatalf("%s: %d members", topicUID, len(x))
		}
		for seq := 1; seq < 9; seq++ {
			a, b := fmt.Sprintf("S-%04d", spec.first+seq-1), fmt.Sprintf("S-%04d", spec.first+seq)
			if _, ok := x[b]; !ok || x[a] >= x[b] {
				t.Fatalf("%s: %s (x %v) not left of %s (x %v)", topicUID, a, x[a], b, x[b])
			}
		}
	}
}

// The relative dates (SeedShiftDays) still move the tutorial's dated
// examples: T-0002 (the sweep task) is written for 2026-11-02 with the anchor 2026-11-01.
func TestTutorialSeedDatesStillShift(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir(), store.NewManifest(demoManifest()), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := ApplySeed(ctx, st, "tutorial"); err != nil {
		t.Fatal(err)
	}
	unit, err := st.GetUnit(ctx, "T-0002")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC).AddDate(0, 0, SeedShiftDays("tutorial", clock.Now())).Format("2006-01-02")
	if got := pyjson.Str(unit.Value("start_at")); got != want {
		t.Fatalf("T-0002 start_at %q, want %q", got, want)
	}
}
