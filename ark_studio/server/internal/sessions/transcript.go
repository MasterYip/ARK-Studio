package sessions

// Port of arkstudio/session_transcript.py: structured views over a task
// session's append-only logs and workspace.  Events and items are
// *pyjson.Object values so key order and Python's loose typing (a "name"
// that is a number, a "text" that is a list) survive unchanged.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyre"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/store"
)

const (
	arkEventPrefix    = "[[ark-event]]"
	MaxSourceLines    = 4000
	maxTextChars      = 120_000
	maxWorkspaceFiles = 400
	maxWorkspaceDepth = 6
)

func set(items ...string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[s] = true
	}
	return out
}

var (
	imageSuffixes    = set(".png", ".jpg", ".jpeg", ".webp", ".gif", ".svg", ".avif", ".bmp", ".ico")
	videoSuffixes    = set(".mp4", ".webm", ".mov", ".m4v", ".ogv")
	audioSuffixes    = set(".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac")
	documentSuffixes = set(".md", ".markdown", ".mdx", ".txt", ".rst", ".tex", ".json", ".jsonl",
		".csv", ".tsv", ".log", ".html", ".htm", ".xml", ".yaml", ".yml",
		".toml", ".ini", ".cfg", ".py", ".js", ".mjs", ".ts", ".css", ".sql",
		".ps1", ".sh", ".bat")
	toolCallTypes   = set("tool_call", "tool_use", "function_call", "tool_start", "command")
	toolResultTypes = set("tool_result", "tool_output", "function_result", "tool_end")
	messageTypes    = set("message", "assistant", "assistant_message", "markdown", "content")
	userTypes       = set("user", "user_message")
	errorTypes      = set("error", "failed", "failure")
	noticeTypes     = set("system", "notice", "status", "info")
	reasoningTypes  = set("reasoning", "thinking", "thought")
	usageTypes      = set("usage", "tokens", "token_usage")

	permissionRequestTypes = set("permission_request", "permission.request", "permission_required",
		"approval_request", "approval_required", "tool_permission_request")
	permissionResolutionTypes = set("permission_resolved", "permission_resolved_event", "permission_cancelled",
		"permission_canceled", "permission_cancelled_event", "approval_resolved")
	permissionKinds       = set("command", "file_edit", "network", "tool", "other")
	permissionKindAliases = map[string]string{
		"bash": "command", "shell": "command", "exec": "command", "execute": "command",
		"command_execution": "command", "terminal": "command", "process": "command",
		"edit": "file_edit", "write": "file_edit", "write_file": "file_edit",
		"file": "file_edit", "file_change": "file_edit", "filechange": "file_edit",
		"patch": "file_edit", "apply_patch": "file_edit", "file_write": "file_edit",
		"fetch": "network", "http": "network", "web": "network", "browser": "network",
		"mcp": "tool", "tool_use": "tool", "tool_call": "tool", "function": "tool",
	}
	// Ordered like the Python dict (the last loop of _permission_kind).
	permissionKindTools = []struct {
		kind  string
		names map[string]bool
	}{
		{"command", set("bash", "shell", "exec", "execute", "terminal", "command", "cmd", "powershell")},
		{"file_edit", set("write", "edit", "write_file", "create_file", "apply_patch", "patch",
			"str_replace", "str_replace_editor", "multiedit", "file_change")},
		{"network", set("fetch", "webfetch", "web_fetch", "web_search", "http", "browse", "browser")},
	}
	languageBySuffix = map[string]string{
		".bat": "bat", ".c": "c", ".cfg": "ini", ".conf": "ini", ".cpp": "cpp",
		".cs": "csharp", ".css": "css", ".csv": "csv", ".go": "go", ".h": "c",
		".hpp": "cpp", ".htm": "html", ".html": "html", ".ini": "ini", ".java": "java",
		".js": "javascript", ".json": "json", ".jsonl": "jsonl", ".jsx": "jsx",
		".log": "log", ".md": "markdown", ".markdown": "markdown", ".mdx": "markdown",
		".mjs": "javascript", ".ps1": "powershell", ".py": "python", ".rb": "ruby",
		".rs": "rust", ".rst": "rst", ".sh": "bash", ".sql": "sql", ".svg": "xml",
		".tex": "latex", ".toml": "toml", ".ts": "typescript", ".tsx": "tsx",
		".txt": "text", ".xml": "xml", ".yaml": "yaml", ".yml": "yaml",
	}

	ansiRe            = pyre.MustCompile(`\x1b\[[0-9;]*m`, 0)
	reasoningMarkerRe = pyre.MustCompile(`^\s*(?:dsh\s*:\s*)?(?:reasoning|thinking|thought)\s*:\s*$`, pyre.FlagIgnoreCase)
	dshEventRe        = pyre.MustCompile(`^\s*dsh\s*:\s*([A-Za-z][\w-]*)\s*:\s*(.*)$`, 0)
	codexHeaderRe     = pyre.MustCompile(`^\s*(?:OpenAI Codex v|--------|workdir:|model:|provider:|approval:|`+
		`sandbox:|reasoning effort:|reasoning summaries:|session id:|mcp startup:)`, pyre.FlagIgnoreCase)
	codexErrorRe = pyre.MustCompile(`^\s*(?:ERROR\b|error:|fatal:|failed\b|unexpected status|`+
		`invalid api key|not authenticated|panic:|request failed)`, pyre.FlagIgnoreCase)
	codexNumberRe = pyre.MustCompile(`[\d,]+\Z`, 0)
)

const reBudget = 1 << 62

// reSearch is pattern.search(text) (match for ^-anchored patterns).
func reSearch(re *pyre.Regexp, text string) (pyre.Match, []rune, bool) {
	s := []rune(text)
	b := reBudget
	m, ok, _ := re.SearchFrom(s, 0, -1, &b)
	return m, s, ok
}

func reMatches(re *pyre.Regexp, text string) bool {
	_, _, ok := reSearch(re, text)
	return ok
}

func reGroup(m pyre.Match, s []rune, g int) string {
	a, b, ok := m.Group(g)
	if !ok {
		return ""
	}
	return string(s[a:b])
}

// pySuffix is PurePath(name).suffix.
func pySuffix(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	i := strings.LastIndex(name, ".")
	if i > 0 && i < len(name)-1 {
		return name[i:]
	}
	return ""
}

// LanguageFor is session_transcript.language_for.
func LanguageFor(name string) string {
	return languageBySuffix[pytext.Lower(pySuffix(name))]
}

// cleanLine is clean_line.
func cleanLine(v any) string {
	text := strOr(v)
	if strings.Contains(text, "\x1b") {
		s := []rune(text)
		b := reBudget
		all, _ := ansiRe.FindAll(s, &b)
		if len(all) > 0 {
			var out []rune
			last := 0
			for _, m := range all {
				a, e, _ := m.Group(0)
				out = append(out, s[last:a]...)
				last = e
			}
			text = string(append(out, s[last:]...))
		}
	}
	return pytext.RStrip(strings.ReplaceAll(text, "\r", ""))
}

func truncate(value string, limit int) string {
	if pytext.Len(value) <= limit {
		return value
	}
	return pytext.Prefix(value, limit) + "\n…[truncated]"
}

// safeJSON is _safe_json.
func safeJSON(v any) any {
	switch x := v.(type) {
	case nil, bool, int, int64, float64:
		return v
	case string:
		return truncate(x, maxTextChars)
	case []any:
		n := len(x)
		if n > 200 {
			n = 200
		}
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = safeJSON(x[i])
		}
		return out
	case *pyjson.Object:
		out := pyjson.NewObject()
		for i, k := range x.Keys() {
			if i == 200 {
				break
			}
			out.Set(k, safeJSON(x.Value(k)))
		}
		return out
	}
	if _, ok := v.(interface{ String() string }); ok { // *big.Int
		return v
	}
	return truncate(pyjson.Str(v), maxTextChars)
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	return string(pyjson.DumpsIndent(v, 2))
}

func toolStatus(v any, def string) string {
	text := pytext.Lower(pytext.Strip(strOr(v)))
	switch text {
	case "error", "failed", "failure", "err", "nonzero":
		return "error"
	case "running", "started", "pending", "in_progress":
		return "running"
	case "ok", "success", "succeeded", "completed", "done", "exit_0":
		return "ok"
	}
	return def
}

var usageKeys = []string{"input_tokens", "output_tokens", "total_tokens",
	"cache_read_input_tokens", "cached_input_tokens", "reasoning_tokens"}
var usageAliases = map[string]string{"prompt_tokens": "input_tokens", "completion_tokens": "output_tokens",
	"cached_tokens": "cache_read_input_tokens"}

// pyIntValue is int(value) for a decoded JSON value (ok=false: TypeError/ValueError).
func pyIntValue(v any) (int64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case int64:
		return x, true
	case int:
		return int64(x), true
	case float64:
		if x != x || x > 9.2e18 || x < -9.2e18 {
			return 0, false
		}
		return int64(x), true
	case string:
		n, err := pytext.Int(x)
		return n, err == nil
	}
	return 0, false
}

// usageNumbers is _usage_numbers; keys keep first-seen order.
func usageNumbers(raw any) *pyjson.Object {
	src, ok := raw.(*pyjson.Object)
	out := pyjson.NewObject()
	if !ok {
		return out
	}
	if u, ok := src.Value("usage").(*pyjson.Object); ok {
		src = u
	}
	for _, key := range src.Keys() {
		target := key
		if a, ok := usageAliases[key]; ok {
			target = a
		}
		known := false
		for _, k := range usageKeys {
			known = known || k == target
		}
		if !known {
			continue
		}
		n, ok := pyIntValue(src.Value(key))
		if !ok || n < 0 {
			continue
		}
		prev, _ := out.Value(target).(int64)
		out.Set(target, prev+n)
	}
	if !out.Has("total_tokens") && (out.Has("input_tokens") || out.Has("output_tokens")) {
		in, _ := out.Value("input_tokens").(int64)
		o, _ := out.Value("output_tokens").(int64)
		out.Set("total_tokens", in+o)
	}
	return out
}

func event(kind string, kv ...any) *pyjson.Object {
	return pyjson.NewObject(append([]any{"event", kind}, kv...)...)
}

func usageEvent(numbers *pyjson.Object) *pyjson.Object {
	e := event("usage")
	for _, k := range numbers.Keys() {
		e.Set(k, numbers.Value(k))
	}
	return e
}

func parseObject(text string) (*pyjson.Object, bool) {
	v, err := pyjson.Loads(text)
	if err != nil {
		return nil, false
	}
	o, ok := v.(*pyjson.Object)
	return o, ok
}

// parseArkEvent is parse_ark_event.
func parseArkEvent(line string) (*pyjson.Object, bool) {
	text := pytext.Strip(line)
	if !strings.HasPrefix(text, arkEventPrefix) {
		return nil, false
	}
	raw := pytext.Strip(text[len(arkEventPrefix):])
	if raw == "" {
		return nil, false
	}
	return parseObject(raw)
}

func emptyish(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []any:
		return len(x) == 0
	case *pyjson.Object:
		return x.Len() == 0
	}
	return false
}

func pick(sources []*pyjson.Object, keys ...string) any {
	for _, src := range sources {
		if src == nil {
			continue
		}
		for _, k := range keys {
			if v := src.Value(k); !emptyish(v) {
				return v
			}
		}
	}
	return nil
}

// text is _text.
func textOf(v any, limit int) string {
	switch x := v.(type) {
	case nil:
		return ""
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = pyjson.Str(item)
		}
		v = strings.Join(parts, " ")
	case *pyjson.Object:
		v = string(pyjson.Dumps(x))
	}
	return pytext.Prefix(pytext.Strip(pyjson.Str(v)), limit)
}

func permissionKind(value any, tool, command, path string) string {
	explicit := strings.ReplaceAll(strings.ReplaceAll(pytext.Lower(pytext.Strip(strOr(value))), "-", "_"), " ", "_")
	if permissionKinds[explicit] {
		return explicit
	}
	if k, ok := permissionKindAliases[explicit]; ok {
		return k
	}
	lowered := pytext.Lower(pytext.Strip(tool))
	if permissionKindTools[1].names[lowered] {
		return "file_edit"
	}
	if command != "" {
		return "command"
	}
	if path != "" {
		return "file_edit"
	}
	for _, kt := range permissionKindTools {
		if kt.names[lowered] {
			return kt.kind
		}
	}
	if lowered != "" {
		return "tool"
	}
	return "other"
}

func objType(obj *pyjson.Object) string {
	return pytext.Lower(pytext.Strip(strOr(or(obj.Value("type"), obj.Value("event")))))
}

// NormalizePermissionRequest is normalize_permission_request.
func NormalizePermissionRequest(v any) *pyjson.Object {
	obj, ok := v.(*pyjson.Object)
	if !ok {
		return nil
	}
	etype := objType(obj)
	protocol := "ark-permission-v1"
	source := obj
	if etype == "control_request" {
		request, ok := obj.Value("request").(*pyjson.Object)
		if !ok {
			return nil
		}
		switch pytext.Lower(pytext.Strip(strOr(request.Value("subtype")))) {
		case "can_use_tool", "permission_request", "approval_request":
		default:
			return nil
		}
		protocol = "claude-control"
		source = request.Copy().Set("request_id", or(obj.Value("request_id"), request.Value("request_id")))
	} else if !permissionRequestTypes[etype] {
		return nil
	}
	nested, _ := source.Value("request").(*pyjson.Object)
	if nested == nil {
		nested = pyjson.NewObject()
	}
	sources := []*pyjson.Object{source, nested}
	requestID := pick(sources, "request_id", "requestId", "id", "approval_id", "approvalId", "tool_call_id", "call_id")
	toolV := pick(sources, "tool", "tool_name", "toolName", "name", "function")
	if d, ok := toolV.(*pyjson.Object); ok {
		toolV = or(d.Value("name"), d.Value("tool"))
	}
	tool := textOf(toolV, 120)
	args := pick(sources, "input", "arguments", "args", "params", "parameters", "details")
	commandV := pick(sources, "command", "cmd", "argv", "script", "command_line")
	if a, ok := args.(*pyjson.Object); ok && !pyjson.Truthy(commandV) {
		commandV = or(or(a.Value("command"), a.Value("cmd")), a.Value("argv"))
	}
	command := textOf(commandV, 2000)
	pathV := pick(sources, "path", "file", "file_path", "filepath", "target", "uri", "resource")
	if a, ok := args.(*pyjson.Object); ok && !pyjson.Truthy(pathV) {
		pathV = or(or(a.Value("path"), a.Value("file")), a.Value("file_path"))
	}
	path := textOf(pathV, 500)
	kind := permissionKind(pick(sources, "approval_kind", "permission_kind", "category", "action", "kind", "type"), tool, command, path)
	description := textOf(pick(sources, "description", "reason", "message", "prompt", "title", "tool_description"), 600)
	if !(pyjson.Truthy(requestID) || tool != "" || command != "" || path != "") {
		return nil
	}
	native := ""
	if protocol == "claude-control" {
		native = textOf(or(obj.Value("id"), obj.Value("request_id")), 120)
	}
	return pyjson.NewObject("request_id", textOf(requestID, 120), "kind", kind, "tool", tool, "command", command,
		"path", path, "description", description, "input", safeJSON(args), "protocol", protocol, "native_id", native)
}

func jsonLineObject(line string) (*pyjson.Object, bool) {
	text := pytext.Strip(line)
	if !(strings.HasPrefix(text, "{") && strings.HasSuffix(text, "}")) {
		return nil, false
	}
	return parseObject(text)
}

// PermissionRequestFromLine is permission_request_from_line.
func PermissionRequestFromLine(line string) *pyjson.Object {
	if explicit, ok := parseArkEvent(line); ok {
		return NormalizePermissionRequest(explicit)
	}
	obj, ok := jsonLineObject(line)
	if !ok {
		return nil
	}
	return NormalizePermissionRequest(obj)
}

func normalizePermissionResolution(v any) *pyjson.Object {
	obj, ok := v.(*pyjson.Object)
	if !ok {
		return nil
	}
	etype := objType(obj)
	if !permissionResolutionTypes[etype] {
		return nil
	}
	requestID := pick([]*pyjson.Object{obj}, "request_id", "requestId", "id", "approval_id")
	if !pyjson.Truthy(requestID) {
		return nil
	}
	status := "resolved"
	if strings.Contains(etype, "cancel") {
		status = "cancelled"
	}
	return pyjson.NewObject("request_id", textOf(requestID, 120), "status", status,
		"message", textOf(or(obj.Value("message"), obj.Value("reason")), 300))
}

// PermissionResolutionFromLine is permission_resolution_from_line.
func PermissionResolutionFromLine(line string) *pyjson.Object {
	if explicit, ok := parseArkEvent(line); ok {
		return normalizePermissionResolution(explicit)
	}
	obj, ok := jsonLineObject(line)
	if !ok {
		return nil
	}
	return normalizePermissionResolution(obj)
}

// BuildPermissionResponse is build_permission_response.
func BuildPermissionResponse(request *pyjson.Object, decision, scope, message string) *pyjson.Object {
	allow := pytext.Lower(decision) == "allow"
	requestID := textOf(or(request.Value("request_id"), request.Value("id")), 120)
	if pyjson.Str(or(request.Value("protocol"), "")) == "claude-control" {
		native := textOf(or(request.Value("native_id"), requestID), 120)
		behavior := "deny"
		if allow {
			behavior = "allow"
		}
		response := pyjson.NewObject("behavior", behavior)
		if !allow && message != "" {
			response.Set("message", textOf(message, 600))
		} else if in, ok := request.Value("input").(*pyjson.Object); allow && ok {
			response.Set("updatedInput", in)
		}
		return pyjson.NewObject("type", "control_response",
			"response", pyjson.NewObject("subtype", "success", "request_id", native, "response", response))
	}
	d := "deny"
	if allow {
		d = "allow"
	}
	if scope == "" {
		scope = "once"
	}
	return pyjson.NewObject("type", "permission_response", "protocol", "ark-permission-v1", "id", requestID,
		"request_id", requestID, "decision", d, "scope", scope, "message", textOf(message, 600))
}

func blocks(v any) []*pyjson.Object {
	list, _ := v.([]any)
	var out []*pyjson.Object
	for _, item := range list {
		if o, ok := item.(*pyjson.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// getDefault is d.get(key, default) (presence, not truthiness).
func getDefault(o *pyjson.Object, key string, def any) any {
	if v, ok := o.Get(key); ok {
		return v
	}
	return def
}

func eventsFromToolCall(obj *pyjson.Object) []*pyjson.Object {
	name := or(or(or(obj.Value("name"), obj.Value("tool")), obj.Value("tool_name")), obj.Value("function"))
	if d, ok := name.(*pyjson.Object); ok {
		name = d.Value("name")
	}
	args := getDefault(obj, "input", getDefault(obj, "arguments", getDefault(obj, "args", obj.Value("parameters"))))
	callID := or(or(obj.Value("id"), obj.Value("tool_call_id")), obj.Value("call_id"))
	output := getDefault(obj, "output", getDefault(obj, "result", obj.Value("content")))
	def := "running"
	if output != nil {
		def = "ok"
	}
	status := toolStatus(obj.Value("status"), def)
	if obj.Has("exit_code") {
		if ec := obj.Value("exit_code"); ec == nil || store.PyEqual(ec, int64(0)) {
			status = "ok"
		} else {
			status = "error"
		}
	}
	if !pyjson.Truthy(name) && output == nil {
		return nil
	}
	n := pyjson.Str(or(name, "tool"))
	out := []*pyjson.Object{event("tool_call", "id", callID, "name", n, "input", safeJSON(args), "status", "running")}
	if output != nil {
		out = append(out, event("tool_result", "id", callID, "name", n, "output", safeJSON(output), "status", status))
	}
	return out
}

func eventsFromContent(content any, role string) []*pyjson.Object {
	if s, ok := content.(string); ok {
		if pytext.Strip(s) != "" {
			return []*pyjson.Object{event("message", "role", role, "text", s)}
		}
		return nil
	}
	var events []*pyjson.Object
	for _, block := range blocks(content) {
		btype := pytext.Lower(strOr(block.Value("type")))
		switch {
		case btype == "text" || btype == "output_text" || btype == "input_text":
			text := or(or(block.Value("text"), block.Value("content")), "")
			if pyjson.Truthy(text) {
				events = append(events, event("message", "role", role, "text", text))
			}
		case toolCallTypes[btype] || btype == "tool_use" || btype == "server_tool_use":
			events = append(events, eventsFromToolCall(block)...)
		case toolResultTypes[btype]:
			callID := or(or(block.Value("tool_use_id"), block.Value("tool_call_id")), block.Value("id"))
			output := getDefault(block, "content", getDefault(block, "output", block.Value("result")))
			if list, ok := output.([]any); ok {
				var parts []string
				for _, item := range list {
					if o, ok := item.(*pyjson.Object); ok {
						parts = append(parts, strOr(o.Value("text")))
					}
				}
				output = strings.Join(parts, "\n")
			}
			var st any = block.Value("status")
			if pyjson.Truthy(block.Value("is_error")) {
				st = "error"
			}
			events = append(events, event("tool_result", "id", callID, "output", safeJSON(output), "status", toolStatus(st, "ok")))
		case reasoningTypes[btype]:
			events = append(events, event("reasoning", "text", strOr(or(block.Value("text"), block.Value("thinking")))))
		}
	}
	return events
}

func eventsFromCodexItem(item *pyjson.Object, completed bool) []*pyjson.Object {
	itype := pytext.Lower(strOr(item.Value("type")))
	status := "running"
	if completed {
		status = "ok"
	}
	switch itype {
	case "agent_message", "assistant_message":
		text := or(or(item.Value("text"), item.Value("content")), "")
		if pyjson.Truthy(text) {
			return []*pyjson.Object{event("message", "role", "assistant", "text", text, "markdown", true)}
		}
		return nil
	case "reasoning", "agent_reasoning":
		text := or(or(item.Value("text"), item.Value("content")), "")
		if pyjson.Truthy(text) {
			return []*pyjson.Object{event("reasoning", "text", text)}
		}
		return nil
	case "command_execution", "command":
		output := getDefault(item, "aggregated_output", getDefault(item, "output", item.Value("stdout")))
		if output == nil && pyjson.Truthy(item.Value("stderr")) {
			output = item.Value("stderr")
		}
		if item.Has("exit_code") {
			if ec := item.Value("exit_code"); ec == nil || store.PyEqual(ec, int64(0)) {
				status = "ok"
			} else {
				status = "error"
			}
		}
		callID := item.Value("id")
		out := []*pyjson.Object{event("tool_call", "id", callID, "name", "command",
			"input", safeJSON(or(item.Value("command"), item.Value("input"))), "status", "running")}
		if completed || output != nil {
			out = append(out, event("tool_result", "id", callID, "name", "command", "output", safeJSON(output), "status", status))
		}
		return out
	case "mcp_tool_call", "tool_call", "web_search":
		name := or(item.Value("tool"), item.Value("name"))
		if pyjson.Truthy(item.Value("server")) && pyjson.Truthy(item.Value("tool")) {
			name = pyjson.Str(item.Value("server")) + "." + pyjson.Str(item.Value("tool"))
		}
		n := pyjson.Str(or(name, itype))
		callID := item.Value("id")
		out := []*pyjson.Object{event("tool_call", "id", callID, "name", n,
			"input", safeJSON(getDefault(item, "arguments", item.Value("input"))), "status", "running")}
		output := getDefault(item, "result", item.Value("output"))
		if completed || output != nil {
			out = append(out, event("tool_result", "id", callID, "name", n, "output", safeJSON(output), "status", status))
		}
		return out
	case "file_change":
		callID := item.Value("id")
		changes := getDefault(item, "changes", item.Value("paths"))
		return []*pyjson.Object{
			event("tool_call", "id", callID, "name", "file_change", "input", safeJSON(changes), "status", "running"),
			event("tool_result", "id", callID, "name", "file_change", "output", "file change recorded", "status", status),
		}
	}
	return nil
}

func toolCallsEvents(calls any) []*pyjson.Object {
	var events []*pyjson.Object
	list, _ := calls.([]any)
	for _, c := range list {
		call, ok := c.(*pyjson.Object)
		if !ok {
			continue
		}
		fn, _ := call.Value("function").(*pyjson.Object)
		if fn == nil {
			fn = pyjson.NewObject()
		}
		events = append(events, eventsFromToolCall(pyjson.NewObject("id", call.Value("id"),
			"name", or(fn.Value("name"), call.Value("name")),
			"arguments", or(fn.Value("arguments"), call.Value("arguments"))))...)
	}
	return events
}

// eventsFromJSON is _events_from_json; nil means "show as text".
func eventsFromJSON(obj *pyjson.Object) []*pyjson.Object {
	etype := pytext.Lower(pytext.Strip(strOr(obj.Value("type"))))
	role := pytext.Lower(pytext.Strip(strOr(obj.Value("role"))))
	if permissionRequestTypes[etype] || etype == "control_request" {
		if p := NormalizePermissionRequest(obj); p != nil {
			return []*pyjson.Object{event("permission", "request", p)}
		}
		return nil
	}
	if permissionResolutionTypes[etype] {
		if r := normalizePermissionResolution(obj); r != nil {
			return []*pyjson.Object{event("permission_resolution", "resolution", r)}
		}
		return nil
	}
	if item, ok := obj.Value("item").(*pyjson.Object); ok && (etype == "item.completed" || etype == "item.started" || etype == "item.updated") {
		return nonNil(eventsFromCodexItem(item, etype == "item.completed"))
	}
	_, usageIsDict := obj.Value("usage").(*pyjson.Object)
	if usageTypes[etype] || (etype == "" && usageIsDict) {
		if n := usageNumbers(obj); n.Len() > 0 {
			return []*pyjson.Object{usageEvent(n)}
		}
		return nil
	}
	if etype == "result" {
		if s, ok := obj.Value("result").(string); ok {
			events := []*pyjson.Object{event("message", "role", "assistant", "text", s, "markdown", true)}
			if n := usageNumbers(obj); n.Len() > 0 {
				events = append(events, usageEvent(n))
			}
			return events
		}
		if usageIsDict {
			if n := usageNumbers(obj); n.Len() > 0 {
				return []*pyjson.Object{usageEvent(n)}
			}
			return nil
		}
	}
	message, msgIsDict := obj.Value("message").(*pyjson.Object)
	if (etype == "assistant" || etype == "message") && msgIsDict {
		events := eventsFromContent(message.Value("content"), "assistant")
		if len(events) == 0 {
			if s, ok := message.Value("text").(string); ok {
				events = []*pyjson.Object{event("message", "role", "assistant", "text", s)}
			}
		}
		return events
	}
	if etype == "user" && msgIsDict {
		return eventsFromContent(message.Value("content"), "user")
	}
	if toolCallTypes[etype] {
		return nonNil(eventsFromToolCall(obj))
	}
	if toolResultTypes[etype] {
		return []*pyjson.Object{event("tool_result",
			"id", or(or(obj.Value("tool_use_id"), obj.Value("tool_call_id")), obj.Value("id")),
			"name", or(obj.Value("name"), obj.Value("tool_name")),
			"output", safeJSON(getDefault(obj, "output", getDefault(obj, "content", obj.Value("result")))),
			"status", toolStatus(obj.Value("status"), "ok"))}
	}
	if errorTypes[etype] {
		return []*pyjson.Object{event("error", "text", strOr(or(or(obj.Value("message"), obj.Value("error")), obj.Value("text"))))}
	}
	if noticeTypes[etype] {
		text := or(or(obj.Value("message"), obj.Value("text")), obj.Value("subtype"))
		if pyjson.Truthy(text) {
			return []*pyjson.Object{event("notice", "text", pyjson.Str(text))}
		}
		return nil
	}
	if reasoningTypes[etype] {
		text := or(or(obj.Value("text"), obj.Value("reasoning")), obj.Value("content"))
		if pyjson.Truthy(text) {
			return []*pyjson.Object{event("reasoning", "text", pyjson.Str(text))}
		}
		return nil
	}
	if messageTypes[etype] && !msgIsDict {
		text := or(or(obj.Value("text"), obj.Value("content")), obj.Value("markdown"))
		if s, ok := text.(string); ok && pytext.Strip(s) != "" {
			r := role
			if r == "" {
				r = "assistant"
			}
			return []*pyjson.Object{event("message", "role", r, "text", s,
				"markdown", pyjson.Truthy(obj.Value("markdown")) || etype == "markdown" || etype == "content")}
		}
	}
	if choices, ok := obj.Value("choices").([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(*pyjson.Object)
		if choice == nil {
			choice = pyjson.NewObject()
		}
		msg := or(or(choice.Value("message"), choice.Value("delta")), pyjson.NewObject())
		var events []*pyjson.Object
		if m, ok := msg.(*pyjson.Object); ok {
			r := pyjson.Str(or(m.Value("role"), "assistant"))
			switch content := m.Value("content").(type) {
			case string:
				if pytext.Strip(content) != "" {
					events = append(events, event("message", "role", r, "text", content))
				}
			case []any:
				events = append(events, eventsFromContent(content, r)...)
			}
			events = append(events, toolCallsEvents(m.Value("tool_calls"))...)
		}
		if n := usageNumbers(obj); n.Len() > 0 {
			events = append(events, usageEvent(n))
		}
		return nonNil(events)
	}
	if (role == "assistant" || role == "user") && etype == "" {
		events := eventsFromContent(obj.Value("content"), role)
		if len(events) == 0 {
			if s, ok := obj.Value("text").(string); ok {
				events = []*pyjson.Object{event("message", "role", role, "text", s)}
			}
		}
		return nonNil(events)
	}
	if _, ok := obj.Value("tool_calls").([]any); ok {
		return nonNil(toolCallsEvents(obj.Value("tool_calls")))
	}
	return nil
}

func nonNil(events []*pyjson.Object) []*pyjson.Object {
	if len(events) == 0 {
		return nil
	}
	return events
}

// eventsFromLine is events_from_line.
func eventsFromLine(line string) []*pyjson.Object {
	if explicit, ok := parseArkEvent(line); ok {
		if p := NormalizePermissionRequest(explicit); p != nil {
			return []*pyjson.Object{event("permission", "request", p)}
		}
		if r := normalizePermissionResolution(explicit); r != nil {
			return []*pyjson.Object{event("permission_resolution", "resolution", r)}
		}
		etype := pytext.Lower(strOr(or(explicit.Value("type"), explicit.Value("event"))))
		switch {
		case toolCallTypes[etype]:
			return []*pyjson.Object{event("tool_call", "id", explicit.Value("id"),
				"name", or(or(explicit.Value("name"), explicit.Value("tool")), "tool"),
				"input", safeJSON(getDefault(explicit, "input", explicit.Value("arguments"))),
				"status", toolStatus(explicit.Value("status"), "running"))}
		case toolResultTypes[etype]:
			return []*pyjson.Object{event("tool_result", "id", explicit.Value("id"), "name", explicit.Value("name"),
				"output", safeJSON(getDefault(explicit, "output", explicit.Value("result"))),
				"status", toolStatus(explicit.Value("status"), "ok"))}
		case errorTypes[etype]:
			return []*pyjson.Object{event("error", "text", strOr(or(explicit.Value("text"), explicit.Value("message"))))}
		case reasoningTypes[etype]:
			return []*pyjson.Object{event("reasoning", "text", strOr(explicit.Value("text")))}
		case usageTypes[etype]:
			if n := usageNumbers(explicit); n.Len() > 0 {
				return []*pyjson.Object{usageEvent(n)}
			}
			return nil
		case noticeTypes[etype]:
			return []*pyjson.Object{event("notice", "text", strOr(or(explicit.Value("text"), explicit.Value("message"))),
				"level", pyjson.Str(or(explicit.Value("level"), "info")))}
		case userTypes[etype]:
			return []*pyjson.Object{event("message", "role", "user", "text", strOr(or(explicit.Value("text"), explicit.Value("content"))))}
		}
		text := or(or(explicit.Value("text"), explicit.Value("content")), explicit.Value("markdown"))
		if s, ok := text.(string); ok && pytext.Strip(s) != "" {
			md := true
			if v, ok := explicit.Get("markdown"); ok {
				md = pyjson.Truthy(v)
			}
			return []*pyjson.Object{event("message", "role", pyjson.Str(or(explicit.Value("role"), "assistant")), "text", s, "markdown", md)}
		}
		return nil
	}
	obj, ok := jsonLineObject(line)
	if !ok {
		return nil
	}
	return eventsFromJSON(obj)
}

// TranscriptInput is the keyword arguments of build_transcript.
type TranscriptInput struct {
	SessionID, Status, Prompt string
	PromptAt                  any
	Stdout, Stderr            []*pyjson.Object
	Error, Notice, Platform   string
}

type transcriptBuilder struct {
	items         []*pyjson.Object
	tools         map[string]*pyjson.Object
	permissions   map[string]*pyjson.Object
	openAssistant *pyjson.Object
	n             int
	reported      *pyjson.Object
	usageEvents   int64
}

func (b *transcriptBuilder) nextID(prefix string) string {
	b.n++
	return prefix + "-" + strconv.Itoa(b.n)
}

func (b *transcriptBuilder) addMessage(role, text string, at any, source string, markdown bool) {
	text = truncate(cleanLine(text), maxTextChars)
	if pytext.Strip(text) == "" {
		return
	}
	if role == "assistant" && b.openAssistant != nil && markdown {
		b.openAssistant.Set("text", truncate(pyjson.Str(b.openAssistant.Value("text"))+"\n"+text, maxTextChars))
		b.openAssistant.Set("at", or(at, b.openAssistant.Value("at")))
		return
	}
	item := pyjson.NewObject("id", b.nextID("m"), "type", "message", "role", role, "text", text,
		"at", at, "markdown", markdown, "source", source)
	b.items = append(b.items, item)
	if role == "assistant" {
		b.openAssistant = item
	} else {
		b.openAssistant = nil
	}
}

func (b *transcriptBuilder) addNotice(text string, at any, level, source string) {
	text = truncate(cleanLine(text), maxTextChars)
	if pytext.Strip(text) == "" {
		return
	}
	if n := len(b.items); n > 0 {
		last := b.items[n-1]
		if last.Value("type") == "notice" && last.Value("level") == level && last.Value("source") == source {
			last.Set("text", truncate(pyjson.Str(last.Value("text"))+"\n"+text, maxTextChars))
			return
		}
	}
	b.openAssistant = nil
	b.items = append(b.items, pyjson.NewObject("id", b.nextID("n"), "type", "notice", "level", level,
		"text", text, "at", at, "source", source))
}

func (b *transcriptBuilder) addError(text string, at any, source string) {
	text = truncate(cleanLine(text), maxTextChars)
	if pytext.Strip(text) == "" {
		return
	}
	b.openAssistant = nil
	b.items = append(b.items, pyjson.NewObject("id", b.nextID("e"), "type", "error", "text", text,
		"at", at, "source", source))
}

func (b *transcriptBuilder) addToolCall(call *pyjson.Object) *pyjson.Object {
	b.openAssistant = nil
	callID := strOr(call.Value("id"))
	if callID != "" {
		if existing := b.tools[callID]; existing != nil {
			existing.Set("name", or(call.Value("name"), existing.Value("name")))
			if call.Value("input") != nil {
				existing.Set("input", call.Value("input"))
			}
			if pyjson.Truthy(call.Value("status")) {
				existing.Set("status", call.Value("status"))
			}
			return existing
		}
	}
	id := callID
	if id == "" {
		id = b.nextID("t")
	}
	item := pyjson.NewObject("id", id, "type", "tool", "name", pyjson.Str(or(call.Value("name"), "tool")),
		"input", call.Value("input"), "output", nil, "status", toolStatus(call.Value("status"), "running"),
		"at", call.Value("at"), "completed_at", nil, "source", or(call.Value("source"), "stdout"))
	b.items = append(b.items, item)
	b.tools[id] = item
	return item
}

func (b *transcriptBuilder) addToolResult(result *pyjson.Object) {
	b.openAssistant = nil
	callID := strOr(result.Value("id"))
	var item *pyjson.Object
	if callID != "" {
		item = b.tools[callID]
	}
	if item == nil && pyjson.Truthy(result.Value("name")) {
		for i := len(b.items) - 1; i >= 0; i-- {
			c := b.items[i]
			if c.Value("type") == "tool" && store.PyEqual(c.Value("name"), result.Value("name")) && c.Value("status") == "running" {
				item = c
				break
			}
		}
	}
	if item == nil {
		var id any
		if callID != "" {
			id = callID
		}
		item = b.addToolCall(pyjson.NewObject("id", id, "name", or(result.Value("name"), "tool"), "input", nil,
			"status", "running", "at", result.Value("at")))
	}
	item.Set("output", result.Value("output"))
	item.Set("status", toolStatus(result.Value("status"), "ok"))
	item.Set("completed_at", or(result.Value("at"), item.Value("at")))
}

func (b *transcriptBuilder) addPermission(request *pyjson.Object) {
	b.openAssistant = nil
	requestID := strOr(request.Value("request_id"))
	if requestID != "" && b.permissions[requestID] != nil {
		return
	}
	suffix := requestID
	if suffix == "" {
		suffix = b.nextID("p")
	}
	item := pyjson.NewObject("id", "perm-"+suffix, "type", "permission", "request_id", requestID,
		"kind", pyjson.Str(or(request.Value("kind"), "other")), "tool", strOr(request.Value("tool")),
		"command", strOr(request.Value("command")), "path", strOr(request.Value("path")),
		"description", strOr(request.Value("description")), "input", request.Value("input"),
		"protocol", pyjson.Str(or(request.Value("protocol"), "ark-permission-v1")), "status", "pending",
		"at", request.Value("at"), "source", or(request.Value("source"), "stdout"))
	b.items = append(b.items, item)
	if requestID != "" {
		b.permissions[requestID] = item
	}
}

func (b *transcriptBuilder) addPermissionResolution(resolution *pyjson.Object) {
	item := b.permissions[strOr(resolution.Value("request_id"))]
	if item == nil || item.Value("status") != "pending" {
		return
	}
	status := "resolved"
	if resolution.Value("status") == "cancelled" {
		status = "cancelled"
	}
	item.Set("status", status).Set("message", strOr(resolution.Value("message"))).Set("resolved_at", resolution.Value("at"))
}

func withExtra(e *pyjson.Object, kv ...any) *pyjson.Object {
	out := e.Copy()
	for i := 0; i+1 < len(kv); i += 2 {
		out.Set(kv[i].(string), kv[i+1])
	}
	return out
}

func (b *transcriptBuilder) handleEvents(events []*pyjson.Object, at any, source string) {
	for _, e := range events {
		switch pytext.Lower(strOr(e.Value("event"))) {
		case "message":
			role := pytext.Lower(pyjson.Str(or(e.Value("role"), "assistant")))
			md := role != "user"
			if v, ok := e.Get("markdown"); ok {
				md = pyjson.Truthy(v)
			}
			r := "assistant"
			if role == "user" || role == "assistant" {
				r = role
			}
			b.addMessage(r, strOr(e.Value("text")), at, source, md)
		case "tool_call":
			b.addToolCall(withExtra(e, "at", at, "source", source))
		case "tool_result":
			b.addToolResult(withExtra(e, "at", at))
		case "permission":
			if r, ok := e.Value("request").(*pyjson.Object); ok {
				b.addPermission(withExtra(r, "at", at, "source", source))
			}
		case "permission_resolution":
			if r, ok := e.Value("resolution").(*pyjson.Object); ok {
				b.addPermissionResolution(withExtra(r, "at", at))
			}
		case "reasoning":
			if text := pytext.Strip(strOr(e.Value("text"))); text != "" {
				b.addNotice(text, at, "reasoning", source)
			}
		case "usage":
			if n := usageNumbers(e); n.Len() > 0 {
				b.usageEvents++
				for _, k := range n.Keys() {
					prev, _ := b.reported.Value(k).(int64)
					b.reported.Set(k, prev+n.Value(k).(int64))
				}
			}
		case "error":
			b.addError(strOr(e.Value("text")), at, source)
		default:
			b.addNotice(strOr(e.Value("text")), at, pyjson.Str(or(e.Value("level"), "info")), source)
		}
	}
}

var inputMarkers = "›❯>"

// stripInputMarker is re.sub(r"^\s*[›❯>]\s*", "", line).
func stripInputMarker(line string) string {
	rest := pytext.LStrip(line)
	for _, r := range rest {
		if strings.ContainsRune(inputMarkers, r) {
			return pytext.LStrip(rest[len(string(r)):])
		}
		break
	}
	return line
}

// BuildTranscript is build_transcript.
func BuildTranscript(in TranscriptInput) *pyjson.Object {
	b := &transcriptBuilder{tools: map[string]*pyjson.Object{}, permissions: map[string]*pyjson.Object{}, reported: pyjson.NewObject()}
	active := in.Status == "starting" || in.Status == "running" || in.Status == "idle" || in.Status == "stopping"
	if pytext.Strip(in.Prompt) != "" {
		b.addMessage("user", in.Prompt, in.PromptAt, "prompt", false)
	}
	type row struct {
		stream string
		index  int
		entry  *pyjson.Object
		key    string
	}
	var combined []row
	for i, e := range in.Stdout {
		combined = append(combined, row{"stdout", i, e, strOr(e.Value("t"))})
	}
	for i, e := range in.Stderr {
		combined = append(combined, row{"stderr", i, e, strOr(e.Value("t"))})
	}
	sort.SliceStable(combined, func(i, j int) bool {
		a, c := combined[i], combined[j]
		if a.key != c.key {
			return a.key < c.key
		}
		if a.stream != c.stream {
			return a.stream == "stdout"
		}
		return a.index < c.index
	})
	reasoningStream := false
	codexMode := pytext.Lower(in.Platform) == "codex"
	codexSpeaker := ""
	codexUsageNumber, codexDone := false, false
	for _, r := range combined {
		line := cleanLine(r.entry.Value("line"))
		if pytext.Strip(line) == "" {
			continue
		}
		at := r.entry.Value("t")
		if r.entry.Value("kind") == "input" {
			reasoningStream = false
			b.addMessage("user", stripInputMarker(line), at, "stdin", false)
			continue
		}
		if r.stream == "stderr" && codexMode {
			marker := pytext.Strip(line)
			low := pytext.Lower(marker)
			switch {
			case codexDone:
				continue
			case codexUsageNumber && reMatches(codexNumberRe, marker):
				codexUsageNumber, codexDone = false, true
				continue
			case low == "tokens used":
				codexUsageNumber = true
				continue
			case low == "user":
				codexSpeaker = "user"
				continue
			case low == "codex":
				codexSpeaker = "codex"
				continue
			case reMatches(codexHeaderRe, line):
				continue
			case reMatches(codexErrorRe, line):
				b.addError(line, at, "stderr")
				continue
			case strings.HasPrefix(low, "warning:") || strings.HasPrefix(low, "thinking:") || strings.HasPrefix(low, "reasoning:"):
				b.addNotice(line, at, "reasoning", "stderr")
				continue
			case codexSpeaker == "user":
				continue
			case codexSpeaker == "codex":
				b.addMessage("assistant", line, at, "stderr", true)
			default:
				b.addNotice(line, at, "info", "stderr")
			}
			continue
		}
		if r.stream == "stderr" {
			if reMatches(reasoningMarkerRe, line) {
				reasoningStream = true
				continue
			}
			if reasoningStream {
				m, s, ok := reSearch(dshEventRe, line)
				if ok && !reasoningTypes[pytext.Lower(reGroup(m, s, 1))] {
					reasoningStream = false
				} else {
					b.addNotice(line, at, "reasoning", "stderr")
					continue
				}
			}
			b.addError(line, at, "stderr")
			continue
		}
		reasoningStream = false
		if events := eventsFromLine(line); len(events) > 0 {
			b.handleEvents(events, at, "stdout")
		} else {
			b.addMessage("assistant", line, at, "stdout", true)
		}
	}
	if in.Error != "" {
		b.addError(in.Error, nil, "meta")
	}
	if in.Notice != "" {
		b.addNotice(in.Notice, nil, "notice", "meta")
	}
	items := b.items
	var lastActive *pyjson.Object
	for _, it := range items {
		if t := it.Value("type"); t == "message" || t == "tool" {
			lastActive = it
		}
	}
	if active && lastActive != nil {
		if lastActive.Value("type") == "message" && lastActive.Value("role") == "assistant" {
			lastActive.Set("streaming", true)
		} else if lastActive.Value("type") == "tool" && lastActive.Value("status") == "running" {
			lastActive.Set("streaming", true)
		}
	}
	if n := len(items); active && n > 0 && items[n-1].Value("type") == "notice" && items[n-1].Value("level") == "reasoning" {
		items[n-1].Set("streaming", true)
	}
	inputChars, outputChars := 0, 0
	for _, it := range items {
		switch it.Value("type") {
		case "message":
			n := pytext.Len(strOr(it.Value("text")))
			if it.Value("role") == "user" {
				inputChars += n
			} else if it.Value("role") == "assistant" {
				outputChars += n
			}
		}
	}
	for _, it := range items {
		if it.Value("type") == "tool" {
			inputChars += pytext.Len(formatValue(it.Value("input")))
		}
	}
	for _, it := range items {
		if it.Value("type") == "tool" {
			outputChars += pytext.Len(formatValue(it.Value("output")))
		}
	}
	estIn, estOut := int64((inputChars+3)/4), int64((outputChars+3)/4)
	estimated := pyjson.NewObject("input_tokens", estIn, "output_tokens", estOut, "total_tokens", estIn+estOut)
	usage := pyjson.NewObject("source", "estimated", "estimated", b.usageEvents == 0, "reported_events", b.usageEvents,
		"reported", b.reported.Copy(), "estimated_usage", estimated, "input_chars", int64(inputChars), "output_chars", int64(outputChars))
	effective := estimated
	if b.usageEvents > 0 {
		usage.Set("source", "reported")
		effective = b.reported
	}
	for _, k := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		n, _ := effective.Value(k).(int64)
		usage.Set(k, n)
	}
	if usage.Value("total_tokens").(int64) == 0 {
		usage.Set("total_tokens", usage.Value("input_tokens").(int64)+usage.Value("output_tokens").(int64))
	}
	for _, k := range []string{"cache_read_input_tokens", "cached_input_tokens", "reasoning_tokens"} {
		if v, ok := b.reported.Get(k); ok {
			usage.Set(k, v)
		}
	}
	count := func(pred func(*pyjson.Object) bool) int64 {
		n := int64(0)
		for _, it := range items {
			if pred(it) {
				n++
			}
		}
		return n
	}
	isType := func(t string) func(*pyjson.Object) bool {
		return func(it *pyjson.Object) bool { return it.Value("type") == t }
	}
	stats := pyjson.NewObject(
		"messages", count(isType("message")),
		"user_messages", count(func(it *pyjson.Object) bool { return it.Value("type") == "message" && it.Value("role") == "user" }),
		"assistant_messages", count(func(it *pyjson.Object) bool { return it.Value("type") == "message" && it.Value("role") == "assistant" }),
		"tools", count(isType("tool")),
		"permissions", count(isType("permission")),
		"pending_permissions", count(func(it *pyjson.Object) bool {
			return it.Value("type") == "permission" && it.Value("status") == "pending"
		}),
		"errors", count(isType("error")),
		"tokens", usage.Value("total_tokens"))
	last := pyjson.NewObject()
	if len(items) > 0 {
		last = items[len(items)-1]
	}
	lastText := []rune(strOr(last.Value("text")))
	if len(lastText) > 80 {
		lastText = lastText[len(lastText)-80:]
	}
	signature := strings.Join([]string{in.SessionID, in.Status, strconv.Itoa(len(items)),
		pyjson.Str(stats.Value("tools")), pyjson.Str(stats.Value("errors")),
		strOr(last.Value("id")), strOr(last.Value("at")), strOr(last.Value("status")),
		string(lastText), pyjson.Str(usage.Value("total_tokens")), permissionSignature(items)}, "|")
	list := make([]any, len(items))
	for i, it := range items {
		list[i] = it
	}
	return pyjson.NewObject("session", in.SessionID, "status", in.Status, "active", active,
		"generated_at", NowISO(), "items", list, "stats", stats, "usage", usage, "signature", signature)
}

func permissionSignature(items []*pyjson.Object) string {
	var parts []string
	for _, it := range items {
		if it.Value("type") == "permission" {
			parts = append(parts, pyjson.Str(it.Value("request_id"))+":"+pyjson.Str(it.Value("status")))
		}
	}
	return strings.Join(parts, ",")
}

func classify(name string) string {
	suffix := pytext.Lower(pySuffix(name))
	switch {
	case imageSuffixes[suffix]:
		return "image"
	case videoSuffixes[suffix]:
		return "video"
	case audioSuffixes[suffix]:
		return "audio"
	case suffix == ".pdf":
		return "pdf"
	case documentSuffixes[suffix]:
		return "document"
	}
	return "other"
}

// isoMtime is datetime.fromtimestamp(st_mtime, utc).isoformat(timespec="seconds").
func isoMtime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05") + "+00:00"
}

// sortedChildren is sorted(folder.iterdir(), key=(not is_dir, name.lower())).
func sortedChildren(folder string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, err
	}
	isDir := func(e os.DirEntry) bool {
		fi, err := os.Stat(filepath.Join(folder, e.Name()))
		return err == nil && fi.IsDir()
	}
	dirs := make(map[string]bool, len(entries))
	for _, e := range entries {
		dirs[e.Name()] = isDir(e)
	}
	// os.ReadDir sorts by name; Python's iterdir order breaks key ties.
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := dirs[entries[i].Name()], dirs[entries[j].Name()]
		if di != dj {
			return di
		}
		return pytext.Lower(entries[i].Name()) < pytext.Lower(entries[j].Name())
	})
	return entries, nil
}

func isSymlink(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// IndexWorkspace is index_workspace.
func IndexWorkspace(root string, guess func(string) string) *pyjson.Object {
	media, documents, other := []any{}, []any{}, []any{}
	truncated := false
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return pyjson.NewObject("media", media, "documents", documents, "other", other, "truncated", false, "count", int64(0))
	}
	var walk func(folder string, depth int)
	walk = func(folder string, depth int) {
		if depth > maxWorkspaceDepth || truncated {
			return
		}
		children, err := sortedChildren(folder)
		if err != nil {
			return
		}
		for _, child := range children {
			if len(media)+len(documents)+len(other) >= maxWorkspaceFiles {
				truncated = true
				return
			}
			path := filepath.Join(folder, child.Name())
			if isSymlink(path) || strings.HasPrefix(child.Name(), ".") {
				continue
			}
			fi, err := os.Stat(path)
			if err != nil {
				continue
			}
			if fi.IsDir() {
				walk(path, depth+1)
				continue
			}
			rel, _ := filepath.Rel(root, path)
			kind := classify(child.Name())
			mime := guess(child.Name())
			if mime == "" {
				mime = "application/octet-stream"
			}
			entry := pyjson.NewObject("name", child.Name(), "path", filepath.ToSlash(rel), "size", fi.Size(),
				"mtime", isoMtime(fi.ModTime()), "kind", kind, "mime", mime,
				"text", (kind == "document" || kind == "other") && documentSuffixes[pytext.Lower(pySuffix(child.Name()))],
				"language", LanguageFor(child.Name()))
			switch kind {
			case "image", "video", "audio":
				media = append(media, entry)
			case "document", "pdf":
				documents = append(documents, entry)
			default:
				other = append(other, entry)
			}
		}
	}
	walk(root, 0)
	sort.SliceStable(media, func(i, j int) bool {
		a, b := media[i].(*pyjson.Object), media[j].(*pyjson.Object)
		sa, sb := a.Value("size").(int64), b.Value("size").(int64)
		if sa != sb {
			return sa > sb
		}
		return pytext.Lower(a.Value("name").(string)) < pytext.Lower(b.Value("name").(string))
	})
	sort.SliceStable(documents, func(i, j int) bool {
		return pytext.Lower(documents[i].(*pyjson.Object).Value("path").(string)) <
			pytext.Lower(documents[j].(*pyjson.Object).Value("path").(string))
	})
	return pyjson.NewObject("media", media, "documents", documents, "other", other, "truncated", truncated,
		"count", int64(len(media)+len(documents)+len(other)))
}
