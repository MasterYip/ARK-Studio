package api

import (
	"reflect"
	"testing"

	"arkstudio/go/internal/compat/pyurl"
)

func TestPyRe(t *testing.T) {
	cases := []struct {
		pattern string
		full    bool
		path    string
		want    bool
	}{
		{`^/api/units/([^/]+)$`, false, "/api/units/a", true},
		{`^/api/units/([^/]+)$`, false, "/api/units/a\n", true},   // `$` before a final newline
		{`^/api/units/([^/]+)$`, false, "/api/units/a\n\n", true}, // [^/]+ takes "a\n", `$` matches before the last "\n"
		{`^/api/units/([^/]+)$`, false, "/api/units/a/b", false},
		{`/api/media/(\d+)/raw`, true, "/api/media/１２/raw", true}, // Unicode \d
		{`/api/media/(\d+)/raw`, true, "/api/media/1/raw/x", false},
		{`/api/media/(\d+)/raw`, false, "/api/media/1/raw/x", true}, // re.match: prefix only
		{`^/a\$`, false, "/a$", true},
	}
	for _, tc := range cases {
		if got := PyRe(tc.pattern, tc.full).MatchString(tc.path); got != tc.want {
			t.Errorf("PyRe(%q, %v) on %q = %v", tc.pattern, tc.full, tc.path, got)
		}
	}
}

func TestQList(t *testing.T) {
	q := pyurl.Values{"tags": {"a, b", "b,,c"}, "tag": {" d ", "a"}}
	if got := QList(q, "tags", "tag"); !reflect.DeepEqual(got, []any{"a", "b", "c", "d"}) {
		t.Fatalf("%v", got)
	}
	if got := QList(q, "none"); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestStrOrAndErr(t *testing.T) {
	for in, want := range map[any]string{nil: "", "": "", false: "", int64(0): "", "x": "x", int64(3): "3", true: "True"} {
		if got := StrOr(in); got != want {
			t.Errorf("StrOr(%v) = %q, want %q", in, got, want)
		}
	}
	e := Err(404, "missing", "NOT_FOUND")
	if e.Error() != "missing" || e.Status != 404 || e.Code != "NOT_FOUND" {
		t.Fatalf("%+v", e)
	}
}
