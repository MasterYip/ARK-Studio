package pyos

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestAgainstCPython(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Shlex []struct {
			Text      string
			Posix, Nt []string
			PosixErr  string `json:"posix_err"`
			NtErr     string `json:"nt_err"`
		}
		Cmdline []struct {
			Args []string
			Line string
		}
		Capitalize []struct{ In, Out string }
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	check := func(posix bool, want []string, wantErr, text string) {
		got, err := ShlexSplit(text, posix)
		gotErr := ""
		if err != nil {
			gotErr = err.Error()
		}
		if len(got) == 0 {
			got = nil
		}
		if len(want) == 0 {
			want = nil
		}
		if gotErr != wantErr || (wantErr == "" && !reflect.DeepEqual(got, want)) {
			t.Errorf("ShlexSplit(%q, %v) = %q %q; want %q %q", text, posix, got, gotErr, want, wantErr)
		}
	}
	for _, c := range doc.Shlex {
		check(true, c.Posix, c.PosixErr, c.Text)
		check(false, c.Nt, c.NtErr, c.Text)
	}
	for _, c := range doc.Cmdline {
		if got := List2Cmdline(c.Args); got != c.Line {
			t.Errorf("List2Cmdline(%q) = %q; want %q", c.Args, got, c.Line)
		}
	}
	for _, c := range doc.Capitalize {
		if got := Capitalize(c.In); got != c.Out {
			t.Errorf("Capitalize(%q) = %q; want %q", c.In, got, c.Out)
		}
	}
	t.Logf("%d shlex, %d cmdline, %d capitalize vectors", len(doc.Shlex), len(doc.Cmdline), len(doc.Capitalize))
}
