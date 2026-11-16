package settings

import (
	"encoding/json"
	"os"
	"testing"

	"arkstudio/go/internal/pyjson"
)

func TestArrowsAgainstPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases map[string][][2]string
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	result := func(v *pyjson.Object, err error) string {
		if err != nil {
			return string(pyjson.Dumps(pyjson.NewObject("error", err.Error())))
		}
		return string(pyjson.Dumps(v))
	}
	fns := map[string]func(any) string{
		"normalize_arrows": func(v any) string { return result(NormalizeArrows(v), nil) },
		"update_arrows": func(v any) string {
			base := pyjson.NewObject("relations", pyjson.NewObject("cites", pyjson.NewObject("head", "bar")))
			return result(UpdateArrows(base, v))
		},
		"personal_appearance": func(v any) string { return result(PersonalAppearance(v), nil) },
		"update_sessions": func(v any) string {
			return result(UpdateSessions(pyjson.NewObject("accept_uploads", true), v))
		},
		"project_settings": func(v any) string {
			return result(ProjectSettings(pyjson.NewObject("settings", v)))
		},
	}
	n := 0
	for name, list := range cases {
		fn := fns[name]
		if fn == nil {
			t.Fatalf("no Go function for %s", name)
		}
		for _, c := range list {
			in, err := pyjson.Loads(c[0])
			if err != nil {
				t.Fatal(err)
			}
			if got := fn(in); got != c[1] {
				t.Errorf("%s(%s)\n got %s\nwant %s", name, c[0], got, c[1])
			}
			n++
		}
	}
	if n < 50 {
		t.Fatalf("only %d vectors", n)
	}
}
