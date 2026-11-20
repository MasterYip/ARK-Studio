package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func appendLog(t *testing.T, dir string, entries []*pyjson.Object) {
	t.Helper()
	fh, err := os.OpenFile(filepath.Join(dir, "stdout.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, e := range entries {
		if _, err := fh.Write(append(pyjson.Dumps(e), '\n')); err != nil {
			t.Fatal(err)
		}
	}
}

func itemsJSON(o *pyjson.Object) string {
	return string(pyjson.Dumps(o.Value("items")))
}

// The cached builder fed in steps gives the transcript a full rebuild gives.
func TestTranscriptCacheIncremental(t *testing.T) {
	all := streamFixture(t, "claude_stream_subagents.jsonl")
	dir := t.TempDir()
	m := &Manager{}
	s := &Session{ID: "s1", Folder: dir}
	in := TranscriptInput{SessionID: "s1", Status: "running", Prompt: "Do it", Platform: "claude"}
	for _, cut := range []int{5, 17, 30, len(all)} {
		start := 0
		if c := m.tcache.m["s1"]; c != nil {
			start = c.outN
		}
		appendLog(t, dir, all[start:cut])
		got, _ := m.cachedTranscript(s, in, "")
		want := BuildTranscript(TranscriptInput{SessionID: "s1", Status: "running", Prompt: "Do it", Platform: "claude", Stdout: all[:cut]})
		if itemsJSON(got) != itemsJSON(want) {
			t.Fatalf("after %d lines the cached transcript differs:\n%s\n---\n%s", cut, itemsJSON(got), itemsJSON(want))
		}
		if got.Value("signature") != want.Value("signature") {
			t.Fatalf("signature after %d lines: %v vs %v", cut, got.Value("signature"), want.Value("signature"))
		}
	}
	// A half-written line waits for its newline.
	fh, _ := os.OpenFile(filepath.Join(dir, "stdout.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = fh.WriteString(`{"t": "2026-11-19T10:01:00+00:00", "line": "partial`)
	_ = fh.Close()
	again, _ := m.cachedTranscript(s, in, "")
	if itemsJSON(again) != itemsJSON(BuildTranscript(TranscriptInput{SessionID: "s1", Status: "running", Prompt: "Do it", Platform: "claude", Stdout: all})) {
		t.Fatal("a partial line changed the transcript")
	}
	// A replaced (shorter) log starts over.
	if err := os.WriteFile(filepath.Join(dir, "stdout.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	appendLog(t, dir, all[:3])
	reset, _ := m.cachedTranscript(s, in, "")
	if itemsJSON(reset) != itemsJSON(BuildTranscript(TranscriptInput{SessionID: "s1", Status: "running", Prompt: "Do it", Platform: "claude", Stdout: all[:3]})) {
		t.Fatal("a shorter log was not rebuilt")
	}
	// Item lookup.
	appendLog(t, dir, all[3:])
	if _, item := m.cachedTranscript(s, in, "toolu_A"); item == nil || item.Value("name") != "Task" {
		t.Fatalf("item lookup: %v", item)
	}
}

func TestShapeTranscript(t *testing.T) {
	data := BuildTranscript(TranscriptInput{SessionID: "s1", Status: "running", Prompt: "Do it", Platform: "claude",
		Stdout: streamFixture(t, "claude_stream_subagents.jsonl")})
	sig := strOr(data.Value("signature"))
	if got := shapeTranscript(data, nil, TranscriptOptions{Known: sig}); got.Value("unchanged") != true || got.Has("items") {
		t.Errorf("known signature: %v", got)
	}
	if got := shapeTranscript(data, nil, TranscriptOptions{Known: "old"}); !got.Has("items") {
		t.Error("a changed transcript is sent")
	}
	page := shapeTranscript(data, nil, TranscriptOptions{Tail: 3})
	items := page.Value("items").([]any)
	total := len(objects(data.Value("items")))
	if len(items) != 3 || page.Value("offset") != int64(total-3) || page.Value("total_items") != int64(total) {
		t.Fatalf("tail page: %d items, offset %v", len(items), page.Value("offset"))
	}
	var ids []string
	for _, x := range page.Value("activity").([]any) {
		ids = append(ids, strOr(x.(*pyjson.Object).Value("id")))
	}
	// Sub-agents, background commands and the KillShell call; not the foreground command or the Read.
	if got := strings.Join(ids, ","); got != "toolu_A,toolu_B,toolu_C,toolu_D,toolu_E,toolu_F" {
		t.Errorf("activity items: %s", got)
	}
	if len(objects(data.Value("items"))) != total {
		t.Error("shaping does not change the cached transcript")
	}
}
