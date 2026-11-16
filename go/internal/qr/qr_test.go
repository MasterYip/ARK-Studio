package qr

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAgainstPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Data, SVG string }
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		got, err := SVG(c.Data)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.SVG {
			t.Errorf("payload %q: SVG differs", c.Data)
		}
	}
}
