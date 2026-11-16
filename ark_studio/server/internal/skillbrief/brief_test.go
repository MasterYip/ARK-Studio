package skillbrief

import (
	"encoding/json"
	"os"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// testdata/vectors.json comes from testdata/gen_vectors.py (CPython).
func TestValidateBriefCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]string
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		in, ok := pyjson.LoadsObject(c[0])
		if !ok {
			t.Fatalf("bad input %q", c[0])
		}
		var got string
		res, err := ValidateBrief(in)
		if err != nil {
			got = "!" + err.Error()
		} else {
			got = string(pyjson.Dumps(res))
		}
		if got != c[1] {
			bad++
			if bad <= 10 {
				t.Errorf("input %s\n got %s\nwant %s", c[0], got, c[1])
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d differ", bad, len(cases))
	}
}
