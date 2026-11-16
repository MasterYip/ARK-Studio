package skillkit

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
)

func scrubNow(v any) any {
	switch x := v.(type) {
	case *obj:
		for _, k := range x.Keys() {
			if k == "created_at" {
				x.Set(k, "<NOW>")
			} else {
				x.Set(k, scrubNow(x.Value(k)))
			}
		}
	case []any:
		for i := range x {
			x[i] = scrubNow(x[i])
		}
	}
	return v
}

func render(res any, err error) string {
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			return "!V:" + e.Msg
		}
		var u *Uncaught
		if errors.As(err, &u) {
			return "!" + u.Class + ": " + u.Message
		}
		var x *xmlError
		if errors.As(err, &x) {
			return "!ParseError: " + x.Error()
		}
		return "!?" + err.Error()
	}
	return string(pyjson.Dumps(scrubNow(res)))
}

// testdata/vectors.json.gz comes from testdata/gen_vectors.py (CPython).
func TestCPythonVectors(t *testing.T) {
	now = func() string { return "2000-01-01T00:00:00+00:00" }
	f, err := os.Open("testdata/vectors.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var cases [][3]string
	if err := json.NewDecoder(zr).Decode(&cases); err != nil {
		t.Fatal(err)
	}
	bad := map[string]int{}
	total := map[string]int{}
	for _, c := range cases {
		name, input, want := c[0], c[1], c[2]
		total[name]++
		v, err := pyjson.Loads(input)
		if err != nil {
			t.Fatalf("bad input %q", input)
		}
		p, _ := v.(*obj)
		var got string
		switch name {
		case "task":
			got = render(PrepareTask(p, or(p.Value("existing_ids"), []any{}),
				pyjson.NewObject("id", p.Value("project_id"), "name", p.Value("project_name"))))
		case "manifest":
			ids, err := iterate(or(p.Value("existing_ids"), []any{}))
			if err != nil {
				got = render(nil, err)
				break
			}
			kept := []any{}
			for _, x := range ids {
				if str(x) != str(p.Value("task_id")) {
					kept = append(kept, x)
				}
			}
			got = render(ValidateManifest(p, kept))
		case "round":
			got = render(ValidateRound(p))
		case "history":
			got = render(HistorySnapshot(p))
		case "edit":
			got = render(ValidateEdit(p), nil)
		case "leakage":
			got = render(ScanLeakage(p.Value("text").(string), nil))
		case "leakage-patterns":
			got = render(ScanLeakage(p.Value("text").(string), p.Value("patterns").([]any)))
		case "domain":
			got = render(ValidateDomain(p))
		case "theme":
			got = render(ValidateTheme(p.Value("theme"), p.Value("variant")))
		case "substitute":
			got = render(substitute(p.Value("template").(string), p.Value("context").(*obj)))
		case "render-value":
			key := p.Value("key").(string)
			got = render(substitute("[$"+key+"]", pyjson.NewObject(key, p.Value("value"))))
		case "xml":
			tag, err := xmlRoot([]byte(v.(string)))
			if err != nil {
				got = render(nil, err)
			} else {
				got = string(pyjson.Dumps(tag))
			}
		case "upper":
			got = string(pyjson.Dumps(pytext.Upper(v.(string))))
		default:
			t.Fatalf("unknown vector %s", name)
		}
		if got != want {
			bad[name]++
			if bad[name] <= 3 {
				at := 0
				for at < len(got) && at < len(want) && got[at] == want[at] {
					at++
				}
				lo := at - 120
				if lo < 0 {
					lo = 0
				}
				t.Errorf("%s %.200s\n got ...%.300s\nwant ...%.300s", name, input, got[lo:], want[lo:])
			}
		}
	}
	for name, n := range total {
		if bad[name] > 0 {
			t.Errorf("%s: %d of %d vectors differ", name, bad[name], n)
		}
	}
	t.Logf("%d vectors", len(cases))
}
