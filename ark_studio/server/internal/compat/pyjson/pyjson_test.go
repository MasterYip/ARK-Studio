package pyjson

import (
	"encoding/json"
	"os"
	"testing"
)

// vectors.json is produced by CPython (testdata/gen_vectors.py), so these
// tests compare against the reference implementation, not against our reading
// of it.
func TestAgainstCPythonVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		OK []struct {
			In, Dumps, Indent, Str string
		}
		Bad []string
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	for _, v := range data.OK {
		value, err := Loads(v.In)
		if err != nil {
			t.Errorf("Loads(%q): %v", v.In, err)
			continue
		}
		if got := string(Dumps(value)); got != v.Dumps {
			t.Errorf("Dumps(%q)\n got %q\nwant %q", v.In, got, v.Dumps)
		}
		if got := string(DumpsIndent(value, 2)); got != v.Indent {
			t.Errorf("DumpsIndent(%q)\n got %q\nwant %q", v.In, got, v.Indent)
		}
		if got := Str(value); got != v.Str {
			t.Errorf("Str(%q)\n got %q\nwant %q", v.In, got, v.Str)
		}
	}
	for _, text := range data.Bad {
		if _, err := Loads(text); err == nil {
			t.Errorf("Loads(%q) accepted invalid JSON", text)
		}
	}
	if len(data.OK) < 300 {
		t.Fatalf("only %d vectors", len(data.OK))
	}
}

func TestObjectOrder(t *testing.T) {
	o := NewObject("a", 1, "b", 2)
	o.Set("a", 3)
	o.Delete("b")
	o.Set("b", 4)
	if got := string(Dumps(o)); got != `{"a": 3, "b": 4}` {
		t.Fatal(got)
	}
}
