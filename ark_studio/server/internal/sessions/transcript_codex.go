package sessions

// Codex `exec --json` events (2026-11-23).  ARK runs Codex with its JSON
// event stream; every stdout line is one event:
//
//   thread.started {thread_id}            the native id (ops.go)
//   turn.started / turn.completed {usage} / turn.failed {error}
//   item.started / item.updated / item.completed {item}
//   error {message}
//
// Items carry an id and a type: agent_message, reasoning, command_execution
// (command, aggregated_output, exit_code, status), file_change (changes:
// [{path, kind}]), mcp_tool_call, web_search, todo_list, error (a warning
// such as ignored config keys).  They map onto the blocks Claude Code
// sessions use: Markdown messages, collapsible commands with their output,
// file changes and reasoning notes.  An item that is started, updated and
// completed stays one block (keyed by its id), so nothing repeats.
//
// Without --json Codex writes a human log to stderr (feed's codexMode
// branch, kept for older sessions); its tracing lines are dropped there too.

import (
	"regexp"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

var (
	// 2026-11-23T09:02:10.643786Z  WARN codex_core::...: message
	codexTracingRe  = regexp.MustCompile(`^\s*\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z?\s+(?:TRACE|DEBUG|INFO|WARN|ERROR)\s`)
	codexStdinRe    = regexp.MustCompile(`^\s*Reading (?:additional input|prompt) from stdin\.\.\.\s*$`)
	codexBrokenRe   = regexp.MustCompile(`^\{\s*"type"\s*:\s*"item\.(?:started|updated|completed)"`)
	codexBrokenIDRe = regexp.MustCompile(`"item"\s*:\s*\{\s*"id"\s*:\s*"([^"]{1,80})"`)
	// "C:\...\pwsh.exe" -Command "Get-Content x" / /bin/bash -lc 'ls -la'
	codexShellWrapRe = regexp.MustCompile(`(?is)^\s*(?:"[^"]*?(?:pwsh|powershell|bash|zsh|sh|cmd)(?:\.exe)?"|\S*?(?:pwsh|powershell|bash|zsh|sh|cmd)(?:\.exe)?)` +
		`\s+(?:-NoProfile\s+|-NoLogo\s+)*(?:-Command|-c|-lc|/c|/C)\s+(.+?)\s*$`)
)

// codexNoise reports Codex stderr lines that are not for the reader.
func codexNoise(line string) bool {
	return codexTracingRe.MatchString(line) || codexStdinRe.MatchString(line)
}

// codexCommand drops Codex's shell wrapper from a command line.
func codexCommand(cmd string) string {
	m := codexShellWrapRe.FindStringSubmatch(cmd)
	if m == nil {
		return cmd
	}
	inner := m[1]
	if n := len(inner); n >= 2 && (inner[0] == '"' || inner[0] == '\'') && inner[n-1] == inner[0] {
		double := inner[0] == '"'
		inner = inner[1 : n-1]
		if double {
			// Codex escapes a double-quoted argument the way a shell would.
			inner = strings.ReplaceAll(inner, `\\`, `\`)
			inner = strings.ReplaceAll(inner, `\"`, `"`)
		}
	}
	if strings.TrimSpace(inner) == "" {
		return cmd
	}
	return inner
}

// cleanOutput is command output as the transcript shows it: escape
// sequences removed, CRLF as LF, a line redrawn with CR kept as its last
// state.
func cleanOutput(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = cleanLine(line)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

func (b *transcriptBuilder) codexItem(id string) *pyjson.Object {
	if id == "" || b.codexItems == nil {
		return nil
	}
	return b.codexItems[id]
}

func (b *transcriptBuilder) keepCodexItem(id string, item *pyjson.Object) {
	if id == "" {
		return
	}
	if b.codexItems == nil {
		b.codexItems = map[string]*pyjson.Object{}
	}
	b.codexItems[id] = item
}

// codexText adds or updates a message or reasoning note of one item.
func (b *transcriptBuilder) codexText(id, kind, text string, at any) {
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if pytext.Strip(text) == "" {
		return
	}
	if existing := b.codexItem(id); existing != nil {
		existing.Set("text", truncate(text, maxTextChars))
		return
	}
	b.openAssistant = nil
	var item *pyjson.Object
	if kind == "reasoning" {
		item = pyjson.NewObject("id", b.nextID("n"), "type", "notice", "level", "reasoning",
			"text", truncate(text, maxTextChars), "at", at, "source", "stdout")
	} else {
		item = pyjson.NewObject("id", b.nextID("m"), "type", "message", "role", "assistant",
			"text", truncate(text, maxTextChars), "at", at, "markdown", true, "source", "stdout")
	}
	b.items = append(b.items, item)
	b.keepCodexItem(id, item)
	// Each agent message is complete: the next one is its own block.
	b.openAssistant = nil
}

func codexStatus(item *pyjson.Object, completed bool) string {
	switch pytext.Lower(strOr(item.Value("status"))) {
	case "failed", "declined", "error", "cancelled", "canceled":
		return "error"
	case "completed", "success", "succeeded":
		return "ok"
	case "in_progress", "running", "pending":
		if !completed {
			return "running"
		}
	}
	if completed {
		return "ok"
	}
	return "running"
}

// codexTool adds or updates the tool block of one item.
func (b *transcriptBuilder) codexTool(id, name string, input, output any, status string, at any) {
	key := "codex-" + id
	if id == "" {
		key = ""
	}
	call := pyjson.NewObject("id", nilIfEmpty(key), "name", name, "input", input, "status", "running", "at", at, "source", "stdout")
	tool := b.addToolCall(call)
	if input != nil {
		tool.Set("input", input)
	}
	if status == "running" {
		if output != nil && output != "" {
			tool.Set("output", output)
		}
		return
	}
	b.addToolResult(pyjson.NewObject("id", tool.Value("id"), "name", name, "output", output, "status", status, "at", at))
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func codexChanges(item *pyjson.Object) (files []any, summary string) {
	var lines []string
	list, _ := item.Value("changes").([]any)
	for _, raw := range list {
		c, ok := raw.(*pyjson.Object)
		if !ok {
			continue
		}
		path := strOr(c.Value("path"))
		kind := pytext.Lower(strOr(c.Value("kind")))
		if path == "" {
			continue
		}
		files = append(files, pyjson.NewObject("path", path, "kind", kind))
		verb := map[string]string{"add": "added", "delete": "deleted", "update": "updated"}[kind]
		if verb == "" {
			verb = "changed"
		}
		lines = append(lines, verb+" "+path)
	}
	return files, strings.Join(lines, "\n")
}

func codexTodo(item *pyjson.Object) (any, string) {
	list, _ := item.Value("items").([]any)
	var lines []string
	for _, raw := range list {
		t, ok := raw.(*pyjson.Object)
		if !ok {
			continue
		}
		mark := "[ ]"
		if pyjson.Truthy(t.Value("completed")) {
			mark = "[x]"
		}
		lines = append(lines, "- "+mark+" "+strOr(t.Value("text")))
	}
	return item.Value("items"), strings.Join(lines, "\n")
}

func codexMCPResult(item *pyjson.Object) any {
	if e, ok := item.Value("error").(*pyjson.Object); ok && pyjson.Truthy(e.Value("message")) {
		return strOr(e.Value("message"))
	}
	r, ok := item.Value("result").(*pyjson.Object)
	if !ok {
		return safeJSON(item.Value("result"))
	}
	var parts []string
	if list, ok := r.Value("content").([]any); ok {
		for _, raw := range list {
			if c, ok := raw.(*pyjson.Object); ok && pyjson.Truthy(c.Value("text")) {
				parts = append(parts, strOr(c.Value("text")))
			}
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return safeJSON(r)
}

// codexWarning shows a Codex warning once (it repeats some at start-up).
func (b *transcriptBuilder) codexWarning(text string, at any) {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if text == "" {
		return
	}
	if b.codexSeen == nil {
		b.codexSeen = map[string]bool{}
	}
	if b.codexSeen[text] {
		return
	}
	b.codexSeen[text] = true
	b.openAssistant = nil
	b.items = append(b.items, pyjson.NewObject("id", b.nextID("n"), "type", "notice", "level", "warning",
		"text", truncate(text, maxTextChars), "at", at, "source", "stdout"))
}

func (b *transcriptBuilder) codexItemEvent(item *pyjson.Object, completed bool, at any) {
	id := strOr(item.Value("id"))
	itype := pytext.Lower(strOr(item.Value("type")))
	status := codexStatus(item, completed)
	switch itype {
	case "agent_message", "assistant_message":
		b.codexText(id, "message", strOr(or(item.Value("text"), item.Value("content"))), at)
	case "reasoning", "agent_reasoning":
		b.codexText(id, "reasoning", strOr(or(item.Value("text"), item.Value("content"))), at)
	case "command_execution", "command":
		if item.Has("exit_code") && item.Value("exit_code") != nil && completed {
			if ec, ok := pyIntValue(item.Value("exit_code")); ok && ec != 0 {
				status = "error"
			}
		}
		output := cleanOutput(strOr(or(item.Value("aggregated_output"), item.Value("output"))))
		var out any = output
		if status != "running" && output == "" {
			out = ""
		}
		cmd := codexCommand(strOr(item.Value("command")))
		b.codexTool(id, "command", pyjson.NewObject("command", cmd), out, status, at)
	case "file_change", "patch_apply":
		files, summary := codexChanges(item)
		b.codexTool(id, "file_change", pyjson.NewObject("files", files), summary, status, at)
	case "mcp_tool_call", "tool_call":
		name := strOr(or(item.Value("tool"), item.Value("name")))
		if server := strOr(item.Value("server")); server != "" && name != "" {
			name = server + "." + name
		}
		if name == "" {
			name = "tool"
		}
		var out any
		if completed || status == "error" {
			out = codexMCPResult(item)
		}
		b.codexTool(id, name, safeJSON(item.Value("arguments")), out, status, at)
	case "web_search":
		b.codexTool(id, "web_search", pyjson.NewObject("query", strOr(item.Value("query"))), "", status, at)
	case "todo_list", "plan":
		items, text := codexTodo(item)
		b.codexTool(id, "update_plan", pyjson.NewObject("todos", items), text, "ok", at)
	case "error":
		b.codexWarning(strOr(item.Value("message")), at)
	default:
		// A newer item type: shown as a tool with its fields, not as JSON text.
		if itype == "" {
			return
		}
		fields := item.Copy()
		fields.Delete("id")
		fields.Delete("type")
		fields.Delete("status")
		if fields.Len() == 0 {
			return
		}
		b.codexTool(id, itype, safeJSON(fields), nil, status, at)
	}
}

// codexEventLine handles one Codex JSON event; false when the line is not one.
func (b *transcriptBuilder) codexEventLine(line string, at any) bool {
	text := pytext.Strip(line)
	if !strings.HasPrefix(text, "{") {
		return false
	}
	obj, ok := jsonLineObject(text)
	if !ok {
		if codexBrokenRe.MatchString(text) {
			// Cut short by the log line limit: close the block it belongs to.
			if m := codexBrokenIDRe.FindStringSubmatch(text); m != nil {
				if tool := b.tools["codex-"+m[1]]; tool != nil && tool.Value("status") == "running" {
					tool.Set("status", "ok").Set("output", "(output too long; see the raw log)").Set("completed_at", at)
				}
			}
			return true
		}
		return false
	}
	etype := pytext.Lower(strOr(obj.Value("type")))
	switch etype {
	case "thread.started", "turn.started":
		return true
	case "turn.completed":
		if n := usageNumbers(obj); n.Len() > 0 {
			b.handleEvents([]*pyjson.Object{usageEvent(n)}, at, "stdout")
		}
		return true
	case "turn.failed":
		msg := ""
		if e, ok := obj.Value("error").(*pyjson.Object); ok {
			msg = strOr(e.Value("message"))
		}
		if msg == "" {
			msg = "Codex turn failed"
		}
		b.addError(msg, at, "stdout")
		return true
	case "item.started", "item.updated", "item.completed":
		if item, ok := obj.Value("item").(*pyjson.Object); ok {
			b.codexItemEvent(item, etype == "item.completed", at)
		}
		return true
	case "error":
		if !b.codexMode {
			return false
		}
		msg := strOr(obj.Value("message"))
		if strings.Contains(strings.ToLower(msg), "reconnecting") {
			b.codexWarning(msg, at)
		} else {
			b.addError(msg, at, "stdout")
		}
		return true
	}
	return false
}
