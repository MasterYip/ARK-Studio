package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 关闭 / End and 删除 / Delete from the session menu: a closed session leaves
// the nudger's list; Delete keeps the transcript (archive) unless asked.
func TestSessionCloseArchiveDeleteAndNudges(t *testing.T) {
	app, _ := localGPUApp(t)
	start := func(title string) string {
		r := gpuCall(t, app, "POST", "/api/agent-sessions", map[string]any{"platform": "echo", "title": title, "prompt": "hello"}, nil)
		if r.code != 200 {
			t.Fatalf("start: %d %s", r.code, r.raw)
		}
		return r.body["session"].(map[string]any)["id"].(string)
	}
	nudged := func(id string) bool {
		for _, s := range app.NudgeDeps().Sessions() {
			if s.ID == id {
				return true
			}
		}
		return false
	}
	waitStopped := func(id string) {
		deadline := time.Now().Add(20 * time.Second)
		for {
			s, _ := app.Sessions.Get(id)
			if s == nil || !app.Sessions.IsActive(s) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s still runs", id)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	a := start("To close")
	if !nudged(a) {
		t.Fatal("a running ARK session can be nudged")
	}
	r := gpuCall(t, app, "PATCH", "/api/agent-sessions/"+a, map[string]any{"closed": true}, nil)
	if r.code != 200 || r.body["session"].(map[string]any)["closed"] != true {
		t.Fatalf("close: %d %s", r.code, r.raw)
	}
	waitStopped(a)
	if nudged(a) {
		t.Fatal("a closed session is never nudged or resumed")
	}
	// A person's next message reopens it.
	if r = gpuCall(t, app, "POST", "/api/agent-sessions/"+a+"/continue", map[string]any{"text": "again"}, nil); r.code != 200 {
		t.Fatalf("continue: %d %s", r.code, r.raw)
	}
	if s, _ := app.Sessions.Get(a); app.Sessions.NudgeExcluded(s) {
		t.Fatal("continuing reopens the session")
	}
	// Stop by a person: ARK leaves it alone afterwards.
	if r = gpuCall(t, app, "POST", "/api/agent-sessions/"+a+"/stop", map[string]any{}, nil); r.code != 200 {
		t.Fatalf("stop: %d %s", r.code, r.raw)
	}
	waitStopped(a)
	if nudged(a) {
		t.Fatal("a session a person stopped is not resumed by ARK")
	}

	// Delete, keeping the transcript: a running session is ended, then archived.
	b := start("To archive")
	sb, _ := app.Sessions.Get(b)
	root := filepath.Dir(sb.Folder)
	// stdout.jsonl appears with the first output line; on a busy machine the
	// echo run may not have written it yet.
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(filepath.Join(sb.Folder, "stdout.jsonl")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the echo session writes stdout.jsonl")
		}
	}
	if r = gpuCall(t, app, "POST", "/api/agent-sessions/"+b+"/archive", map[string]any{"end": true}, nil); r.code != 200 {
		t.Fatalf("archive: %d %s", r.code, r.raw)
	}
	if _, err := app.Sessions.Get(b); err == nil {
		t.Fatal("an archived session leaves the list")
	}
	if _, err := os.Stat(filepath.Join(root, ".archived", b, "stdout.jsonl")); err != nil {
		t.Fatalf("the transcript is kept: %v", err)
	}
	// Delete with the files.
	c := start("To delete")
	sc, _ := app.Sessions.Get(c)
	if r = gpuCall(t, app, "DELETE", "/api/agent-sessions/"+c+"?end=1", nil, nil); r.code != 200 {
		t.Fatalf("delete: %d %s", r.code, r.raw)
	}
	if _, err := os.Stat(sc.Folder); !os.IsNotExist(err) {
		t.Fatalf("files deleted: %v", err)
	}
	// Without ?end=1 a running session is not deleted (as before).
	d := start("Still running")
	defer func() { _, _ = app.Sessions.Close(d) }()
	if r = gpuCall(t, app, "DELETE", "/api/agent-sessions/"+d, nil, nil); r.code == 200 {
		t.Fatal("deleting a running session without end is refused")
	}
}
