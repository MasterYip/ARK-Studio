package secretredact

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestAgainstCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Text []struct {
			Text  string
			Extra []string
			Out   string
			N     int
		}
		Value struct {
			In, Out json.RawMessage
			N       int
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, c := range doc.Text {
		out, n := Text(c.Text, c.Extra)
		if out != c.Out || n != c.N {
			bad++
			if bad < 10 {
				t.Errorf("Text(%q, %q) = %q, %d; want %q, %d", c.Text, c.Extra, out, n, c.Out, c.N)
			}
		}
	}
	in, _ := pyjson.Loads(string(doc.Value.In))
	want, _ := pyjson.Loads(string(doc.Value.Out))
	out, n := Value(in, []string{"supersecrettoken1"})
	if !bytes.Equal(pyjson.Dumps(out), pyjson.Dumps(want)) || n != doc.Value.N {
		t.Errorf("Value = %s, %d; want %s, %d", pyjson.Dumps(out), n, pyjson.Dumps(want), doc.Value.N)
	}
	t.Logf("%d text vectors, %d mismatches", len(doc.Text), bad)
}
