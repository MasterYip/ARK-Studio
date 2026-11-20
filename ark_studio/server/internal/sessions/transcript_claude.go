package sessions

import (
	"regexp"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// Claude Code stream-json (`claude -p --output-format stream-json --verbose`)
// details the generic event parser does not know about:
//
//   - messages of a sub-agent carry `parent_tool_use_id`: they are progress
//     of the Task/Agent call that started it (the call's item gets
//     `children`, `child_tool`, `last_output`, `child_tokens`), never turns
//     of the main conversation;
//   - Claude Code tracks every Bash command and sub-agent as a task
//     (`system` task_started / task_progress / task_updated /
//     task_notification, or a `<task-notification>` user turn).  These
//     update the tool item that started the task (its `task` object: id,
//     type, status, background, description, summary, usage); they are not
//     notices.  A task whose call is not in the transcript (a resumed
//     session) becomes an item {type: "task"} that the thread does not show;
//   - a tool result's `tool_use_result` (agent id, tokens, …) is kept on the
//     tool item as `meta`;
//   - the closing `result` repeats the last reply: it only adds usage;
//   - other bookkeeping events (init, hooks, rate limits, thinking tokens)
//     are not shown;
//   - a stream line cut short by the log line limit cannot be parsed: its
//     tool result is salvaged when possible, and it is never shown as text.
//
// Real stream-json events carry a `uuid`; the bookkeeping filter only applies
// to those, so other JSON-emitting agents keep their notices.

var (
	taskNotificationRe = regexp.MustCompile(`(?s)<task-notification>(.*?)</task-notification>`)
	taskTagRe          = regexp.MustCompile(`(?s)<([a-z_-]+)>(.*?)</[a-z_-]+>`)
	streamLineRe       = regexp.MustCompile(`^\{\s*"(type|parent_tool_use_id|session_id|uuid)"\s*:`)
	brokenTypeRe       = regexp.MustCompile(`^\{\s*"type"\s*:\s*"([a-z_]+)"`)
	brokenToolUseRe    = regexp.MustCompile(`"tool_use_id"\s*:\s*"([A-Za-z0-9_-]+)"`)
	brokenContentRe    = regexp.MustCompile(`"content":"((?:[^"\\]|\\.)*)`)
)

const (
	maxToolOutputChars = 16000 // per tool item in the transcript; the raw log keeps everything
	maxToolInputString = 4000
	maxTaskText        = 400
)

func lastLine(text string, limit int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return truncate(line, limit)
		}
	}
	return ""
}

func firstString(o *pyjson.Object, keys ...string) string {
	for _, k := range keys {
		if s := pytext.Strip(strOr(o.Value(k))); s != "" {
			return s
		}
	}
	return ""
}

func taskKind(taskType string) string {
	t := pytext.Lower(taskType)
	if strings.Contains(t, "agent") {
		return "agent"
	}
	if strings.Contains(t, "bash") || strings.Contains(t, "shell") || strings.Contains(t, "command") {
		return "command"
	}
	return t
}

// taskFor returns the task record for a task id or tool call, creating it on
// the tool item (or as a hidden {type: "task"} item when the call is not in
// this transcript).
func (b *transcriptBuilder) taskFor(taskID, toolUseID string, at any) *pyjson.Object {
	if b.tasks == nil {
		b.tasks = map[string]*pyjson.Object{}
	}
	if taskID != "" && b.tasks[taskID] != nil {
		return b.tasks[taskID]
	}
	var task *pyjson.Object
	if item := b.tools[toolUseID]; toolUseID != "" && item != nil {
		task, _ = item.Value("task").(*pyjson.Object)
		if task == nil {
			task = pyjson.NewObject("id", taskID, "status", "running", "started_at", at)
			item.Set("task", task)
		}
	} else {
		task = pyjson.NewObject("id", taskID, "status", "running", "started_at", at)
		b.items = append(b.items, pyjson.NewObject("id", b.nextID("k"), "type", "task", "task", task,
			"tool_use_id", toolUseID, "at", at, "source", "stdout"))
	}
	if taskID != "" {
		task.Set("id", taskID)
		b.tasks[taskID] = task
	}
	return task
}

// taskEvent applies one task event (fields as in Claude Code's system
// events) to its record.
func (b *transcriptBuilder) taskEvent(sub string, o *pyjson.Object, at any) {
	taskID := firstString(o, "task_id", "task-id", "shell_id", "agent_id")
	toolUseID := firstString(o, "tool_use_id", "tool-use-id")
	if taskID == "" && toolUseID == "" {
		return
	}
	task := b.taskFor(taskID, toolUseID, at)
	b.childEvents++
	if v := firstString(o, "task_type", "task-type"); v != "" {
		task.Set("type", taskKind(v))
	}
	if v := firstString(o, "subagent_type"); v != "" {
		task.Set("subagent_type", truncate(v, 80))
	}
	if bg, ok := o.Get("is_backgrounded"); ok {
		task.Set("background", pyjson.Truthy(bg))
	}
	if patch, ok := o.Value("patch").(*pyjson.Object); ok {
		if bg, ok := patch.Get("is_backgrounded"); ok {
			task.Set("background", pyjson.Truthy(bg))
		}
		if s := firstString(patch, "status"); s != "" {
			task.Set("status", s)
		}
	}
	desc := firstString(o, "description")
	switch sub {
	case "task_started":
		if desc != "" {
			task.Set("description", truncate(desc, maxTaskText))
		}
		task.Set("status", "running")
	case "task_progress":
		if desc != "" {
			task.Set("last", truncate(desc, maxTaskText))
		}
		if tool := firstString(o, "last_tool_name"); tool != "" {
			task.Set("last_tool", truncate(tool, 80))
		}
		if u, ok := o.Value("usage").(*pyjson.Object); ok {
			for _, k := range []string{"total_tokens", "tool_uses", "duration_ms"} {
				if n, ok := pyIntValue(u.Value(k)); ok {
					task.Set(k, n)
				}
			}
		}
	case "task_notification":
		if s := firstString(o, "status"); s != "" {
			task.Set("status", s)
		} else {
			task.Set("status", "completed")
		}
		if s := firstString(o, "summary"); s != "" {
			task.Set("summary", truncate(s, maxTaskText))
		}
		task.Set("ended_at", at)
	}
}

// taskNotificationText applies "<task-notification>…" user turns (Claude
// Code reports finished background work this way).  False when `text` holds
// none.
func (b *transcriptBuilder) taskNotificationText(text string, at any) bool {
	blocks := taskNotificationRe.FindAllStringSubmatch(text, -1)
	if len(blocks) == 0 {
		return false
	}
	for _, block := range blocks {
		o := pyjson.NewObject()
		for _, m := range taskTagRe.FindAllStringSubmatch(block[1], -1) {
			o.Set(m[1], strings.TrimSpace(m[2]))
		}
		b.taskEvent("task_notification", o, at)
	}
	return true
}

// claudeStreamLine handles the stream-json events above; false leaves the
// line to the generic parser.
func (b *transcriptBuilder) claudeStreamLine(line string, at any) bool {
	text := pytext.Strip(line)
	obj, ok := jsonLineObject(text)
	if !ok {
		if streamLineRe.MatchString(text) {
			b.brokenStreamLine(text, at)
			return true
		}
		return false
	}
	etype := pytext.Lower(pytext.Strip(strOr(obj.Value("type"))))
	stream := obj.Has("uuid") && obj.Has("session_id")
	if parent := strOr(obj.Value("parent_tool_use_id")); parent != "" && parent != b.agent {
		if etype == "assistant" || etype == "user" {
			b.childLine(parent, obj, at)
		}
		return true
	}
	switch etype {
	case "system":
		sub := pytext.Lower(strOr(obj.Value("subtype")))
		switch sub {
		case "task_started", "task_progress", "task_updated", "task_notification":
			b.taskEvent(sub, obj, at)
			return true
		}
		return stream
	case "user":
		message, _ := obj.Value("message").(*pyjson.Object)
		if message == nil {
			return false
		}
		if text, ok := message.Value("content").(string); ok && b.taskNotificationText(text, at) {
			return true
		}
		if list, ok := message.Value("content").([]any); ok {
			for _, x := range list {
				if blk, ok := x.(*pyjson.Object); ok && strOr(blk.Value("type")) == "text" && b.taskNotificationText(strOr(blk.Value("text")), at) {
					return true
				}
			}
		}
		meta, _ := obj.Value("tool_use_result").(*pyjson.Object)
		if meta == nil {
			return false
		}
		b.handleEvents(eventsFromContent(message.Value("content"), "user"), at, "stdout")
		for _, x := range blocks(message.Value("content")) {
			if id := strOr(x.Value("tool_use_id")); id != "" && b.tools[id] != nil {
				b.tools[id].Set("meta", toolMeta(meta))
			}
		}
		return true
	case "result":
		text, ok := obj.Value("result").(string)
		if !ok || b.openAssistant == nil {
			return false
		}
		if pytext.Strip(text) == "" || !strings.HasSuffix(pytext.Strip(strOr(b.openAssistant.Value("text"))), pytext.Strip(text)) {
			return false
		}
		if n := usageNumbers(obj); n.Len() > 0 {
			b.handleEvents([]*pyjson.Object{usageEvent(n)}, at, "stdout")
		}
		return true
	case "assistant", "":
		return false
	}
	// rate_limit_event, stream_event, tool_progress and other bookkeeping.
	return stream
}

// brokenStreamLine salvages a stream-json line cut short by the log line
// limit: a main-thread tool result keeps the part that survived.  Nothing
// else of it is shown (the raw log has the line).
func (b *transcriptBuilder) brokenStreamLine(text string, at any) {
	m := brokenTypeRe.FindStringSubmatch(text)
	if m == nil || m[1] != "user" {
		return
	}
	id := brokenToolUseRe.FindStringSubmatch(text)
	if id == nil || b.tools[id[1]] == nil {
		return // a sub-agent's own tool result, or not a result at all
	}
	content := ""
	if c := brokenContentRe.FindStringSubmatch(text); c != nil {
		if s, err := pyjson.Loads(`"` + strings.TrimSuffix(c[1], `\`) + `"`); err == nil {
			content, _ = s.(string)
		} else {
			content = c[1]
		}
	}
	b.addToolResult(pyjson.NewObject("id", id[1], "output", content+"\n…[truncated]", "status", "ok", "at", at))
}

// toolMeta keeps the small, useful part of a tool_use_result.
func toolMeta(meta *pyjson.Object) *pyjson.Object {
	out := pyjson.NewObject()
	for _, k := range []string{"agentId", "agent_id", "status", "totalTokens", "totalDurationMs", "totalToolUseCount",
		"backgroundTaskId", "background_task_id", "shellId", "bash_id", "task_id", "isAsync", "model", "interrupted"} {
		if v, ok := meta.Get(k); ok && v != nil {
			if _, nested := v.(*pyjson.Object); nested {
				continue
			}
			if s, isStr := v.(string); isStr {
				v = truncate(s, 200)
			}
			out.Set(k, v)
		}
	}
	return out
}

// childLine folds a sub-agent's message into the Task/Agent call that runs it.
func (b *transcriptBuilder) childLine(parent string, obj *pyjson.Object, at any) {
	item := b.tools[parent]
	if item == nil {
		item = b.addToolCall(pyjson.NewObject("id", parent, "name", "Agent", "input", nil, "status", "running", "at", at))
	}
	b.childEvents++
	message, _ := obj.Value("message").(*pyjson.Object)
	if message == nil {
		return
	}
	role := "assistant"
	if pytext.Lower(strOr(obj.Value("type"))) == "user" {
		role = "user"
	}
	if role == "user" {
		return // the sub-agent's prompt and its own tool results
	}
	for _, e := range eventsFromContent(message.Value("content"), role) {
		switch strOr(e.Value("event")) {
		case "tool_call":
			n, _ := item.Value("children").(int64)
			item.Set("children", n+1)
			preview := strOr(e.Value("name"))
			if in, ok := e.Value("input").(*pyjson.Object); ok {
				if s := firstString(in, "command", "file_path", "path", "pattern", "description", "query"); s != "" {
					preview += " " + s
				}
			}
			item.Set("child_tool", truncate(preview, 200))
			item.Set("last_at", at)
		case "message":
			if line := lastLine(strOr(e.Value("text")), 240); line != "" {
				item.Set("last_output", line)
				item.Set("last_at", at)
			}
		}
	}
	if u, ok := message.Value("usage").(*pyjson.Object); ok {
		total := int64(0)
		for _, k := range []string{"input_tokens", "output_tokens"} {
			if n, ok := pyIntValue(u.Value(k)); ok {
				total += n
			}
		}
		if total > 0 {
			prev, _ := item.Value("child_tokens").(int64)
			item.Set("child_tokens", prev+total)
		}
	}
}

func (b *transcriptBuilder) childSignature() string {
	return strconv.FormatInt(b.childEvents, 10)
}

// capToolItems keeps the transcript small enough to send every poll: long
// outputs and long string inputs (file contents) are cut, with the full size
// noted (the raw log has everything).
func (b *transcriptBuilder) capToolItems(items []*pyjson.Object) {
	if b.capped == nil {
		b.capped = map[*pyjson.Object]bool{}
	}
	for _, it := range items {
		if it.Value("type") != "tool" || b.capped[it] {
			continue
		}
		if s, ok := it.Value("output").(string); ok && pytext.Len(s) > maxToolOutputChars {
			it.Set("output", pytext.Prefix(s, maxToolOutputChars)).Set("output_chars", int64(pytext.Len(s))).Set("output_truncated", true)
		} else if out := it.Value("output"); out != nil {
			if _, isStr := out.(string); !isStr {
				if text := formatValue(out); pytext.Len(text) > maxToolOutputChars {
					it.Set("output", pytext.Prefix(text, maxToolOutputChars)).Set("output_chars", int64(pytext.Len(text))).Set("output_truncated", true)
				}
			}
		}
		if in, ok := it.Value("input").(*pyjson.Object); ok {
			capStrings(in, maxToolInputString)
		}
		if it.Value("status") != "running" {
			b.capped[it] = true
		}
	}
}

func capStrings(o *pyjson.Object, n int) {
	for _, k := range o.Keys() {
		switch v := o.Value(k).(type) {
		case string:
			if pytext.Len(v) > n {
				o.Set(k, pytext.Prefix(v, n)+"\n…[truncated]")
			}
		case *pyjson.Object:
			capStrings(v, n)
		}
	}
}

// Images in tool results (a Read of a png, a screenshot) are kept by the
// builder, at most maxToolImageBytes in all; the transcript only counts
// them and ?item=<id>&image=<n> returns one.
const maxToolImageBytes = 24 << 20

func imageBlockURL(o *pyjson.Object) string {
	if strOr(o.Value("type")) != "image" {
		return ""
	}
	src, _ := o.Value("source").(*pyjson.Object)
	if src == nil || strOr(src.Value("type")) != "base64" {
		return ""
	}
	mime, data := strOr(src.Value("media_type")), strOr(src.Value("data"))
	if !strings.HasPrefix(mime, "image/") || data == "" {
		return ""
	}
	return "data:" + mime + ";base64," + data
}

func (b *transcriptBuilder) keepToolImages(id string, images []any) {
	if b.toolImages == nil {
		b.toolImages = map[string][]string{}
	}
	for _, x := range images {
		url, _ := x.(string)
		if url == "" || b.imageBytes+len(url) > maxToolImageBytes {
			continue
		}
		b.imageBytes += len(url)
		b.toolImages[id] = append(b.toolImages[id], url)
	}
}
