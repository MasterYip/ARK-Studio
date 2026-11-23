package server

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// fakeClaudeStream is a long-lived Claude Code process (claude -p
// --input-format stream-json) for the nudge tests: every user message on
// stdin is one turn that echoes its first line and ends with a result.
// "start background" leaves a background command running after the turn.
func fakeClaudeStream() {
	n := 0
	out := func(v map[string]any) {
		n++
		v["session_id"] = "fake-nudge-0001"
		v["uuid"] = fmt.Sprintf("u%d", n)
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
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
		if strings.HasPrefix(text, "start background") {
			id := "toolu_train"
			out(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{
				"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "python train.py",
					"description": "Train", "run_in_background": true}}}}})
			out(map[string]any{"type": "system", "subtype": "task_started", "task_id": "sh_train", "tool_use_id": id,
				"description": "Train", "task_type": "local_bash", "is_backgrounded": true})
			out(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": id, "content": "Command running in background with ID: sh_train"}}}})
		}
		out(map[string]any{"type": "result", "subtype": "success", "result": "", "is_error": false})
	}
}
