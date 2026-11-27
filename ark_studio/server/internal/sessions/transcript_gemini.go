package sessions

// Gemini CLI `--output-format stream-json` events (2026-11-27, checked
// against Gemini CLI 0.71).  ARK runs Gemini headless (gemini.go); every
// stdout line is one event:
//
//   init        {session_id, model}                    the native id (ops.go)
//   message     {role: user|assistant, content, delta} assistant text arrives
//                                                      in pieces (delta)
//   tool_use    {tool_name, tool_id, parameters}
//   tool_result {tool_id, status: success|error, output, error {type, message}}
//   error       {severity: warning|error, message}     not fatal
//   result      {status: success|error, error, stats}  the end of the turn
//
// The user message repeats the prompt ARK sent (with the session block):
// ARK shows the person's own message instead, so it is dropped.  Assistant
// pieces join into one Markdown message until a tool runs.  Tools keep
// Gemini's names (run_shell_command, read_file, replace, ...), the web UI
// sorts them by name.  stderr carries terminal warnings and stack dumps:
// noise is dropped, the rest shows as notices or errors (geminiStderrKind).

import (
	"regexp"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

var (
	geminiBrokenRe   = regexp.MustCompile(`^\{\s*"type"\s*:\s*"(?:tool_result|tool_use|message)"`)
	geminiBrokenIDRe = regexp.MustCompile(`"tool_id"\s*:\s*"([^"]{1,200})"`)
)

// geminiText adds a piece of assistant text: it continues the open message.
func (b *transcriptBuilder) geminiText(text string, at any) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return
	}
	if open := b.geminiOpen; open != nil && b.openAssistant == open {
		open.Set("text", truncate(strOr(open.Value("text"))+text, maxTextChars))
		return
	}
	if pytext.Strip(text) == "" {
		return
	}
	item := pyjson.NewObject("id", b.nextID("m"), "type", "message", "role", "assistant",
		"text", truncate(strings.TrimLeft(text, "\n"), maxTextChars), "at", at, "markdown", true, "source", "stdout")
	b.items = append(b.items, item)
	b.openAssistant, b.geminiOpen = item, item
}

func geminiToolKey(id string) any {
	if id == "" {
		return nil
	}
	return "gemini-" + id
}

// geminiUsage maps result stats onto the usage numbers.
func geminiUsage(stats *pyjson.Object) *pyjson.Object {
	out := pyjson.NewObject()
	for _, k := range [][2]string{{"input_tokens", "input_tokens"}, {"output_tokens", "output_tokens"},
		{"total_tokens", "total_tokens"}, {"cached", "cache_read_input_tokens"}} {
		if n, ok := pyIntValue(stats.Value(k[0])); ok && n >= 0 {
			out.Set(k[1], n)
		}
	}
	if !out.Has("total_tokens") && (out.Has("input_tokens") || out.Has("output_tokens")) {
		in, _ := out.Value("input_tokens").(int64)
		o, _ := out.Value("output_tokens").(int64)
		out.Set("total_tokens", in+o)
	}
	return out
}

// geminiErrorOnce adds an error unless it repeats the last one.
func (b *transcriptBuilder) geminiErrorOnce(text string, at any) {
	text = pytext.Strip(text)
	for i := len(b.items) - 1; i >= 0; i-- {
		it := b.items[i]
		if it.Value("type") == "error" {
			if pytext.Strip(strOr(it.Value("text"))) == text {
				return
			}
			break
		}
		if it.Value("type") == "message" || it.Value("type") == "tool" {
			break
		}
	}
	b.addError(text, at, "stdout")
}

// geminiEventLine handles one Gemini stream-json event; false when the line
// is not one.
func (b *transcriptBuilder) geminiEventLine(line string, at any) bool {
	text := pytext.Strip(line)
	if !strings.HasPrefix(text, "{") {
		return false
	}
	obj, ok := jsonLineObject(text)
	if !ok {
		if !geminiBrokenRe.MatchString(text) {
			return false
		}
		// Cut short by the log line limit: close the tool it belongs to.
		if m := geminiBrokenIDRe.FindStringSubmatch(text); m != nil {
			if tool := b.tools["gemini-"+m[1]]; tool != nil && tool.Value("status") == "running" &&
				strings.Contains(text, `"tool_result"`) {
				tool.Set("status", "ok").Set("output", "(output too long; see the raw log)").Set("completed_at", at)
			}
		}
		return true
	}
	switch pytext.Lower(strOr(obj.Value("type"))) {
	case "init":
		return true
	case "message":
		if pytext.Lower(strOr(obj.Value("role"))) == "user" {
			b.geminiOpen = nil
			return true
		}
		b.geminiText(strOr(obj.Value("content")), at)
		return true
	case "tool_use":
		name := strOr(or(obj.Value("tool_name"), obj.Value("name")))
		if name == "" {
			name = "tool"
		}
		b.addToolCall(pyjson.NewObject("id", geminiToolKey(strOr(obj.Value("tool_id"))), "name", name,
			"input", safeJSON(obj.Value("parameters")), "status", "running", "at", at, "source", "stdout"))
		return true
	case "tool_result":
		status := "ok"
		output := cleanOutput(strOr(obj.Value("output")))
		if pytext.Lower(strOr(obj.Value("status"))) == "error" {
			status = "error"
			if e, ok := obj.Value("error").(*pyjson.Object); ok {
				if msg := strOr(e.Value("message")); msg != "" && !strings.Contains(output, msg) {
					output = strings.TrimSpace(output + "\n" + msg)
				}
			}
		}
		b.addToolResult(pyjson.NewObject("id", geminiToolKey(strOr(obj.Value("tool_id"))),
			"name", obj.Value("tool_name"), "output", output, "status", status, "at", at))
		return true
	case "error":
		msg := strOr(obj.Value("message"))
		if pytext.Lower(strOr(obj.Value("severity"))) == "error" {
			b.geminiErrorOnce(msg, at)
		} else {
			b.codexWarning(msg, at) // once per text
		}
		return true
	case "result":
		if stats, ok := obj.Value("stats").(*pyjson.Object); ok {
			if n := geminiUsage(stats); n.Len() > 0 && pyjson.Truthy(n.Value("total_tokens")) {
				b.handleEvents([]*pyjson.Object{usageEvent(n)}, at, "stdout")
			}
		}
		if outcome, reason := geminiResultOutcome(obj); outcome == "error" && reason != "interrupted" {
			b.geminiErrorOnce(reason, at)
		}
		b.openAssistant, b.geminiOpen = nil, nil
		return true
	}
	return false
}

// geminiStderr shows a Gemini stderr line (geminiStderrKind).
func (b *transcriptBuilder) geminiStderr(line string, at any) {
	switch kind := geminiStderrKind(line); kind {
	case "drop":
	case "error":
		b.geminiErrorOnce(geminiTagRe.ReplaceAllString(pytext.Strip(line), ""), at)
	default:
		text := geminiTagRe.ReplaceAllString(pytext.Strip(line), "")
		if kind == "warning" {
			b.codexWarning(text, at)
		} else {
			b.addNotice(text, at, "info", "stderr")
		}
	}
}
