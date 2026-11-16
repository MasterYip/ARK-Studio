package sessionctx

// The pure helpers of session_context.py: provider JSONL normalisation,
// metadata sniffing, resume commands, compact context summaries, visibility
// and the session-context view of internal (task) sessions and their mirrors.

import (
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/textfile"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

const (
	MaxTitle     = 160
	MaxField     = 400
	MaxChunks    = 64
	MaxLinks     = 20
	SummaryLimit = 6000
)

var (
	// ExternalIDRe is EXTERNAL_ID_RE.
	ExternalIDRe = regexp.MustCompile(`\Aark-ses-[a-z0-9]{6,32}\z`)
	linkRe       = regexp.MustCompile(`\A[A-Za-z0-9_.:-]+\z`)
	nativeIDRe   = regexp.MustCompile(`\A[A-Za-z0-9._:-]{4,120}\z`)
	agents       = map[string]bool{"claude": true, "codex": true, "dsh": true, "other": true}
)

func cerr(message string, status int, code string) *Error {
	if status == 0 {
		status = 400
	}
	return &Error{Message: message, Status: status, Code: code}
}

// orStr is str(v or "").
func orStr(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}

// orAny is (v or def).
func orAny(v, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

// pyList is list(v or []) for a decoded JSON value.
func pyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return append([]any{}, x...)
	case string:
		out := []any{}
		for _, r := range x {
			out = append(out, string(r))
		}
		return out
	case *pyjson.Object:
		out := []any{}
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out
	}
	return []any{}
}

// iterDicts yields the dict members of `for part in (v or [])`.
func iterDicts(v any) []*pyjson.Object {
	var out []*pyjson.Object
	if list, ok := v.([]any); ok {
		for _, item := range list {
			if o, ok := item.(*pyjson.Object); ok {
				out = append(out, o)
			}
		}
	}
	return out
}

func pyIntOr0(v any) int64 {
	if !pyjson.Truthy(v) {
		return 0
	}
	n, err := store.PyInt(v)
	if err != nil {
		return 0
	}
	return n
}

func objOf(v any) *pyjson.Object {
	if o, ok := v.(*pyjson.Object); ok {
		return o
	}
	return pyjson.NewObject()
}

// Limits is limits(): the effective upload limits in bytes.
type Limits struct{ Chunk, Line, Session, Project int64 }

func envInt(name string, def int64) int64 {
	raw := pytext.Strip(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := pytext.Int(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// CurrentLimits reads the ARK_SESSION_* variables per call, like Python.
func CurrentLimits() Limits {
	return Limits{
		Chunk:   4 * 1024 * 1024,
		Line:    envInt("ARK_SESSION_LINE_KB", 256) * 1024,
		Session: envInt("ARK_SESSION_MAX_MB", 50) * 1024 * 1024,
		Project: envInt("ARK_SESSION_PROJECT_MAX_MB", 500) * 1024 * 1024,
	}
}

func (l Limits) Object() *pyjson.Object {
	return pyjson.NewObject("chunk_bytes", l.Chunk, "line_bytes", l.Line, "session_bytes", l.Session, "project_bytes", l.Project)
}

// clean is _clean.
func clean(v any, limit int) string {
	s := strings.ReplaceAll(orStr(v), "\x00", "")
	return pytext.Prefix(pytext.Strip(s), limit)
}

func cleanAgent(v any) string {
	agent := pytext.Lower(clean(v, 40))
	switch agent {
	case "claude-code", "claude_code", "anthropic":
		agent = "claude"
	case "codex-cli", "openai":
		agent = "codex"
	case "deepseek", "deepseek-harness":
		agent = "dsh"
	}
	if agents[agent] {
		return agent
	}
	if agent != "" {
		return "other"
	}
	return ""
}

// CleanLinks is clean_links.
func CleanLinks(v any) ([]any, error) {
	var items []any
	switch x := v.(type) {
	case string:
		for _, part := range strings.Split(x, ",") {
			items = append(items, pytext.Strip(part))
		}
	case []any:
		items = x
	default:
		return nil, cerr("linked_units must be a list of node ids", 400, "SESSION_LINKS_INVALID")
	}
	out := []any{}
	seen := map[string]bool{}
	for _, item := range items {
		uid := clean(item, 40)
		if uid != "" && linkRe.MatchString(uid) && !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	if len(out) > MaxLinks {
		out = out[:MaxLinks]
	}
	return out, nil
}

// CleanShared is clean_shared.
func CleanShared(v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, cerr("shared must be true or false", 400, "SESSION_SHARED_INVALID")
	}
	return b, nil
}

// --------------------------------------------------------------- JSONL helpers

var instructionPrefixes = []string{"<environment_context>", "<user_instructions>", "<permissions instructions>"}

// NormalizeLine is normalize_line (nil for bookkeeping records).
func NormalizeLine(obj *pyjson.Object) *pyjson.Object {
	etype := orStr(obj.Value("type"))
	if (etype == "user" || etype == "assistant") && isObj(obj.Value("message")) {
		if pyjson.Truthy(obj.Value("isMeta")) || pyjson.Truthy(obj.Value("isSidechain")) {
			return nil
		}
		return obj
	}
	if etype == "system" && orStr(obj.Value("level")) == "error" {
		var text any
		if e, ok := obj.Value("error").(*pyjson.Object); ok {
			text = e.Value("message")
		} else {
			text = orAny(obj.Value("error"), obj.Value("content"))
		}
		msg := "error"
		if pyjson.Truthy(text) {
			msg = pyjson.Str(text)
		}
		return pyjson.NewObject("type", "error", "message", msg)
	}
	if etype == "ark_truncated" {
		return pyjson.NewObject("type", "notice", "message", "[line of "+pyjson.Str(obj.Value("bytes"))+" bytes truncated by ARK]")
	}
	if item, ok := obj.Value("payload").(*pyjson.Object); etype == "response_item" && ok {
		itype := orStr(item.Value("type"))
		switch itype {
		case "message":
			role := orStr(item.Value("role"))
			if role != "user" && role != "assistant" {
				return nil
			}
			var texts []string
			for _, part := range iterDicts(item.Value("content")) {
				if pyjson.Truthy(part.Value("text")) {
					if t := orStr(part.Value("text")); pytext.Strip(t) != "" {
						texts = append(texts, t)
					}
				}
			}
			text := strings.Join(texts, "\n")
			if text == "" {
				return nil
			}
			lead := pytext.LStrip(text)
			for _, p := range instructionPrefixes {
				if strings.HasPrefix(lead, p) {
					return nil
				}
			}
			return pyjson.NewObject("role", role, "content", []any{pyjson.NewObject("type", "text", "text", text)})
		case "function_call", "custom_tool_call", "local_shell_call":
			args, has := item.Get("arguments")
			if !has {
				args, has = item.Get("input")
				if !has {
					args = item.Value("action")
				}
			}
			return pyjson.NewObject("type", "tool_call", "id", orAny(item.Value("call_id"), item.Value("id")),
				"name", orAny(item.Value("name"), itype), "arguments", args)
		case "function_call_output", "custom_tool_call_output":
			output := item.Value("output")
			if list, ok := output.([]any); ok {
				var parts []string
				for _, part := range list {
					if o, ok := part.(*pyjson.Object); ok {
						parts = append(parts, orStr(o.Value("text")))
					}
				}
				output = strings.Join(parts, "\n")
			}
			return pyjson.NewObject("type", "tool_result", "tool_call_id", item.Value("call_id"), "output", output)
		case "reasoning":
			var parts []string
			for _, part := range iterDicts(orAny(item.Value("summary"), []any{})) {
				parts = append(parts, orStr(part.Value("text")))
			}
			text := strings.Join(parts, "\n")
			if pytext.Strip(text) == "" {
				return nil
			}
			return pyjson.NewObject("type", "reasoning", "text", text)
		}
		return nil
	}
	switch etype {
	case "tool_call", "tool_result", "tool_use", "message", "error", "notice", "reasoning", "result":
		return obj
	}
	if role, ok := obj.Value("role").(string); ok && (role == "user" || role == "assistant") {
		return obj
	}
	return nil
}

func isObj(v any) bool { _, ok := v.(*pyjson.Object); return ok }

// TranscriptEntries is transcript_entries: stored JSONL lines -> stdout entries.
func TranscriptEntries(lines []string, fallbackAt any) []*pyjson.Object {
	var out []*pyjson.Object
	var lastAt any
	for _, raw := range lines {
		raw = pytext.Strip(raw)
		if raw == "" {
			continue
		}
		v, err := pyjson.Loads(raw)
		if err != nil {
			continue
		}
		obj, ok := v.(*pyjson.Object)
		if !ok {
			continue
		}
		mapped := NormalizeLine(obj)
		if mapped == nil {
			continue
		}
		if stamp := orAny(obj.Value("timestamp"), obj.Value("t")); pyjson.Truthy(stamp) {
			lastAt = pyjson.Str(stamp)
		}
		out = append(out, pyjson.NewObject("t", orAny(lastAt, fallbackAt), "line", string(pyjson.Dumps(mapped))))
	}
	return out
}

// SniffMetadata is sniff_metadata.
func SniffMetadata(objects []*pyjson.Object) map[string]string {
	found := map[string]string{}
	set := func(k, v string) {
		if _, has := found[k]; !has {
			found[k] = v
		}
	}
	claudeTypes := map[string]bool{"user": true, "assistant": true, "summary": true, "system": true, "attachment": true}
	for _, obj := range objects {
		etype := orStr(obj.Value("type"))
		payload, _ := obj.Value("payload").(*pyjson.Object)
		if payload == nil {
			payload = pyjson.NewObject()
		}
		switch {
		case etype == "session_meta" && payload.Len() > 0:
			set("agent", "codex")
			if id := orAny(payload.Value("id"), payload.Value("session_id")); pyjson.Truthy(id) {
				set("native_session_id", pyjson.Str(id))
			}
			if pyjson.Truthy(payload.Value("cwd")) {
				set("cwd", pyjson.Str(payload.Value("cwd")))
			}
		case etype == "turn_context" && pyjson.Truthy(payload.Value("model")):
			set("model", pyjson.Str(payload.Value("model")))
		case pyjson.Truthy(obj.Value("sessionId")) && claudeTypes[etype]:
			set("agent", "claude")
			set("native_session_id", pyjson.Str(obj.Value("sessionId")))
			if pyjson.Truthy(obj.Value("cwd")) {
				set("cwd", pyjson.Str(obj.Value("cwd")))
			}
			message, _ := obj.Value("message").(*pyjson.Object)
			if message != nil && etype == "assistant" && pyjson.Truthy(message.Value("model")) &&
				!strings.HasPrefix(pyjson.Str(message.Value("model")), "<") {
				set("model", pyjson.Str(message.Value("model")))
			}
		}
	}
	return found
}

// ResumeCommand is resume_command.
func ResumeCommand(agent, nativeID, cwd string) string {
	nativeID = pytext.Strip(nativeID)
	if nativeID == "" || !nativeIDRe.MatchString(nativeID) {
		return ""
	}
	var command string
	switch agent {
	case "claude":
		command = "claude --resume " + nativeID
	case "codex":
		command = "codex resume " + nativeID
	default:
		return ""
	}
	if cwd != "" {
		quoted := pyos.Quote(cwd)
		if runtime.GOOS == "windows" || strings.Contains(cwd, " ") {
			quoted = `"` + cwd + `"`
		}
		return "cd " + quoted + " && " + command
	}
	return command
}

// ContextSummary is context_summary.
func ContextSummary(record *pyjson.Object, items []any, limit int) string {
	head := []string{
		"## Context from ARK session `" + pyjson.Str(record.Value("id")) + "`",
		"- Title: " + pyjson.Str(orAny(record.Value("title"), "untitled")),
	}
	agentLine := "- Agent: " + pyjson.Str(orAny(record.Value("agent"), "unknown"))
	if pyjson.Truthy(record.Value("model")) {
		agentLine += " · model " + pyjson.Str(record.Value("model"))
	}
	head = append(head, agentLine)
	if pyjson.Truthy(record.Value("cwd")) {
		head = append(head, "- Working directory: "+pyjson.Str(record.Value("cwd")))
	}
	links := pyList(record.Value("linked_units"))
	if ref := record.Value("task_ref"); pyjson.Truthy(ref) {
		in := false
		for _, l := range links {
			if pyEqual(l, ref) {
				in = true
				break
			}
		}
		if !in {
			links = append([]any{ref}, links...)
		}
	}
	if len(links) > 0 {
		parts := make([]string, len(links))
		for i, l := range links {
			parts[i] = pyjson.Str(l)
		}
		head = append(head, "- Linked ARK nodes: "+strings.Join(parts, ", "))
	}
	head = append(head, "- This is a summary of the previous conversation; verify state before acting on it.")
	var messages, tools []*pyjson.Object
	for _, it := range items {
		item, ok := it.(*pyjson.Object)
		if !ok {
			continue
		}
		if item.Value("type") == "message" && pytext.Strip(orStr(item.Value("text"))) != "" {
			messages = append(messages, item)
		}
		if item.Value("type") == "tool" {
			tools = append(tools, item)
		}
	}
	if len(tools) > 0 {
		names := map[string]int{}
		for _, t := range tools {
			names[pyjson.Str(orAny(t.Value("name"), "tool"))]++
		}
		keys := make([]string, 0, len(names))
		for k := range names {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 8 {
			keys = keys[:8]
		}
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + "×" + itoa(names[k])
		}
		head = append(head, "- Tools used: "+strings.Join(parts, ", "))
	}
	budget := limit - pytext.Len(strings.Join(head, "\n")) - 40
	var body []string
	var firstUser *pyjson.Object
	for _, m := range messages {
		if m.Value("role") == "user" {
			firstUser = m
			break
		}
	}
	if firstUser != nil {
		text := pytext.Strip(pyjson.Str(firstUser.Value("text")))
		entry := "### Original request\n" + pytext.Prefix(text, 1200)
		if pytext.Len(text) > 1200 {
			entry += " …"
		}
		body = append(body, entry)
		budget -= pytext.Len(entry)
	}
	var recent []string
	for i := len(messages) - 1; i >= 0; i-- {
		item := messages[i]
		if item == firstUser {
			break
		}
		text := pytext.Strip(orStr(item.Value("text")))
		snippet := pytext.Prefix(text, 900)
		if pytext.Len(text) > 900 {
			snippet += " …"
		}
		entry := "**" + pyjson.Str(orAny(item.Value("role"), "assistant")) + ":** " + snippet
		if budget-pytext.Len(entry) < 0 {
			break
		}
		recent = append(recent, entry)
		budget -= pytext.Len(entry) + 2
	}
	if len(recent) > 0 {
		for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
			recent[i], recent[j] = recent[j], recent[i]
		}
		body = append(body, "### Latest turns\n"+strings.Join(recent, "\n\n"))
	}
	out := strings.Join(head, "\n")
	if len(body) > 0 {
		out += "\n\n" + strings.Join(body, "\n\n")
	}
	return pytext.Prefix(out, limit)
}

func itoa(n int) string { return big.NewInt(int64(n)).String() }

// pyEqual is == for decoded scalars (enough for node ids).
func pyEqual(a, b any) bool {
	return pyjson.Repr(a) == pyjson.Repr(b) && pyTypeClass(a) == pyTypeClass(b)
}

func pyTypeClass(v any) string {
	switch v.(type) {
	case string:
		return "str"
	case *pyjson.Object:
		return "dict"
	case []any:
		return "list"
	case nil:
		return "none"
	}
	return "num"
}

// ---------------------------------------------------------------- visibility

func ownerOf(record *pyjson.Object) string { return orStr(record.Value("owner")) }

// CanView is can_view.
func CanView(record *pyjson.Object, user string, admin bool) bool {
	if admin || pyjson.Truthy(record.Value("shared")) {
		return true
	}
	owner := ownerOf(record)
	return owner != "" && pytext.Lower(owner) == pytext.Lower(user)
}

// CanManage is can_manage.
func CanManage(record *pyjson.Object, user string, admin bool) bool {
	if admin {
		return true
	}
	owner := ownerOf(record)
	return owner != "" && pytext.Lower(owner) == pytext.Lower(user)
}

// WithAccess is with_access.
func WithAccess(record *pyjson.Object, user string, admin bool) *pyjson.Object {
	return record.Copy().Set("can_manage", CanManage(record, user, admin))
}

// ------------------------------------------------- internal transcript mirrors

func mtime(path string) (float64, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return float64(fi.ModTime().UnixNano()) / 1e9, true
}

// MirrorIsStale is mirror_is_stale.
func MirrorIsStale(projectPath, runtimeFolder string) (bool, error) {
	folder, err := MirrorFolder(projectPath, filepath.Base(runtimeFolder))
	if err != nil {
		return false, err
	}
	mirrored, ok := mtime(filepath.Join(folder, "meta.json"))
	if !ok {
		return true, nil
	}
	for _, name := range []string{"meta.json", "stdout.jsonl", "stderr.jsonl"} {
		if t, ok := mtime(filepath.Join(runtimeFolder, name)); ok && t > mirrored {
			return true, nil
		}
	}
	return false, nil
}

// MirrorIDs is mirror_ids.
func MirrorIDs(projectPath string) []string {
	root := filepath.Join(resolve(projectPath), SessionsDir, InternalDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		if fi, err := os.Stat(full); err == nil && fi.IsDir() && InternalIDRe.MatchString(e.Name()) {
			if mi, err := os.Stat(filepath.Join(full, "meta.json")); err == nil && mi.Mode().IsRegular() {
				out = append(out, e.Name())
			}
		}
	}
	sort.Strings(out)
	return out
}

// readText is Path.read_text(encoding="utf-8", errors=...) with universal newlines.
func readText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return textfile.Universal(pyurl.DecodeReplace(raw)), nil
}

var errNotFound = func() *Error { return cerr("session not found", 404, "SESSION_NOT_FOUND") }

// ReadMirrorMeta is read_mirror_meta.
func ReadMirrorMeta(projectPath, sessionID string) (*pyjson.Object, error) {
	folder, err := MirrorFolder(projectPath, sessionID)
	if err != nil {
		// ContextError is a ValueError: read_mirror_meta reports it as not found.
		return nil, errNotFound()
	}
	raw, err := os.ReadFile(filepath.Join(folder, "meta.json"))
	if err != nil {
		return nil, errNotFound()
	}
	v, err := pyjson.Loads(textfile.Universal(string(raw)))
	if err != nil {
		return nil, errNotFound()
	}
	meta, ok := v.(*pyjson.Object)
	if !ok {
		return nil, errNotFound()
	}
	return meta, nil
}

func logEntries(path string) []*pyjson.Object {
	text, err := readText(path)
	if err != nil {
		return nil
	}
	lines := pytext.SplitLines(text, false)
	if len(lines) > sessions.MaxSourceLines {
		lines = lines[len(lines)-sessions.MaxSourceLines:]
	}
	var out []*pyjson.Object
	for _, raw := range lines {
		v, err := pyjson.Loads(raw)
		if err != nil {
			continue
		}
		if o, ok := v.(*pyjson.Object); ok {
			out = append(out, o)
		}
	}
	return out
}

// MirrorPublic is mirror_public.
func MirrorPublic(projectPath, sessionID string) (*pyjson.Object, error) {
	meta, err := ReadMirrorMeta(projectPath, sessionID)
	if err != nil {
		return nil, err
	}
	folder, _ := MirrorFolder(projectPath, sessionID)
	payload := meta.Copy().Set("status", "stored").Set("platform", meta.Value("platform"))
	record := InternalPublic(payload, folder)
	record.Set("owner", orAny(meta.Value("actor"), ""))
	record.Set("shared", pyjson.Truthy(meta.Value("shared")))
	record.Set("copy", true)
	record.Set("active", false)
	record.Value("resume").(*pyjson.Object).Set("native", false)
	return record, nil
}

// MirrorTranscript is mirror_transcript.
func MirrorTranscript(projectPath, sessionID string) (*pyjson.Object, error) {
	meta, err := ReadMirrorMeta(projectPath, sessionID)
	if err != nil {
		return nil, err
	}
	folder, _ := MirrorFolder(projectPath, sessionID)
	data := sessions.BuildTranscript(sessions.TranscriptInput{
		SessionID: sessionID, Status: "stored", Prompt: orStr(meta.Value("prompt")),
		PromptAt: orAny(meta.Value("started_at"), meta.Value("created_at")),
		Stdout:   logEntries(filepath.Join(folder, "stdout.jsonl")),
		Stderr:   logEntries(filepath.Join(folder, "stderr.jsonl")),
		Platform: orStr(meta.Value("platform")),
	})
	data.Set("source", "internal")
	data.Set("copy", true)
	return data, nil
}

// UpdateMirror is update_mirror.
func UpdateMirror(projectPath, sessionID string, body *pyjson.Object) (*pyjson.Object, error) {
	meta, err := ReadMirrorMeta(projectPath, sessionID)
	if err != nil {
		return nil, err
	}
	if body.Has("title") {
		if t := clean(body.Value("title"), MaxTitle); t != "" {
			meta.Set("title", t)
		}
	}
	if body.Has("linked_units") {
		links, err := CleanLinks(body.Value("linked_units"))
		if err != nil {
			return nil, err
		}
		meta.Set("linked_units", links)
	}
	if body.Has("shared") {
		shared, err := CleanShared(body.Value("shared"))
		if err != nil {
			return nil, err
		}
		meta.Set("shared", shared)
	}
	folder, _ := MirrorFolder(projectPath, sessionID)
	if err := os.WriteFile(filepath.Join(folder, "meta.json"), textfile.Encode(string(pyjson.DumpsIndent(meta, 2))+"\n"), 0o644); err != nil {
		return nil, err
	}
	return MirrorPublic(projectPath, sessionID)
}

// InternalPublic is internal_public: the session-context view of a task
// session payload ("" folder for None).
func InternalPublic(payload *pyjson.Object, folder string) *pyjson.Object {
	platform := orStr(payload.Value("platform"))
	native := orStr(payload.Value("native_session_id"))
	worktree, _ := payload.Value("worktree").(*pyjson.Object)
	if worktree == nil {
		worktree = pyjson.NewObject()
	}
	cwd := orStr(worktree.Value("path"))
	if cwd == "" {
		if c := payload.Value("cwd"); c != nil && c != "." {
			cwd = orStr(c)
		}
	}
	var size int64
	if folder != "" {
		for _, name := range []string{"stdout.jsonl", "stderr.jsonl"} {
			if fi, err := os.Stat(filepath.Join(folder, name)); err == nil {
				size += fi.Size()
			}
		}
	}
	counts := objOf(orAny(payload.Value("counts"), pyjson.NewObject()))
	resumable := native != "" && contains(sessions.NativeResumePlatforms, platform)
	status := orStr(payload.Value("status"))
	command := ""
	if native != "" {
		command = ResumeCommand(platform, native, cwd)
	}
	return pyjson.NewObject(
		"id", payload.Value("id"),
		"source", "internal",
		"agent", platform,
		"agent_label", orAny(payload.Value("platform_label"), platform),
		"title", orAny(payload.Value("title"), ""),
		"model", orAny(payload.Value("model"), ""),
		"cwd", cwd,
		"native_session_id", native,
		"created_at", payload.Value("created_at"),
		"updated_at", orAny(orAny(payload.Value("last_activity_at"), payload.Value("ended_at")), payload.Value("created_at")),
		"uploaded_by", "",
		"bytes", size,
		"lines", pyIntOr0(counts.Value("stdout"))+pyIntOr0(counts.Value("stderr")),
		"redactions", int64(0),
		"linked_units", pyList(payload.Value("linked_units")),
		"task_ref", orAny(payload.Value("task_ref"), ""),
		"status", orAny(payload.Value("status"), ""),
		"owner", orAny(orAny(payload.Value("owner"), payload.Value("actor")), ""),
		"shared", pyjson.Truthy(payload.Value("shared")),
		"copy", false,
		"active", status == "starting" || status == "running" || status == "idle" || status == "stopping",
		"resume", pyjson.NewObject("native", resumable, "command", command, "cwd", cwd),
	)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
