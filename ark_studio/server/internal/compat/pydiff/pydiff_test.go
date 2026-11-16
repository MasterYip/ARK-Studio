package pydiff

import (
	"encoding/json"
	"os"
	"testing"

	"arkstudio/go/internal/compat/pytext"
)

func TestAgainstCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		A, B string
		Ops  [][]any
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for n, c := range cases {
		got := Opcodes([]rune(c.A), []rune(c.B))
		if len(got) != len(c.Ops) {
			t.Fatalf("case %d: %d ops, want %d", n, len(got), len(c.Ops))
		}
		for i, op := range got {
			w := c.Ops[i]
			if op.Tag != w[0].(string) || float64(op.I1) != w[1].(float64) || float64(op.I2) != w[2].(float64) ||
				float64(op.J1) != w[3].(float64) || float64(op.J2) != w[4].(float64) {
				t.Fatalf("case %d op %d: got %+v want %v", n, i, op, w)
			}
		}
	}
}

func TestUnifiedAgainstCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/unified.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		A, B, Diff, To string
		Lines          []string
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for n, c := range cases {
		got := UnifiedDiff(pytext.SplitLines(c.A, true), pytext.SplitLines(c.B, true), "a/f", c.To, 3)
		if got != c.Diff {
			t.Fatalf("case %d:\n%q\nwant\n%q", n, got, c.Diff)
		}
		lines := pytext.SplitLines(c.A, false)
		if len(lines) != len(c.Lines) {
			t.Fatalf("case %d splitlines: %q want %q", n, lines, c.Lines)
		}
		for i := range lines {
			if lines[i] != c.Lines[i] {
				t.Fatalf("case %d splitlines: %q want %q", n, lines, c.Lines)
			}
		}
	}
}
