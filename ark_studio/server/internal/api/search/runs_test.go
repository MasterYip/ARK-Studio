package search

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/api/apitest"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyurl"
	dom "arkstudio/go/internal/search"
)

// A newer run with the same sid supersedes the older one; at most 500 ids
// are kept, the oldest inserted id first out (Python dict order).
func TestSearchRunsSupersedeAndBound(t *testing.T) {
	r := &runs{tokens: map[string]int64{}}
	first := r.set("panel:files")
	second := r.set("panel:files")
	if r.current("panel:files", first) || !r.current("panel:files", second) {
		t.Fatal("newer run must supersede the older one")
	}
	r.done("panel:files", first) // the stale run must not drop the newer one
	if !r.current("panel:files", second) {
		t.Fatal("stale run removed the current one")
	}
	r.done("panel:files", second)
	for i := 0; i < 501; i++ {
		r.set(fmt.Sprintf("sid-%d", i))
	}
	if n, has0 := len(r.tokens), r.tokens["sid-0"] != 0; n != 500 || has0 {
		t.Fatalf("bounded map: %d ids, oldest kept=%v", n, has0)
	}
}

func TestOptions(t *testing.T) {
	cases := []struct {
		q    pyurl.Values
		want dom.Options
	}{
		{pyurl.Values{}, dom.Options{}},
		{pyurl.Values{"case": {"1"}, "word": {"TRUE"}, "regex": {"on"}}, dom.Options{Case: true, Word: true, Regex: true}},
		{pyurl.Values{"case": {"yes"}, "word": {"0"}, "regex": {"2"}}, dom.Options{Case: true}},
	}
	for _, tc := range cases {
		if got := Options(&apitest.Ctx{Q: tc.q}); got != tc.want {
			t.Errorf("%v: %+v, want %+v", tc.q, got, tc.want)
		}
	}
}

func TestErr(t *testing.T) {
	var ae *api.Error
	if err := Err(&dom.Error{Message: "bad regex"}); !errors.As(err, &ae) || ae.Status != 400 || ae.Message != "bad regex" {
		t.Errorf("search error: %v", err)
	}
	if err := Err(&dom.InternalError{Message: "boom"}); !errors.As(err, &ae) || ae.Status != 500 || ae.Message != "Internal error: boom" {
		t.Errorf("internal error: %v", err)
	}
	other := errors.New("x")
	if Err(other) != other {
		t.Error("other errors pass through")
	}
}

func TestStreamLines(t *testing.T) {
	cases := []struct {
		name    string
		gzip    bool
		run     func(cancelled func() bool, emit dom.Emit) (*pyjson.Object, error)
		want    []string
		headers string
	}{
		{"done", false, func(_ func() bool, emit dom.Emit) (*pyjson.Object, error) {
			emit(pyjson.NewObject("id", "u1"))
			return pyjson.NewObject("count", 1), nil
		}, []string{`{"type": "node", "node": {"id": "u1"}}`, `{"type": "done", "count": 1, "cancelled": false}`},
			"X-Accel-Buffering=no Connection=close"},
		{"error", true, func(_ func() bool, _ dom.Emit) (*pyjson.Object, error) {
			return nil, &dom.Error{Message: "bad"}
		}, []string{`{"type": "error", "message": "bad"}`},
			"X-Accel-Buffering=no Connection=close Content-Encoding=gzip Vary=Accept-Encoding"},
		{"no summary", false, func(_ func() bool, _ dom.Emit) (*pyjson.Object, error) { return nil, nil }, nil,
			"X-Accel-Buffering=no Connection=close"},
	}
	for _, tc := range cases {
		c := &apitest.Ctx{Q: pyurl.Values{"sid": {"s"}}}
		if tc.gzip {
			c.Headers = map[string]string{"Accept-Encoding": "gzip"}
		}
		if err := Stream(c, "node", tc.run); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		body := c.StreamBody.Bytes()
		if tc.gzip {
			zr, err := gzip.NewReader(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			body, _ = io.ReadAll(zr)
		}
		var got []string
		for _, l := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
			if l != "" {
				got = append(got, l)
			}
		}
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: lines %q, want %q", tc.name, got, tc.want)
		}
		var hs []string
		for _, kv := range c.StreamExtra {
			hs = append(hs, kv[0]+"="+kv[1])
		}
		if strings.Join(hs, " ") != tc.headers || c.StreamType != "application/x-ndjson; charset=utf-8" || !c.Detached {
			t.Errorf("%s: headers %q type %q detached %v", tc.name, hs, c.StreamType, c.Detached)
		}
	}
}
