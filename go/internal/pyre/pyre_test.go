package pyre

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAgainstCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		P     string
		F     int
		Error *string
		Empty bool
		Runs  [][2]json.RawMessage
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		re, err := Compile(c.P, c.F)
		if c.Error != nil {
			got := ""
			if err != nil {
				got = err.Error()
				if _, ok := err.(*OverflowError); ok {
					got = "OverflowError: " + got
				}
				if _, ok := err.(*RuntimeError); ok {
					got = "RuntimeError: " + got
				}
				if _, ok := err.(*ValueError); ok {
					got = "ValueError: " + got
				}
			}
			if got != *c.Error {
				bad++
				t.Errorf("%q flags=%d: error %q, want %q", c.P, c.F, got, *c.Error)
			}
			continue
		}
		if err != nil {
			bad++
			t.Errorf("%q flags=%d: unexpected error %v", c.P, c.F, err)
			continue
		}
		if re.MatchesEmpty() != c.Empty {
			bad++
			t.Errorf("%q flags=%d: search('') = %v", c.P, c.F, !c.Empty)
		}
		for _, run := range c.Runs {
			var s string
			var want [][2]int
			json.Unmarshal(run[0], &s)    //nolint:errcheck
			json.Unmarshal(run[1], &want) //nolint:errcheck
			budget := 1 << 22
			got, err := re.FindAll([]rune(s), &budget)
			ok := err == nil && len(got) == len(want)
			for i := 0; ok && i < len(got); i++ {
				ok = got[i].Start == want[i][0] && got[i].End == want[i][1]
			}
			if !ok {
				bad++
				t.Errorf("%q flags=%d on %q: %v, want %v (%v)", c.P, c.F, s, got, want, err)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d mismatches", bad)
	}
}
