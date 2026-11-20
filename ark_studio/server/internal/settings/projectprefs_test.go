package settings

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestProjectPrefs(t *testing.T) {
	ids := func(o *pyjson.Object, key string) string {
		var out []string
		for _, v := range o.Value(key).([]any) {
			out = append(out, v.(string))
		}
		return strings.Join(out, ",")
	}
	got, err := ProjectPrefs(pyjson.NewObject("pinned", []any{"b", "a", "b", "h"}, "hidden", []any{"h", "x"}), true)
	if err != nil {
		t.Fatal(err)
	}
	if ids(got, "pinned") != "b,a" || ids(got, "hidden") != "h,x" {
		t.Errorf("pinned %q hidden %q", ids(got, "pinned"), ids(got, "hidden"))
	}
	if got, _ := ProjectPrefs(pyjson.NewObject("pinned", []any{}, "hidden", []any{}), true); got.Len() != 0 {
		t.Error("two empty lists are no preference")
	}
	for _, bad := range []any{
		"x", []any{}, pyjson.NewObject("order", []any{}), pyjson.NewObject("pinned", "a"),
		pyjson.NewObject("pinned", []any{int64(1)}), pyjson.NewObject("hidden", []any{""}),
		pyjson.NewObject("hidden", []any{"a\nb"}), pyjson.NewObject("hidden", []any{strings.Repeat("x", 201)}),
	} {
		if _, err := ProjectPrefs(bad, true); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	long := make([]any, 201)
	for i := range long {
		long[i] = string(rune('a'+i%26)) + strings.Repeat("y", i)
	}
	if _, err := ProjectPrefs(pyjson.NewObject("pinned", long), true); err == nil {
		t.Error("accepted 201 pins")
	}
	// Stored prefs are cleaned, never refused.
	got, err = ProjectPrefs(pyjson.NewObject("pinned", []any{"a", int64(3)}, "extra", true), false)
	if err != nil || ids(got, "pinned") != "a" || ids(got, "hidden") != "" {
		t.Errorf("lenient: %v %v", got, err)
	}
	if got, _ := ProjectPrefs("junk", false); got.Len() != 0 {
		t.Error("lenient junk")
	}
	// "Created by me" filter: kept when true, dropped when false, refused when not a boolean.
	got, err = ProjectPrefs(pyjson.NewObject("only_mine", true), true)
	if err != nil || got.Value("only_mine") != true || ids(got, "pinned") != "" {
		t.Errorf("only_mine: %v %v", got, err)
	}
	if got, _ := ProjectPrefs(pyjson.NewObject("only_mine", false), true); got.Len() != 0 {
		t.Errorf("only_mine false must leave the prefs empty: %v", got)
	}
	if _, err := ProjectPrefs(pyjson.NewObject("only_mine", "yes"), true); err == nil {
		t.Error("accepted a non-boolean only_mine")
	}
}
