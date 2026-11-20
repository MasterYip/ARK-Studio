package sessions

import (
	"os"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestRenameAndAutoTitle(t *testing.T) {
	m := streamManager(t)
	// No first message: the process starts and waits; no title yet.
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", ""), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, s)
	if metaOf(m, s, "title") != "" || metaOf(m, s, "turn_state") != "waiting" {
		t.Fatalf("empty start: title %q turn %v", metaOf(m, s, "title"), metaOf(m, s, "turn_state"))
	}
	if m.Public(s, false).Value("status") != "idle" {
		t.Fatal("an empty session waits for its first message")
	}
	// The first message names it.
	if _, err := m.Steer(nil, s.ID, "\n  Survey the reward papers\nand more", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first turn", func() bool { return strings.Contains(logText(m, s), "echo: Survey the reward papers") })
	if metaOf(m, s, "title") != "Survey the reward papers" {
		t.Fatalf("auto title: %q", metaOf(m, s, "title"))
	}
	// Rename; an empty title returns to the default; a later message does not rename it again.
	pub, err := m.Rename(s.ID, "  HOI   survey ")
	if err != nil || pub.Value("title") != "HOI survey" {
		t.Fatalf("rename: %v %v", pub.Value("title"), err)
	}
	waitFor(t, "waiting", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if _, err := m.Steer(nil, s.ID, "another", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second", func() bool { return strings.Contains(logText(m, s), "echo: another") })
	if metaOf(m, s, "title") != "HOI survey" {
		t.Fatal("a renamed session keeps its name")
	}
	if pub, _ := m.Rename(s.ID, "   "); pub.Value("title") != "" {
		t.Fatalf("empty rename: %q", pub.Value("title"))
	}
	if _, err := m.Rename("s_missing00000", "x"); err == nil {
		t.Error("unknown session")
	}
}

func TestEmptyStartOneShot(t *testing.T) {
	m := streamManager(t)
	t.Setenv("ARK_CLAUDE_STREAM", "0") // one-shot turns (like Codex)
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", ""), "http://127.0.0.1:1", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if m.IsActive(s) || metaOf(m, s, "awaiting_first") != true {
		t.Fatalf("one-shot empty start: active=%v awaiting=%v", m.IsActive(s), metaOf(m, s, "awaiting_first"))
	}
	if _, err := m.ContinueSession(nil, s.ID, "Plan the ablation", ContinueOptions{Actor: "bob"}); err != nil {
		t.Fatal(err)
	}
	defer killSession(m, s)
	// The first message is the prompt itself, not a correction of a turn.
	cmd := strOr(metaOf(m, s, "command_preview"))
	if strings.Contains(cmd, "Previous output") || strings.Contains(cmd, "User correction") {
		t.Fatalf("first message composed as a correction: %s", cmd)
	}
	if metaOf(m, s, "title") != "Plan the ablation" {
		t.Fatalf("title from the first message: %q", metaOf(m, s, "title"))
	}
}

// After a `result` the session waits, also while a background task runs;
// the agent speaking on its own (woken by a notification) starts a turn.
func TestStreamTurnStateWithBackgroundWork(t *testing.T) {
	m := streamManager(t)
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hello"), "http://127.0.0.1:1", "carol")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, s)
	waitFor(t, "waiting", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	m.observeStreamLine(s, `{"type":"system","subtype":"task_started","task_id":"bash_1","tool_use_id":"t9","is_backgrounded":true,"session_id":"x","uuid":"a"}`)
	if metaOf(m, s, "turn_state") != "waiting" {
		t.Fatal("a background task does not start a turn")
	}
	page, err := m.TranscriptPage(s.ID, TranscriptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range objects(page.Value("items")) {
		if pyjson.Truthy(it.Value("streaming")) {
			t.Fatalf("nothing streams while it waits: %v", it)
		}
	}
	m.observeStreamLine(s, `{"type":"assistant","message":{"content":[{"type":"text","text":"The watcher reported a comment."}]},"parent_tool_use_id":null,"session_id":"x","uuid":"b"}`)
	if metaOf(m, s, "turn_state") != "running" || m.Public(s, false).Value("status") == "idle" {
		t.Fatal("the agent speaking starts a turn")
	}
	m.observeStreamLine(s, `{"type":"assistant","message":{"content":[]},"parent_tool_use_id":"toolu_x","session_id":"x","uuid":"c"}`)
	m.observeStreamLine(s, `{"type":"result","subtype":"success","result":"","session_id":"x","uuid":"d"}`)
	if metaOf(m, s, "turn_state") != "waiting" {
		t.Fatal("the result ends that turn")
	}
	_ = os.Getenv
}
