package gpu

import (
	"encoding/json"
	"os"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// TestPyvalAgainstCPython checks float(), round(x, 1), the queue's since
// filter and the probe parsers against testdata/pyval_vectors.json
// (testdata/gen_pyval.py).
func TestPyvalAgainstCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/pyval_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Float []struct{ In, Out, Err string }
		Round []struct {
			In  float64
			Out string
		}
		Since []struct {
			In  string
			Out *string
		}
		Smi, System []struct {
			In  string
			Out string
		}
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, v := range doc.Float {
		got, err := PyFloat(v.In)
		if v.Err != "" {
			if err == nil || err.Error() != v.Err {
				t.Errorf("float(%q): got %v, %v; want error %q", v.In, got, err, v.Err)
			}
			continue
		}
		if err != nil || pyjson.FloatRepr(got, false) != v.Out {
			t.Errorf("float(%q) = %v, %v; want %s", v.In, pyjson.FloatRepr(got, false), err, v.Out)
		}
	}
	for _, v := range doc.Round {
		if got := pyjson.FloatRepr(round1(v.In), false); got != v.Out {
			t.Errorf("round(%v, 1) = %s; want %s", v.In, got, v.Out)
		}
	}
	for _, v := range doc.Since {
		got, ok := sinceStamp(v.In)
		if (v.Out == nil) != !ok || (ok && got != *v.Out) {
			t.Errorf("since %q = %q, %v; want %v", v.In, got, ok, v.Out)
		}
	}
	norm := func(b []byte) string {
		v, err := pyjson.Loads(string(b))
		if err != nil {
			t.Fatal(err)
		}
		return string(pyjson.Dumps(v))
	}
	for _, v := range doc.Smi {
		gpus, warnings, err := ParseNvidiaSMI(v.In)
		if err != nil {
			t.Errorf("parse_nvidia_smi(%q): %v", v.In, err)
			continue
		}
		if got := string(pyjson.Dumps([]any{gpus, warnings})); got != norm([]byte(v.Out)) {
			t.Errorf("parse_nvidia_smi(%q)\n got %s\nwant %s", v.In, got, norm([]byte(v.Out)))
		}
	}
	for _, v := range doc.System {
		out, err := ParseSystem(v.In)
		if err != nil {
			t.Errorf("parse_system(%q): %v", v.In, err)
			continue
		}
		if got := string(pyjson.Dumps(out)); got != norm([]byte(v.Out)) {
			t.Errorf("parse_system(%q)\n got %s\nwant %s", v.In, got, norm([]byte(v.Out)))
		}
	}
}
