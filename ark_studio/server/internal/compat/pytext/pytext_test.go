package pytext

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAgainstPythonVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Excerpts []struct {
			Body, Excerpt, MD string
			Limit             int    `json:"limit"`
			MDLimit           int    `json:"md_limit"`
			MDCut             bool   `json:"md_cut"`
			NoteStripped      string `json:"note_stripped"`
		}
		Ints []struct {
			In, Error   string
			OK, IsDigit bool
			Value       int64
		}
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	fails := 0
	for _, v := range data.Excerpts {
		if got := Excerpt(v.Body, v.Limit); got != v.Excerpt {
			fails++
			if fails < 10 {
				t.Errorf("Excerpt(%q, %d)\n got %q\nwant %q", v.Body, v.Limit, got, v.Excerpt)
			}
		}
		md, cut := MarkdownExcerpt(v.Body, v.MDLimit)
		if md != v.MD || cut != v.MDCut {
			fails++
			if fails < 10 {
				t.Errorf("MarkdownExcerpt(%q, %d)\n got %q %v\nwant %q %v", v.Body, v.MDLimit, md, cut, v.MD, v.MDCut)
			}
		}
		if got := NoteLineRe.ReplaceAllString(v.Body, ""); got != v.NoteStripped {
			fails++
			if fails < 10 {
				t.Errorf("NoteLine(%q)\n got %q\nwant %q", v.Body, got, v.NoteStripped)
			}
		}
	}
	for _, v := range data.Ints {
		n, err := Int(v.In)
		if v.OK && (err != nil || n != v.Value) {
			t.Errorf("Int(%q) = %d, %v; want %d", v.In, n, err, v.Value)
		}
		if !v.OK && (err == nil || err.Error() != v.Error) {
			t.Errorf("Int(%q) error %v; want %q", v.In, err, v.Error)
		}
		if IsDigit(v.In) != v.IsDigit {
			t.Errorf("IsDigit(%q) = %v", v.In, !v.IsDigit)
		}
	}
	if fails > 0 {
		t.Fatalf("%d excerpt mismatches of %d", fails, len(data.Excerpts)*3)
	}
}
