package sessions

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func TestAgentTranscriptView(t *testing.T) {
	all := streamFixture(t, "claude_stream_subagents.jsonl")
	dir := t.TempDir()
	appendLog(t, dir, all)
	m := &Manager{}
	s := &Session{ID: "s1", Folder: dir}
	data, _ := m.cachedTranscriptFor(s, TranscriptInput{SessionID: "s1", Status: "stopped", Platform: "claude"}, "", "toolu_A")
	var kinds []string
	for _, x := range data.Value("items").([]any) {
		it := x.(*pyjson.Object)
		kinds = append(kinds, strOr(it.Value("type"))+":"+strOr(it.Value("role"))+strOr(it.Value("name"))+strOr(it.Value("level")))
	}
	got := strings.Join(kinds, " ")
	// Its prompt, its reasoning, its Grep call and its report; nothing of the main thread.
	if got != "message:user notice:reasoning tool:Grep message:assistant" {
		t.Fatalf("sub-agent view: %s", got)
	}
}

func TestAttachMediaAndUpload(t *testing.T) {
	b := newTranscriptBuilder(TranscriptInput{Prompt: "look"})
	b.attachMedia(`["uploads/a.png"]`)
	b.attachMedia(`["uploads/b.png"]`)
	b.attachMedia(`not json`)
	if imgs := b.items[0].Value("images").([]any); len(imgs) != 2 || imgs[1] != "uploads/b.png" {
		t.Fatalf("images on the user turn: %v", imgs)
	}
	m := &Manager{}
	s := &Session{ID: "s1", Folder: t.TempDir()}
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nfake"))
	rel, err := m.saveUpload(s, "../../evil name.png", "data:image/png;base64,"+png)
	if err != nil || !strings.HasPrefix(rel, "uploads/") || strings.Contains(rel, "..") || strings.Contains(rel, " ") {
		t.Fatalf("upload path: %q %v", rel, err)
	}
	if blob, err := os.ReadFile(filepath.Join(s.Workspace(), filepath.FromSlash(rel))); err != nil || !strings.HasSuffix(string(blob), "fake") {
		t.Fatalf("saved file: %v", err)
	}
	if _, err := m.saveUpload(s, "x.exe", png); err == nil {
		t.Error("only images")
	}
	big := base64.StdEncoding.EncodeToString(make([]byte, maxUploadBytes+10))
	if _, err := m.saveUpload(s, "x.png", big); err == nil || !strings.Contains(err.Error(), "MB") {
		t.Errorf("size limit: %v", err)
	}
	entries, err := m.contextEntries(nil, s, []any{pyjson.NewObject("kind", "image_data", "name", "shot.png", "data", png)})
	if err != nil || len(entries) != 1 || entries[0].Value("kind") != "media" || !strings.Contains(strOr(entries[0].Value("text")), "- file: ") {
		t.Fatalf("image_data context: %v %v", entries, err)
	}
}

func TestSendSteerNowChecks(t *testing.T) {
	m := &Manager{sessions: map[string]*Session{}}
	id := "s_20261119120000abcdef"
	s := &Session{ID: id, Folder: t.TempDir(), m: m, Meta: pyjson.NewObject("id", id, "status", "running",
		"steer_queue", []any{pyjson.NewObject("id", "st_1", "text", "a", "status", "delivered"),
			pyjson.NewObject("id", "st_2", "text", "b", "status", "queued")})}
	m.sessions[id] = s
	if _, err := m.SendSteerNow(nil, id, "st_x", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("unknown item: %v", err)
	}
	if _, err := m.SendSteerNow(nil, id, "st_1", ""); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("delivered item: %v", err)
	}
	// KeepQueue: a continue that sends one message leaves the others queued.
	m.mu.Lock()
	drained := m.drainSteerLocked(s)
	m.mu.Unlock()
	if len(drained) != 1 || drained[0] != "b" {
		t.Errorf("drain sends queued messages in order: %v", drained)
	}
}
