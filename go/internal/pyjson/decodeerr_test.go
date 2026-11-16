package pyjson

import (
	"encoding/json"
	"os"
	"testing"
)

// testdata/decode_errors.json comes from testdata/gen_decode_errors.py (CPython).
func TestDecodeErrorMessageCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/decode_errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases [][2]string
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range cases {
		got := DecodeErrorMessage(c[0])
		if got != c[1] {
			bad++
			if bad < 25 {
				t.Errorf("%q: got %q want %q", c[0], got, c[1])
			}
		}
		if _, err := Loads(c[0]); (err == nil) != (c[1] == "") {
			t.Errorf("%q: Loads err=%v but CPython says %q", c[0], err, c[1])
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d differ", bad, len(cases))
	}
}
