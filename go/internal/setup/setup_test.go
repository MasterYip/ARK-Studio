package setup

import (
	"encoding/json"
	"os"
	"testing"

	"arkstudio/go/internal/pyjson"
)

// TestAgainstCPython replays testdata/vectors.json (gen_vectors.py):
// normalize_profile, digest, questions, readiness, render_summary and
// normalize_layer (project and user) of random trap profiles.
func TestAgainstCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	load := func(text string) *obj {
		v, err := pyjson.Loads(text)
		if err != nil {
			t.Fatal(err)
		}
		return v.(*obj)
	}
	dump := func(v any) string { return string(pyjson.Dumps(v)) }
	bad := 0
	report := func(i int, what, got, want string) {
		bad++
		if bad <= 10 {
			t.Errorf("vector %d %s:\n got %s\nwant %s\n  in %s", i, what, got, want, rows[i]["in"])
		}
	}
	for i, row := range rows {
		profile, errs, warns := NormalizeProfile(load(row["in"]))
		if got := dump([]any{profile, strList(errs), strList(warns)}); got != row["profile"] {
			report(i, "normalize_profile", got, row["profile"])
			continue
		}
		if got := Digest(profile); got != row["digest"] {
			report(i, "digest", got, row["digest"])
		}
		if got := dump(Questions(profile)); got != row["questions"] {
			report(i, "questions", got, row["questions"])
		}
		if got := dump(Readiness(profile, row["state"])); got != row["readiness"] {
			report(i, "readiness", got, row["readiness"])
		}
		if got := RenderSummary(load(row["record"])); got != row["summary"] {
			report(i, "render_summary", got, row["summary"])
		}
		for _, layer := range []string{"project", "user"} {
			p, locs, e, w, err := normalizeLayer(load(row["in"]), layer, "Bob", nil)
			got := ""
			if err != nil {
				got = "error: " + err.Error()
			} else {
				pairs := []any{}
				for _, l := range locs {
					pairs = append(pairs, []any{l.id, l.path})
				}
				got = dump([]any{p, pairs, strList(e), strList(w)})
			}
			if got != row[layer] {
				report(i, "normalize_layer "+layer, got, row[layer])
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d vectors differ", bad, len(rows))
	}
}

func TestPathStr(t *testing.T) {
	if !windows {
		t.Skip("Windows pathlib rules")
	}
	for in, want := range map[string]string{"": ".", "C:/a//b/./": `C:\a\b`, "/x": `\x`, "C:": "C:",
		`\\srv\share\x`: `\\srv\share\x`, "a/b": `a\b`} {
		if got := pathStr(in); got != want {
			t.Errorf("pathStr(%q) = %q, want %q", in, got, want)
		}
	}
	if got := pathParent(`C:\a`); got != `C:\` {
		t.Errorf("parent = %q", got)
	}
}
