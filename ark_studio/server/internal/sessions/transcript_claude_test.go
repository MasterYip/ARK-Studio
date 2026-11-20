package sessions

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkstudio/go/internal/compat/pyjson"
)

func streamFixture(t *testing.T, name string) []*pyjson.Object {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []*pyjson.Object
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	i := 0
	for sc.Scan() {
		i++
		at := "2026-11-19T10:00:" + pad2(i) + "+00:00"
		out = append(out, pyjson.NewObject("t", at, "line", sc.Text()))
	}
	return out
}

func pad2(n int) string {
	return fmt.Sprintf("%02d", n)
}

func TestClaudeStreamSubagents(t *testing.T) {
	tr := BuildTranscript(TranscriptInput{SessionID: "s1", Status: "running", Prompt: "Do it", Platform: "claude",
		Stdout: streamFixture(t, "claude_stream_subagents.jsonl")})
	items := tr.Value("items").([]any)
	byID := map[string]*pyjson.Object{}
	var users, assistants []string
	for _, x := range items {
		it := x.(*pyjson.Object)
		byID[strOr(it.Value("id"))] = it
		switch {
		case it.Value("type") == "message" && it.Value("role") == "user":
			users = append(users, strOr(it.Value("text")))
		case it.Value("type") == "message":
			assistants = append(assistants, strOr(it.Value("text")))
		case it.Value("type") == "notice" && it.Value("level") != "reasoning":
			t.Errorf("bookkeeping or task event shown as a notice: %v", it.Value("text"))
		case it.Value("type") == "task":
			t.Errorf("every task in this stream has its call: %v", it)
		}
		if strings.Contains(strOr(it.Value("text")), `"type"`) {
			t.Errorf("raw JSON in the thread: %v", it.Value("text"))
		}
	}
	if len(users) != 1 || users[0] != "Do it" {
		t.Errorf("only the prompt is a user turn (sub-agent prompts are not): %q", users)
	}
	if got := assistants[len(assistants)-1]; got != "All done: tests pass." {
		t.Errorf("the closing result is not repeated: %q", got)
	}
	task := func(id string) *pyjson.Object {
		it := byID[id]
		if it == nil {
			t.Fatalf("no tool item %s", id)
		}
		tk, _ := it.Value("task").(*pyjson.Object)
		if tk == nil {
			t.Fatalf("tool %s has no task", id)
		}
		return tk
	}
	a := byID["toolu_A"]
	if a.Value("status") != "ok" || a.Value("children") != int64(1) || a.Value("last_output") != "session-model.js holds the helpers." {
		t.Fatalf("sub-agent A: %v", a)
	}
	if a.Value("child_tokens") != int64(2570) {
		t.Errorf("child tokens: %v", a.Value("child_tokens"))
	}
	if meta, _ := a.Value("meta").(*pyjson.Object); meta == nil || meta.Value("totalTokens") != int64(2570) {
		t.Errorf("tool_use_result meta: %v", a.Value("meta"))
	}
	if ta := task("toolu_A"); ta.Value("type") != "agent" || ta.Value("status") != "completed" || ta.Value("last") != "Searching for session" ||
		ta.Value("total_tokens") != int64(1230) || ta.Value("background") != false {
		t.Errorf("task A: %v", ta)
	}
	if byID["toolu_A1"] != nil {
		t.Error("a sub-agent's own tool calls stay out of the main thread")
	}
	if tb := task("toolu_B"); tb.Value("status") != "running" || byID["toolu_B"].Value("status") != "running" {
		t.Errorf("sub-agent B still runs: %v", tb)
	}
	if tg := task("toolu_G"); tg.Value("type") != "command" || tg.Value("background") != false || tg.Value("status") != "completed" {
		t.Errorf("foreground command task: %v", tg)
	}
	if tc := task("toolu_C"); tc.Value("id") != "bash_1" || tc.Value("background") != true || tc.Value("status") != "running" {
		t.Errorf("background command C (KillShell is applied by the client): %v", tc)
	}
	if td := task("toolu_D"); td.Value("background") != true || td.Value("status") != "completed" || !strings.Contains(strOr(td.Value("summary")), "exit code 0") {
		t.Errorf("command moved to the background: %v", td)
	}
	if tf := task("toolu_F"); tf.Value("id") != "agent_77" || tf.Value("status") != "failed" {
		t.Errorf("task-notification turn: %v", tf)
	}
	if h := byID["toolu_H"]; h == nil || !strings.HasPrefix(strOr(h.Value("output")), "The reward is 1 if xxx") {
		t.Errorf("a cut-off tool result is salvaged: %v", h)
	}
	if !strings.Contains(strOr(tr.Value("signature")), "|c") {
		t.Errorf("signature follows task progress: %v", tr.Value("signature"))
	}
	if usage := tr.Value("usage").(*pyjson.Object); usage.Value("source") != "reported" {
		t.Errorf("result usage still counts: %v", usage)
	}
	// The JS activity model is tested against this transcript.
	if os.Getenv("ARK_UPDATE_FIXTURES") == "1" {
		out := filepath.Join("..", "..", "..", "..", "tests", "js", "fixtures", "claude_subagents_transcript.json")
		if err := os.WriteFile(out, append(pyjson.Dumps(tr), 10), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaudeStreamOrphanTaskAndCaps(t *testing.T) {
	big := strings.Repeat("y", maxToolOutputChars+50)
	lines := []string{
		`{"type":"system","subtype":"task_notification","task_id":"bash_9","status":"completed","summary":"npm run watch","session_id":"s","uuid":"u1"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Write","input":{"file_path":"a","content":"` + big + `"}}]},"session_id":"s","uuid":"u2"}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"` + big + `"}]},"session_id":"s","uuid":"u3"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"unrelated cut`,
	}
	var stdout []*pyjson.Object
	for _, l := range lines {
		stdout = append(stdout, pyjson.NewObject("t", "2026-11-19T10:00:00+00:00", "line", l))
	}
	tr := BuildTranscript(TranscriptInput{SessionID: "s", Status: "running", Platform: "claude", Stdout: stdout})
	items := tr.Value("items").([]any)
	if len(items) != 2 {
		t.Fatalf("an orphan task and the tool, nothing for the cut assistant line: %d", len(items))
	}
	orphan := items[0].(*pyjson.Object)
	if orphan.Value("type") != "task" || orphan.Value("task").(*pyjson.Object).Value("status") != "completed" {
		t.Errorf("orphan task: %v", orphan)
	}
	tool := items[1].(*pyjson.Object)
	if tool.Value("output_truncated") != true || tool.Value("output_chars") != int64(maxToolOutputChars+50) {
		t.Errorf("output capped: %v %v", tool.Value("output_truncated"), tool.Value("output_chars"))
	}
	if c := strOr(tool.Value("input").(*pyjson.Object).Value("content")); len(c) > maxToolInputString+20 {
		t.Errorf("long input string capped: %d", len(c))
	}
}

func TestClaudeStreamArgs(t *testing.T) {
	if got := claudeStreamArgs([]string{"claude", "-p", "x"}); strings.Join(got, " ") != "--output-format stream-json --verbose" {
		t.Errorf("print mode: %v", got)
	}
	if got := claudeStreamArgs([]string{"claude", "-p", "--output-format", "text", "x"}); got != nil {
		t.Errorf("explicit format kept: %v", got)
	}
	if got := claudeStreamArgs([]string{"claude", "x"}); got != nil {
		t.Errorf("interactive mode untouched: %v", got)
	}
}
