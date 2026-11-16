package server

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestPortedMatchesGolden pins the route list derived from the router's
// registrations to the hand-kept list it replaced (testdata/ported_golden.json,
// `ark-server routes --json` of 4ccf2a8+carry-overs).  A new route must be
// added to the golden file deliberately.
func TestPortedMatchesGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/ported_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden []struct {
		ID      string   `json:"id"`
		Methods []string `json:"methods"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, r := range golden {
		for _, m := range r.Methods {
			want[r.ID+" "+m] = true
		}
	}
	got := map[string]bool{}
	ids := map[string]bool{}
	for _, r := range Ported {
		if ids[r.ID] {
			t.Errorf("Ported lists %q twice", r.ID)
		}
		ids[r.ID] = true
		for _, m := range strings.Split(r.Methods, ",") {
			got[r.ID+" "+m] = true
		}
	}
	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("derived Ported differs from the golden list:\nmissing %v\nextra %v", missing, extra)
	}
}

func TestDispatchOrderAndMatchers(t *testing.T) {
	var hit []string
	rec := func(name string) func(*reqCtx) error {
		return func(*reqCtx) error { hit = append(hit, name); return nil }
	}
	boom := errors.New("boom")
	table := []route{
		exact("/a", rec("exact /a")),
		match(pyRe(`^/u/([^/]+)$`, false), func(_ *reqCtx, m []string) error { hit = append(hit, "match "+m[1]); return nil }),
		prefix("/p/", func(_ *reqCtx, path string) error { hit = append(hit, "prefix "+path); return nil }),
		when(func(p string) bool { return p == "/err" }, func(*reqCtx, string) error { return boom }),
		sub(func(_ *reqCtx, path string) (bool, error) { return path == "/sub", nil }),
		// Shadowed: the prefix above takes /p/x first (Python branch order).
		exact("/p/x", rec("shadowed")),
	}
	cases := []struct {
		path    string
		handled bool
		err     error
		hit     string
	}{
		{"/a", true, nil, "exact /a"},
		{"/a/", false, nil, ""},
		{"/u/abc", true, nil, "match abc"},
		{"/u/abc\n", true, nil, "match abc\n"}, // Python `$` before a final newline
		{"/u/a/b", false, nil, ""},
		{"/p/x", true, nil, "prefix /p/x"},
		{"/err", true, boom, ""},
		{"/sub", true, nil, ""},
		{"/none", false, nil, ""},
	}
	for _, tc := range cases {
		hit = nil
		handled, err := dispatch(&reqCtx{}, table, tc.path)
		if handled != tc.handled || !errors.Is(err, tc.err) {
			t.Errorf("%q: handled=%v err=%v, want %v %v", tc.path, handled, err, tc.handled, tc.err)
		}
		got := strings.Join(hit, ",")
		if got != tc.hit {
			t.Errorf("%q: hit %q, want %q", tc.path, got, tc.hit)
		}
	}
}

func TestDerivePortedMergesVerbs(t *testing.T) {
	saved := allDispatchTables
	defer func() { allDispatchTables = saved }()
	allDispatchTables = [][]route{
		{exact("/x", nil, PortedRoute{"exact:/x", "GET,HEAD"})},
		{exact("/x", nil, PortedRoute{"exact:/x", "POST,GET"})},
	}
	for _, r := range derivePorted() {
		if r.ID == "exact:/x" && r.Methods != "GET,HEAD,POST" {
			t.Fatalf("merged verbs %q", r.Methods)
		}
	}
}
