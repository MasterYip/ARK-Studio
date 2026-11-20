package sessions

import (
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

// An image in a tool result is counted on the item and served one at a time.
func TestToolResultImages(t *testing.T) {
	dir := t.TempDir()
	appendLog(t, dir, []*pyjson.Object{
		pyjson.NewObject("t", "2026-11-19T10:00:01+00:00", "line",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"shot.png"}}]},"session_id":"s","uuid":"u1"}`),
		pyjson.NewObject("t", "2026-11-19T10:00:02+00:00", "line",
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"a screenshot"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]},"session_id":"s","uuid":"u2"}`),
	})
	m := &Manager{}
	s := &Session{ID: "s1", Folder: dir}
	in := TranscriptInput{SessionID: "s1", Status: "running", Platform: "claude"}
	data, item := m.cachedTranscript(s, in, "t1")
	tool := objects(data.Value("items"))[0]
	if tool.Value("images") != int64(1) || tool.Value("output") != "a screenshot" {
		t.Fatalf("tool item: %v", tool)
	}
	if strings.Contains(string(pyjson.Dumps(data)), "AAAA") {
		t.Fatal("image data is not sent with the transcript")
	}
	got := shapeTranscript(data, item, TranscriptOptions{Item: "t1", Image: 1})
	if got.Value("image") != "data:image/png;base64,AAAA" {
		t.Fatalf("image: %v", got)
	}
	if shapeTranscript(data, item, TranscriptOptions{Item: "t1", Image: 2}).Value("image") != nil {
		t.Error("out of range")
	}
	_, item = m.cachedTranscript(s, in, "t1")
	if one := shapeTranscript(data, item, TranscriptOptions{Item: "t1"}).Value("item").(*pyjson.Object); one.Has("image_data") {
		t.Error("the item reply does not carry image data")
	}
}
