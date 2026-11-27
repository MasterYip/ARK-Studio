package store

// Review anchors that survive later edits (anchorresolve.go): the adaptive
// resolution on read.

import (
	"context"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestResolveHeadingOrder(t *testing.T) {
	body := strings.Join([]string{
		"## Results {#res}",
		"## res",
		"## 更新：Sharpa 手指改用 Dex-X 的增益（DEX-1c）",
		"## Baseline (v2)",
		"## Ablation of the contact reward",
		"Closing words. {#outro}",
	}, "\n")
	anchors := scanBodyAnchors(bodyLines(body))
	cases := []struct{ slug, label, id, how string }{
		{"res", "", "res", "explicit"}, // the explicit id wins over the "res" heading's slug
		{"outro", "", "outro", "explicit"},
		{"baseline-v2", "", "baseline-v2", "slug"},
		// X-0069: the note was written before "（DEX-1c）" was added.
		{"更新-sharpa-手指改用-dex-x-的增益", "更新：Sharpa 手指改用 Dex-X 的增益", "更新-sharpa-手指改用-dex-x-的增益-dex-1c", "normalized"},
		{"nothing-like-it", "更新：Sharpa 手指改用 Dex-X 的增益", "更新-sharpa-手指改用-dex-x-的增益-dex-1c", "normalized"},
		{"baseline", "", "baseline-v2", "normalized"},
		{"ＢＡＳＥＬＩＮＥ", "", "baseline-v2", "normalized"},
		{"ablation-of-contact-reward", "", "ablation-of-the-contact-reward", "fuzzy"},
	}
	for _, c := range cases {
		got, how, ok := resolveHeading(c.slug, c.label, anchors)
		if !ok || got.id != c.id || how != c.how {
			t.Errorf("resolve %q/%q = %q %q %v, want %q %q", c.slug, c.label, got.id, how, ok, c.id, c.how)
		}
	}
	for _, slug := range []string{"missing", "x", "contact"} {
		if got, how, ok := resolveHeading(slug, "", anchors); ok {
			t.Errorf("resolve %q = %q %q, want none", slug, got.id, how)
		}
	}
}

func TestReviewAnchorAdaptiveAndBroken(t *testing.T) {
	ctx := context.Background()
	st := reviewStore(t)
	// X-0069: the note links the heading as it was; "（DEX-1c）" came later.
	uid := mkUnit(t, st, pyjson.NewObject("type", "experiment", "title", "Gains",
		"body", "## 更新：Sharpa 手指改用 Dex-X 的增益（DEX-1c）\nnew gains\n## Results\nacc"), "human:ui")
	patch(t, st, uid, pyjson.NewObject("status", "review",
		"review_note", "见 [更新：Sharpa 手指改用 Dex-X 的增益](#更新-sharpa-手指改用-dex-x-的增益)；[x](#gone-entirely)"), "human:ui")
	info, err := st.ReviewInfo(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	anchors := info.Value("anchors").([]any)
	near, gone := anchors[0].(*pyjson.Object), anchors[1].(*pyjson.Object)
	if near.Value("ok") != true || near.Value("approx") != "normalized" ||
		near.Value("slug") != "更新-sharpa-手指改用-dex-x-的增益-dex-1c" || near.Value("written_slug") != "更新-sharpa-手指改用-dex-x-的增益" {
		t.Fatalf("adaptive anchor: %v", near)
	}
	if gone.Value("ok") != false || !strings.Contains(pyjson.Str(gone.Value("problem")), "its anchors: #更新-sharpa-手指改用-dex-x-的增益-dex-1c, #results") {
		t.Fatalf("broken anchor: %v", gone)
	}
	warnings := info.Value("warnings").([]any)
	if len(warnings) != 2 || !strings.Contains(pyjson.Str(warnings[0].(*pyjson.Object).Value("problem")), "closest heading") {
		t.Fatalf("warnings: %v", warnings)
	}
}

func TestReviewGroupBareAnchorsResolveInMembers(t *testing.T) {
	ctx := context.Background()
	st := reviewStore(t)
	agent := "agent:local/coder"
	// RG-11 (HOI-224 C): the note names the member in the text and links
	// its sections with bare #slug anchors.
	x := mkUnit(t, st, pyjson.NewObject("type", "experiment", "title", "RoboWild 共享时钟",
		"body", "## 结论 {#conclusion}\n快 1.4 倍\n## 公平性 {#fairness}\n同样的 CPU\n## 方法\n..."), agent)
	tk := mkUnit(t, st, pyjson.NewObject("type", "task", "title", "HOI-224 C",
		"body", "## 进展 {#progress}\n与计划有偏差\n## 结论\n另一个结论"), agent)
	note := "请看 " + x + " 的 [结论](#conclusion) 和 [公平性](#fairness)；" + tk + " 的 [进展](#progress) 记录了与计划的偏差；" +
		tk + " 的 [结论](#结论)、[方法](#方法) 和 [不存在](#nowhere)"
	g, err := st.CreateReviewGroup(ctx, pyjson.NewObject("title", "HOI-224 C • RoboWild 共享时钟", "review_note", note,
		"members", []any{x, tk}), agent)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"#conclusion": x, "#fairness": x, "#progress": tk, "#结论": tk, "#方法": x}
	for _, item := range g.Value("anchors").([]any) {
		a := item.(*pyjson.Object)
		raw := pyjson.Str(a.Value("raw"))
		if raw == "#nowhere" {
			if a.Value("ok") != false || !strings.Contains(pyjson.Str(a.Value("problem")), "no member") {
				t.Errorf("#nowhere: %v", a)
			}
			continue
		}
		if a.Value("ok") != true || a.Value("unit") != want[raw] {
			t.Errorf("%s: %v, want unit %s", raw, a, want[raw])
		}
	}
	if w := g.Value("warnings").([]any); len(w) != 1 {
		t.Fatalf("warnings: %v", w)
	}
}
