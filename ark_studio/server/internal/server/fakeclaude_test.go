package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// fakeClaudeStream is a long-lived Claude Code process (claude -p
// --input-format stream-json) for the nudge tests: every user message on
// stdin is one turn that echoes its first line and ends with a result.
//
//   - "start background" leaves a background command running after the turn;
//   - "start two <x>" leaves two (train<x>, eval<x>);
//   - "finish quietly <x>|<file>" ends the turn, then reports both finished
//     while it waits, without taking them up (their output in <file>);
//   - "finish wake <x>" reports them finished and takes them up itself with
//     a turn of its own (as Claude Code does);
//   - "slow" turns take two seconds.
func fakeClaudeStream() {
	n := 0
	var mu sync.Mutex
	out := func(v map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		n++
		v["session_id"] = "fake-nudge-0001"
		v["uuid"] = fmt.Sprintf("u%d", n)
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	startBackground := func(taskID, desc string) {
		id := "toolu_" + taskID
		out(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{
			"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "python " + desc + ".py",
				"description": desc, "run_in_background": true}}}}})
		out(map[string]any{"type": "system", "subtype": "task_started", "task_id": taskID, "tool_use_id": id,
			"description": desc, "task_type": "local_bash", "is_backgrounded": true})
		out(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{
			"type": "tool_result", "tool_use_id": id, "content": "Command running in background with ID: " + taskID}}}})
	}
	notify := func(taskID, desc, file string, code int) {
		out(map[string]any{"type": "system", "subtype": "task_notification", "task_id": taskID, "tool_use_id": "toolu_" + taskID,
			"status": "completed", "output_file": file,
			"summary": fmt.Sprintf("Background command %q completed (exit code %d)", desc, code)})
	}
	out(map[string]any{"type": "system", "subtype": "init"})
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Message   struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if msg.Type == "control_request" {
			out(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": msg.RequestID}})
			continue
		}
		if msg.Type != "user" {
			continue
		}
		text := ""
		for _, c := range msg.Message.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		first, _, _ := strings.Cut(text, "\n")
		out(map[string]any{"type": "assistant", "message": map[string]any{
			"content": []any{map[string]any{"type": "text", "text": "echo: " + first}}}})
		switch {
		case strings.HasPrefix(text, "start background"):
			startBackground("sh_train", "Train")
		case strings.HasPrefix(text, "start two "):
			x := strings.TrimSpace(strings.TrimPrefix(first, "start two "))
			startBackground("sh_train"+x, "train"+x)
			startBackground("sh_eval"+x, "eval"+x)
		case strings.HasPrefix(text, "finish quietly "):
			x, file, _ := strings.Cut(strings.TrimPrefix(first, "finish quietly "), "|")
			go func() {
				time.Sleep(300 * time.Millisecond)
				notify("sh_train"+x, "train"+x, file, 0)
				notify("sh_eval"+x, "eval"+x, "", 3)
			}()
		case strings.HasPrefix(text, "finish wake "):
			x := strings.TrimSpace(strings.TrimPrefix(first, "finish wake "))
			go func() {
				time.Sleep(300 * time.Millisecond)
				notify("sh_train"+x, "train"+x, "", 0)
				notify("sh_eval"+x, "eval"+x, "", 0)
				out(map[string]any{"type": "assistant", "message": map[string]any{
					"content": []any{map[string]any{"type": "text", "text": "self-woke: both finished"}}}})
				out(map[string]any{"type": "result", "subtype": "success", "result": "", "is_error": false})
			}()
		case strings.HasPrefix(text, "slow"):
			time.Sleep(2 * time.Second)
		}
		out(map[string]any{"type": "result", "subtype": "success", "result": "", "is_error": false})
	}
}
