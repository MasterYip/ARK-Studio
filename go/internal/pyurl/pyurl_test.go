package pyurl

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestAgainstCPythonVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		In, Scheme, Netloc, Path, Params, Query, Fragment, Unquoted, Quote string
		QS                                                                 map[string][]string
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		got := Parse(v.In)
		want := Target{v.Scheme, v.Netloc, v.Path, v.Params, v.Query, v.Fragment}
		if got != want {
			t.Errorf("Parse(%q) = %+v, want %+v", v.In, got, want)
		}
		if u := Unquote(got.Path); u != v.Unquoted {
			t.Errorf("Unquote(%q) = %q, want %q", got.Path, u, v.Unquoted)
		}
		qs := ParseQS(got.Query)
		if len(qs) == 0 && len(v.QS) == 0 {
			continue
		}
		if !reflect.DeepEqual(map[string][]string(qs), v.QS) {
			t.Errorf("ParseQS(%q) = %v, want %v", got.Query, qs, v.QS)
		}
		if q := Quote(v.In, ""); q != v.Quote {
			t.Errorf("Quote(%q) = %q, want %q", v.In, q, v.Quote)
		}
	}
}

func TestIncrementalDecoder(t *testing.T) {
	src := []byte("a中😀b\xe4\xb8x\xff\xf0\x9f\x98")
	want := DecodeReplace(src)
	for cut := 0; cut <= len(src); cut++ {
		for cut2 := cut; cut2 <= len(src); cut2++ {
			var d Decoder
			got := d.Decode(src[:cut], false) + d.Decode(src[cut:cut2], false) + d.Decode(src[cut2:], false) + d.Decode(nil, true)
			if got != want {
				t.Fatalf("cut %d/%d: %q want %q", cut, cut2, got, want)
			}
		}
	}
}
