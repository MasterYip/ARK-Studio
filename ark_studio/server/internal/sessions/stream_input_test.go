package sessions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkstudio/go/internal/compat/pyjson"
)

// A fake `claude` that speaks stream-json: the test binary itself, started
// with ARK_FAKE_CLAUDE=1 (see TestMain).
//
//	a user message "slow …" runs for a while; messages that arrive meanwhile
//	are taken into that turn; "crash" exits with code 2; a control_request
//	interrupt ends the running turn; EOF on stdin exits 0.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "session-relay" { // the relay of a relayed session (relay.go)
		os.Exit(RelayMain(os.Args[2:]))
	}
	if os.Getenv("ARK_FAKE_CLAUDE") == "1" {
		fakeClaude()
		return
	}
	os.Exit(m.Run())
}

func fakeClaude() {
	args := strings.Join(os.Args, " ")
	if os.Getenv("ARK_FAKE_CLAUDE_OLD") == "1" && strings.Contains(args, "--input-format") {
		fmt.Fprintln(os.Stderr, "error: unknown option '--input-format'")
		os.Exit(1)
	}
	native := "fake-native-0001"
	for i, a := range os.Args {
		if a == "--resume" && i+1 < len(os.Args) {
			native = os.Args[i+1]
		}
	}
	n := 0
	out := func(v map[string]any) {
		n++
		v["session_id"] = native
		v["uuid"] = fmt.Sprintf("u%d", n)
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	say := func(text string) {
		out(map[string]any{"type": "assistant", "message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": text}}}})
	}
	result := func(sub string) {
		out(map[string]any{"type": "result", "subtype": sub, "result": "", "is_error": sub != "success"})
	}
	out(map[string]any{"type": "system", "subtype": "init"})
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	describe := func(line string) (string, int, string) {
		var msg struct {
			Type    string `json:"type"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			return "", 0, "bad"
		}
		if msg.Type == "control_request" {
			return "", 0, msg.Request.Subtype
		}
		text, images := "", 0
		for _, c := range msg.Message.Content {
			if c.Type == "text" {
				text += c.Text
			}
			if c.Type == "image" {
				images++
			}
		}
		return text, images, msg.Type
	}
	for line := range lines {
		text, images, kind := describe(line)
		if kind != "user" {
			continue
		}
		if strings.Contains(text, "crash") {
			os.Exit(2)
		}
		say(fmt.Sprintf("echo: %s (images: %d)", firstLine(text), images))
		if strings.HasPrefix(text, "start background") { // a background command and the ARK watcher outlive the turn
			for _, bg := range [][2]string{{"sh_1", "npm run build"}, {"sh_w", "sh ark-watch.sh"}} {
				id := "toolu_" + bg[0]
				out(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{
					"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": bg[1], "run_in_background": true}}}}})
				out(map[string]any{"type": "system", "subtype": "task_started", "task_id": bg[0], "tool_use_id": id,
					"description": bg[1], "task_type": "local_bash", "is_backgrounded": true})
				out(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": id, "content": "Command running in background with ID: " + bg[0]}}}})
			}
			result("success")
			continue
		}
		if strings.HasPrefix(text, "finish background") {
			out(map[string]any{"type": "system", "subtype": "task_notification", "task_id": "sh_1", "tool_use_id": "toolu_sh_1",
				"status": "completed", "summary": "Build finished"})
			result("success")
			continue
		}
		if !strings.HasPrefix(text, "slow") {
			result("success")
			continue
		}
		deadline := time.After(8 * time.Second)
	turn:
		for {
			select {
			case next, ok := <-lines:
				if !ok {
					os.Exit(0)
				}
				t, _, k := describe(next)
				if k == "interrupt" {
					say("interrupted")
					result("error_during_execution")
					break turn
				}
				if k == "user" {
					say("mid-turn: " + firstLine(t))
				}
			case <-deadline:
				say("slow turn done")
				result("success")
				break turn
			}
		}
	}
}

func firstLine(s string) string {
	return strings.SplitN(strings.TrimSpace(s), "\n", 2)[0]
}

func streamManager(t *testing.T) *Manager {
	t.Helper()
	m, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	m.Presets = []*pyjson.Object{pyjson.NewObject("id", "claude", "label", "Claude Code", "available", true,
		"command", []any{exe, "-p", "{prompt}"}, "prompt_mode", "arg", "followup", false)}
	t.Setenv("ARK_FAKE_CLAUDE", "1")
	t.Cleanup(func() { killAll(m) })
	return m
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func logText(m *Manager, s *Session) string {
	var out []string
	for _, e := range m.allLogEntries(s, "stdout") {
		out = append(out, strOr(e.Value("line")))
	}
	return strings.Join(out, "\n")
}

func metaOf(m *Manager, s *Session, key string) any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return s.Meta.Value(key)
}

// killSession ends the session's process and waits until its monitor has
// finished the session (status, meta.json, logs): nothing writes to the
// test's temp dir afterwards.
func killSession(m *Manager, s *Session) {
	m.mu.Lock()
	p := s.proc
	m.mu.Unlock()
	if p == nil {
		return
	}
	m.terminate(p)
	select {
	case <-p.done:
	case <-time.After(30 * time.Second):
		panic("session monitor did not finish within 30 s of the kill")
	}
}

// killAll ends every session's process (a test cleanup: runs before the
// TempDir removal registered earlier).
func killAll(m *Manager) {
	m.mu.Lock()
	var all []*Session
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.Unlock()
	for _, s := range all {
		killSession(m, s)
	}
}

func TestClaudeStreamSession(t *testing.T) {
	m := streamManager(t)
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hello"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, s)
	if !isStream(s) {
		t.Fatal("a print-mode Claude Code session runs long-lived")
	}
	if cmd := strOr(metaOf(m, s, "command_preview")); !strings.Contains(cmd, "--input-format stream-json") || strings.Contains(cmd, "hello") {
		t.Fatalf("argv: %s", cmd)
	}
	// The first turn ends with a result; the process waits for the next message.
	waitFor(t, "first turn", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if !strings.Contains(logText(m, s), "echo: hello") {
		t.Fatal(logText(m, s))
	}
	pub := m.Public(s, false)
	if pub.Value("status") != "idle" || !pyjson.Truthy(pub.Value("capabilities").(*pyjson.Object).Value("stream_input")) {
		t.Fatalf("waiting session: %v %v", pub.Value("status"), pub.Value("capabilities"))
	}
	// A message while it waits goes to stdin at once.
	if _, err := m.Steer(nil, s.ID, "second", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second turn", func() bool { return strings.Contains(logText(m, s), "echo: second") })
	waitFor(t, "waiting again", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	// A slow turn: Enter queues, "Send now" injects into the running turn.
	if _, err := m.Steer(nil, s.ID, "slow work", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "slow turn", func() bool { return strings.Contains(logText(m, s), "echo: slow work") })
	queued, err := m.Steer(nil, s.ID, "after the turn", nil)
	if err != nil || queued.Value("steer").(*pyjson.Object).Value("status") != "queued" {
		t.Fatalf("Enter during a turn queues: %v %v", queued, err)
	}
	now, err := m.Steer(nil, s.ID, "right now", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.SendSteerNow(nil, s.ID, pyjson.Str(now.Value("steer").(*pyjson.Object).Value("id")), "alice")
	if err != nil || res.Value("mode") != "stdin" {
		t.Fatalf("send now: %v %v", res, err)
	}
	waitFor(t, "mid-turn injection", func() bool { return strings.Contains(logText(m, s), "mid-turn: right now") })
	if strings.Contains(logText(m, s), "echo: after the turn") {
		t.Fatal("the queued message waits for the turn to end")
	}
	// The turn ends; the queued message starts the next one.
	waitFor(t, "queued message delivered", func() bool { return strings.Contains(logText(m, s), "echo: after the turn") })
	// Stop during a turn interrupts it; the process stays.
	waitFor(t, "waiting", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if _, err := m.Steer(nil, s.ID, "slow again", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "slow again", func() bool { return strings.Contains(logText(m, s), "echo: slow again") })
	if _, err := m.RequestStop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "interrupted", func() bool { return strings.Contains(logText(m, s), "interrupted") })
	waitFor(t, "waiting after the interrupt", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if !m.IsActive(s) {
		t.Fatal("an interrupt keeps the process")
	}
	// Stop while it waits ends the process cleanly.
	if _, err := m.RequestStop(s.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stopped", func() bool { return !m.IsActive(s) })
	if metaOf(m, s, "status") != "stopped" || metaOf(m, s, "native_session_id") != "fake-native-0001" {
		t.Fatalf("after stop: %v %v", metaOf(m, s, "status"), metaOf(m, s, "native_session_id"))
	}
	// The next message resumes the same conversation in a new process.
	if _, err := m.ContinueSession(nil, s.ID, "welcome back", ContinueOptions{Actor: "alice"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed turn", func() bool { return strings.Contains(logText(m, s), "echo: welcome back") })
	if !pyjson.Truthy(metaOf(m, s, "resumed_natively_at")) {
		t.Fatal("the session resumes with --resume")
	}
}

func TestClaudeStreamImagesCrashAndOldCLI(t *testing.T) {
	m := streamManager(t)
	png := "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAYAAABytg0kAAAAFElEQVR4nGP8z8DwnwEKmBgYGBgAJAABBCBlDwAAAABJRU5ErkJggg=="
	// A new session may start with an image only.
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "",
		"context", []any{pyjson.NewObject("kind", "image_data", "name", "shot.png", "data", png)}), "http://127.0.0.1:1", "bob")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "image turn", func() bool { return strings.Contains(logText(m, s), "(images: 1)") })
	if _, err := os.Stat(filepath.Join(s.Workspace(), "uploads")); err != nil {
		t.Error("the pasted image is kept in the workspace")
	}
	if !strings.Contains(logText(m, s), `["uploads/`) {
		t.Error("the turn records its image")
	}
	// A crash ends the session with an error; the next message resumes it.
	waitFor(t, "waiting", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if _, err := m.Steer(nil, s.ID, "crash now", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "crashed", func() bool { return !m.IsActive(s) })
	if metaOf(m, s, "status") != "errored" {
		t.Fatalf("crash: %v", metaOf(m, s, "status"))
	}
	if _, err := m.ContinueSession(nil, s.ID, "again", ContinueOptions{Actor: "bob"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "resumed", func() bool { return strings.Contains(logText(m, s), "echo: again") })
	killSession(m, s)
	waitFor(t, "ended", func() bool { return !m.IsActive(s) })

	// An older CLI rejects the stream flags: stream mode turns off and the
	// next session runs one-shot turns.
	t.Setenv("ARK_FAKE_CLAUDE_OLD", "1")
	old, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hi"), "http://127.0.0.1:1", "carol")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "old CLI exits", func() bool { return !m.IsActive(old) })
	if m.streamEnabled() || !strings.Contains(strOr(metaOf(m, old, "error")), "stream input") {
		t.Fatalf("fallback: enabled=%v error=%v", m.streamEnabled(), metaOf(m, old, "error"))
	}
	next, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "plain"), "http://127.0.0.1:1", "carol")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, next)
	if isStream(next) || strings.Contains(strOr(metaOf(m, next, "command_preview")), "--input-format") {
		t.Fatal("one-shot turns after the fallback")
	}
}

func TestClaudeStreamLimits(t *testing.T) {
	m := streamManager(t)
	old := StreamMaxPerUser
	StreamMaxPerUser = 1
	defer func() { StreamMaxPerUser = old }()
	a, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "one"), "http://127.0.0.1:1", "dave")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, a)
	waitFor(t, "a waits", func() bool { return metaOf(m, a, "turn_state") == "waiting" })
	b, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "two"), "http://127.0.0.1:1", "dave")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, b)
	waitFor(t, "a closed to make room", func() bool { return !m.IsActive(a) })
	if !isStream(b) || metaOf(m, a, "status") != "stopped" || !strings.Contains(strOr(metaOf(m, a, "notice")), "next message resumes") {
		t.Fatalf("limits: b stream=%v a=%v %v", isStream(b), metaOf(m, a, "status"), metaOf(m, a, "notice"))
	}
	if _, ok := claudeStreamArgv([]string{"claude", "-p", "--output-format", "json", "x"}, "x"); ok {
		t.Error("a command with another output format keeps one-shot turns")
	}
	if got, ok := claudeStreamArgv([]string{"claude", "-p", "do it"}, "do it"); !ok || strings.Join(got, " ") != "claude --input-format stream-json --output-format stream-json --verbose -p" {
		t.Errorf("stream argv: %v", got)
	}
}

// Between turns, a running background command keeps the session busy (the
// ARK watcher does not); the public record says so until it finishes.
func TestClaudeStreamBusyBetweenTurns(t *testing.T) {
	m := streamManager(t)
	s, err := m.Start(nil, pyjson.NewObject("platform", "claude", "prompt", "hello"), "http://127.0.0.1:1", "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer killSession(m, s)
	waitFor(t, "first turn", func() bool { return metaOf(m, s, "turn_state") == "waiting" })
	if m.Public(s, false).Value("busy") != nil {
		t.Fatal("nothing runs yet")
	}
	if _, err := m.Steer(nil, s.ID, "start background", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "busy", func() bool {
		b, _ := m.Public(s, false).Value("busy").(*pyjson.Object)
		return metaOf(m, s, "turn_state") == "waiting" && b != nil
	})
	pub := m.Public(s, false)
	b := pub.Value("busy").(*pyjson.Object)
	if pub.Value("status") != "idle" || b.Value("background") != int64(1) || b.Value("subagents") != int64(0) {
		t.Fatalf("busy: %v %v (the watcher does not count)", pub.Value("status"), b)
	}
	if got := m.Busy(s); got.Background != 1 {
		t.Fatalf("Busy: %+v", got)
	}
	if _, err := m.Steer(nil, s.ID, "finish background", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "quiet", func() bool {
		return metaOf(m, s, "turn_state") == "waiting" && m.Public(s, false).Value("busy") == nil &&
			strings.Contains(logText(m, s), "Build finished")
	})
}
