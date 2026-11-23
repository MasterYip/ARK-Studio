// Package chat ports chat.py: the project Assistant chat harness (local
// deterministic commands, the DeepSeek/OpenAI-compatible tool loop with
// streaming, and the optional DeepSeek Harness subprocess).
package chat

import (
	"context"
	"os"
	"runtime"
	"sort"
	"strings"

	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyos"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/store"
)

// PyError is a Python exception that is not a StoreError: Type is the
// class name ("ValueError" and "DeepSeekError" are ValueErrors).
type PyError struct {
	Type, Message string
}

func (e *PyError) Error() string { return e.Message }

// IsValueError reports a ValueError (or StoreError) in Python terms.
func IsValueError(err error) bool {
	switch e := err.(type) {
	case *PyError:
		return e.Type == "ValueError" || e.Type == "DeepSeekError"
	case *assistcfg.Error:
		return e.Type == "ValueError"
	case *store.Error, *pyerr.ValueError:
		return true
	}
	return false
}

func valueErr(msg string) error { return &PyError{"ValueError", msg} }

// Env is one chat request: the project, its store and the effective config.
type Env struct {
	Ctx         context.Context
	Store       *store.Store
	ProjectID   string
	ProjectName string
	ProjectPath string
	Config      *pyjson.Object // the effective assistant config (App.effective_assistant)
	Infohub     *pyjson.Object // the InfoHub UI context or nil
	AgentToken  string
	BaseURL     string
	HistoryUser *string
	// HistoryLegacy adds the pre-v10 rows (empty user): local mode only.
	HistoryLegacy bool
	Who           store.Who
	// Scope is the person's other projects (nil: the current project only).
	Scope *Scope
}

var typeWords = map[string]string{
	"idea": "idea", "想法": "idea", "点子": "idea",
	"task": "task", "todo": "task", "任务": "task", "待办": "task",
	"claim": "claim", "论点": "claim", "主张": "claim",
	"question": "question", "问题": "question",
	"method": "method", "方法": "method",
	"experiment": "experiment", "实验": "experiment", "实验设计": "experiment",
	"related work": "related_work", "related_work": "related_work", "相关工作": "related_work",
	"story": "story", "section": "story", "故事": "story", "章节": "story",
	"source": "source", "来源": "source",
	"note": "note", "笔记": "note",
}

var typeLabel = map[string]string{
	"idea": "idea", "task": "task", "claim": "claim", "question": "question",
	"method": "method", "experiment": "experiment", "related_work": "related work",
	"story": "story section", "source": "source", "note": "note",
}

func labelOf(t any) string {
	s := pyjson.Str(t)
	if l, ok := typeLabel[s]; ok {
		if _, isStr := t.(string); isStr {
			return l
		}
	}
	return s
}

// isAbs is ntpath.isabs (Python 3.10) on Windows, posixpath.isabs elsewhere.
func isAbs(p string) bool {
	if runtime.GOOS != "windows" {
		return strings.HasPrefix(p, "/")
	}
	n := strings.ReplaceAll(p, "/", `\`)
	if strings.HasPrefix(n, `\\?\`) {
		return true
	}
	rest := n
	if len(n) >= 2 && n[1] == ':' {
		rest = n[2:]
	} else if strings.HasPrefix(n, `\\`) && !strings.HasPrefix(n, `\\\`) {
		// UNC: \\server\share is the drive.
		i := strings.Index(n[2:], `\`)
		if i < 0 {
			return false
		}
		j := strings.Index(n[2+i+1:], `\`)
		if i == 0 || j == 0 {
			rest = n
		} else if j < 0 {
			rest = ""
		} else {
			rest = n[2+i+1+j:]
		}
	}
	return rest != "" && rest[0] == '\\'
}

// HarnessStatus is harness_status(config); config nil reads the global
// configuration (deepseek_config()).
func HarnessStatus(config *pyjson.Object) (*pyjson.Object, error) {
	forced := pytext.Lower(assistcfg.EnvValue("ARK_CHAT_MODE"))
	if config == nil {
		var err error
		if config, err = assistcfg.Load(); err != nil {
			return nil, err
		}
	}
	dsh := pytext.Strip(os.Getenv("ARK_DSH_COMMAND"))
	if forced == "local" {
		dsh = ""
	}
	if dsh != "" && forced != "deepseek" {
		parts, err := pyos.ShlexSplit(dsh, false)
		if err != nil {
			return nil, valueErr(err.Error())
		}
		if len(parts) > 0 && (pyos.Which(parts[0], "", false) != "" || isAbs(parts[0])) {
			return pyjson.NewObject("mode", "dsh", "label", "DeepSeek Harness", "command", dsh, "available", true,
				"note", "Writes through the project API; runs with the .ark folder as cwd."), nil
		}
		return pyjson.NewObject("mode", "local", "label", "Local ARK harness", "command", dsh, "available", true,
			"note", "ARK_DSH_COMMAND is configured but not on PATH; using the local harness."), nil
	}
	if pyjson.Truthy(config.Value("api_key")) && forced != "local" {
		return pyjson.NewObject("mode", "deepseek", "label", "DeepSeek API · "+pyjson.Str(config.Value("model")),
			"model", config.Value("model"), "reasoning_effort", config.Value("reasoning_effort"), "available", true,
			"note", "OpenAI-compatible API from Settings → Assistant API; server-side tool calling with reasoning."), nil
	}
	return pyjson.NewObject("mode", "local", "label", "Local ARK harness", "available", true,
		"note", "No DEEPSEEK_API_KEY / DSH command is configured, so this is the deterministic local tool parser, not a model."), nil
}

func suggestions() []any {
	return []any{"What should I do next?",
		"Create idea: A learned recovery policy | Test it against the fixed schedule.",
		"Search scaling"}
}

// reply is _reply: the harness label comes from the global configuration.
func reply(message string, tools []any, refresh bool, sugg []any) (*pyjson.Object, error) {
	status, err := HarnessStatus(nil)
	if err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []any{}
	}
	if sugg == nil {
		sugg = []any{}
	}
	return pyjson.NewObject("message", message, "tools", tools, "refresh", refresh, "suggestions", sugg,
		"harness", status.Value("label")), nil
}

func tool(name string, args *pyjson.Object, result any, ok bool) *pyjson.Object {
	return pyjson.NewObject("name", name, "args", args, "ok", ok, "result", result)
}

const helpText = "**Project tools**\n" +
	"- `create idea: title | body` (also task, claim, question, method, experiment, related work)\n" +
	"- `link T-0001 supports C-0001` (any ARK relation)\n" +
	"- `mark T-0001 done`\n" +
	"- `search scaling`\n" +
	"- `dispatch T-0001`\n" +
	"- `summary` or `what should I do next`\n\n" +
	"Use `Ctrl+J` from anywhere to open this chat."

var (
	reHelp    = compile(`^(help|what can you do|commands|帮助|你能做什么|怎么用)`, 0)
	reSummary = compile(`^(summary|status|overview|project status|what should i do next|next steps|`+
		`总结|状态|进度|下一步|接下来)`, 0)
	reWords   = compile(`[\w一-鿿]{2,}`, 0)
	reCreate1 = compile(`^(?:create|add|new|make|capture)\s+(?:an?\s+|a\s+)?`+
		`(idea|task|todo|claim|question|method|experiment|related work|related_work|story|section|source|note)`+
		`\b(?:\s*(?:about|on|for|titled|:|-))?\s*(.*)$`, reI|reS)
	reCreate2 = compile(`^(idea|task|todo|claim|question|method|experiment|source|note)\s*[:\-]\s*(.+)$`, reI|reS)
	reCreate3 = compile(`^(?:新建|创建|添加|记下|记录|加)(?:一个|一条|个)?\s*`+
		`(想法|点子|任务|待办|论点|主张|问题|方法|实验设计|实验|相关工作|故事|章节|来源|笔记)?`+
		`[\s:：，,]*(.+)$`, reS)
	reDue      = compile(`\bdue\s+(\d{4}-\d{2}-\d{2})\b`, reI)
	reDueStrip = compile(`\s*\bdue\s+\d{4}-\d{2}-\d{2}\b`, reI)
	reSearch   = compile(`^(?:search|find|look for|show me|grep|查找|搜索|找)\s*[:：]?\s*(.+)$`, reI|reS)
	reLink2    = compile(`\b(?:link|connect|relate)\s+([A-Za-z]+-\d+|\d+)\s+(?:to\s+|with\s+)?`+
		`([A-Za-z]+-\d+|\d+)(?:\s+(?:as|via|with)\s+([a-z_]+))?`, reI)
	reStatus1   = compile(`^(?:mark|set|update)\s+([A-Za-z]+-\d+|\d+)\s+(?:as\s+|to\s+)?([a-z_]+)$`, reI)
	reStatus2   = compile(`^(?:把|将)?\s*([A-Za-z]+-\d+|\d+)\s*(?:标记为|设为|改成|状态改为)\s*([a-z_]+)$`, reI)
	reDispatch  = compile(`^(?:dispatch|hand off|派发|分配)\s+([A-Za-z]+-\d+|\d+)$`, reI)
	reLink1     rx
	relationSet = map[string]bool{}
	statusSet   = map[string]bool{}
)

func init() {
	rels := append([]string(nil), store.Relations...)
	sort.SliceStable(rels, func(i, j int) bool { return pytext.Len(rels[i]) > pytext.Len(rels[j]) })
	reLink1 = compile(`\b([A-Za-z]+-\d+|\d+)\s+(`+strings.Join(rels, "|")+`)\s+([A-Za-z]+-\d+|\d+)\b`, reI)
	for _, r := range store.Relations {
		relationSet[r] = true
	}
	for _, s := range store.KnownStatuses {
		statusSet[s] = true
	}
}

var stopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "this": true, "that": true,
	"from": true, "about": true, "what": true, "how": true, "can": true, "you": true, "please": true,
	"help": true, "project": true, "ark": true, "一个": true, "帮我": true, "可以": true, "项目": true}

// stripChars is str.strip(chars).
func stripChars(s, chars string) string {
	return strings.TrimFunc(s, func(r rune) bool { return strings.ContainsRune(chars, r) })
}

func listFilters(kv ...any) store.Filters {
	f := store.Filters{}
	for i := 0; i < len(kv); i += 2 {
		switch kv[i] {
		case "q":
			f.Q = kv[i+1]
		case "limit":
			f.Limit = kv[i+1].(int64)
		case "sort":
			f.Sort = kv[i+1].(string)
		case "types":
			f.Types = []any{kv[i+1]}
		case "statuses":
			f.Statuses = []any{kv[i+1]}
		case "importance":
			f.Importance = []any{kv[i+1]}
		}
	}
	return f
}

func (e *Env) list(f store.Filters) (*pyjson.Object, error) {
	return e.Store.ListUnits(e.Ctx, f, false)
}

func items(o *pyjson.Object) []any {
	switch v := o.Value("items").(type) {
	case []any:
		return v
	case []*pyjson.Object:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = x
		}
		return out
	}
	return nil
}

func obj(v any) *pyjson.Object {
	if o, ok := v.(*pyjson.Object); ok {
		return o
	}
	return pyjson.NewObject()
}

// runLocal is _run_local.
func (e *Env) runLocal(text string) (*pyjson.Object, error) {
	normalized := stripChars(pytext.Strip(text), "。.!！?？")
	lowered := pytext.Lower(normalized)
	if reHelp.search(lowered) != nil {
		return reply(helpText, nil, false, suggestions())
	}
	if reSummary.search(lowered) != nil {
		return e.summary()
	}
	if src, dst, rel, ok := parseLink(normalized); ok {
		return e.link(src, dst, rel)
	}
	if m := reStatus1.search(normalized); m != nil {
		return e.setStatus(m.group(1), pytext.Lower(m.group(2)))
	}
	if m := reStatus2.search(normalized); m != nil {
		return e.setStatus(m.group(1), pytext.Lower(m.group(2)))
	}
	if m := reDispatch.search(normalized); m != nil {
		return e.dispatch(m.group(1))
	}
	if m := reSearch.search(normalized); m != nil {
		return e.search(pytext.Strip(m.group(1)))
	}
	if typ, value, ok := parseCreate(normalized); ok {
		return e.create(typ, value)
	}
	var words []string
	for _, w := range reWords.findall(lowered) {
		if !stopWords[w] {
			words = append(words, w)
		}
	}
	if len(words) > 0 {
		found, err := e.list(listFilters("q", strings.Join(words[:min(4, len(words))], " "), "limit", int64(6)))
		if err != nil {
			return nil, err
		}
		if list := items(found); len(list) > 0 {
			var lines []string
			for _, it := range list {
				u := obj(it)
				lines = append(lines, "- **"+pyjson.Str(u.Value("uid"))+"** "+pyjson.Str(u.Value("title"))+" ("+labelOf(u.Value("type"))+")")
			}
			return reply("I searched the project for your message.\n\n"+strings.Join(lines, "\n")+"\n\n"+
				"I can also create, link, mark, or dispatch nodes. Say `help` for the command list.", nil, false, suggestions())
		}
	}
	missingKey := ""
	status, err := HarnessStatus(e.Config)
	if err != nil {
		return nil, err
	}
	effective := e.Config
	if effective == nil {
		if effective, err = assistcfg.Load(); err != nil {
			return nil, err
		}
	}
	dshCommand := pytext.Strip(os.Getenv("ARK_DSH_COMMAND"))
	if status.Value("mode") == "local" && pytext.Strip(orEmpty(effective.Value("api_key"))) == "" && dshCommand == "" {
		missingKey = "**No model API key is configured.** This reply comes from the local " +
			"deterministic harness, not a language model. Add an API key in " +
			"Settings → Assistant API for model-backed replies.\n\n"
	}
	return reply(missingKey+"I don't have a matching tool for that yet. I can work directly on this project:\n\n"+helpText,
		nil, false, suggestions())
}

// orEmpty is str(v or "").
func orEmpty(v any) string {
	if pyjson.Truthy(v) {
		return pyjson.Str(v)
	}
	return ""
}

func (e *Env) summary() (*pyjson.Object, error) {
	stats, err := e.Store.Stats(e.Ctx)
	if err != nil {
		return nil, err
	}
	actions, err := e.Store.NextActions(e.Ctx)
	if err != nil {
		return nil, err
	}
	byType := obj(stats.Value("by_type"))
	count := func(k string) string {
		if v, ok := byType.Get(k); ok {
			return pyjson.Str(v)
		}
		return "0"
	}
	lines := []string{
		"**" + pyjson.Str(stats.Value("total")) + " units · " + pyjson.Str(stats.Value("links")) + " links · " +
			pyjson.Str(obj(stats.Value("tasks")).Value("open")) + " open tasks**",
		count("idea") + " ideas · " + count("task") + " tasks · " + count("claim") + " claims · " + count("experiment") + " experiments",
	}
	if len(actions) > 0 {
		lines = append(lines, "\n**Needs attention**")
		for _, a := range actions[:min(5, len(actions))] {
			o := obj(a)
			lines = append(lines, "- "+pyjson.Str(o.Value("title"))+" — "+pyjson.Str(o.Value("detail")))
		}
	}
	return reply(strings.Join(lines, "\n"), nil, false, suggestions())
}

func typeFromWord(word string) string {
	if t, ok := typeWords[pytext.Lower(pytext.Strip(word))]; ok {
		return t
	}
	return "idea"
}

func parseCreate(text string) (string, string, bool) {
	if m := reCreate1.search(text); m != nil {
		return typeFromWord(m.group(1)), pytext.Strip(m.group(2)), true
	}
	if m := reCreate2.search(text); m != nil {
		return typeFromWord(m.group(1)), pytext.Strip(m.group(2)), true
	}
	if m := reCreate3.search(text); m != nil {
		word := m.group(1)
		if word == "" {
			word = "idea"
		}
		return typeFromWord(word), pytext.Strip(m.group(2)), true
	}
	return "", "", false
}

func (e *Env) create(typeName, value string) (*pyjson.Object, error) {
	if value == "" {
		return reply("Tell me what the node should say, for example `create idea: Use a learned recovery policy`.", nil, false, nil)
	}
	title, body := value, ""
	if strings.Contains(value, "|") {
		a, b, _ := strings.Cut(value, "|")
		title, body = pytext.Strip(a), pytext.Strip(b)
	} else if strings.Contains(value, "\n") {
		a, b, _ := strings.Cut(value, "\n")
		title, body = pytext.Strip(a), pytext.Strip(b)
	}
	title = pytext.Prefix(stripChars(pytext.Strip(title), "。."), 240)
	var due any
	target := body
	if target == "" {
		target = title
	}
	if m := reDue.search(target); m != nil {
		due = m.group(1)
		title = reDueStrip.sub("", title)
	}
	unit, err := e.Store.CreateUnit(e.Ctx, pyjson.NewObject("type", typeName, "title", title, "body", body,
		"status", "open", "importance", "p2", "end_at", due, "origin", "assistant", "tags", []any{"origin:chat"}),
		"assistant:chat", e.Who)
	if err != nil {
		if se, ok := err.(*store.Error); ok {
			return reply("I couldn't create that node: "+se.Message, []any{tool("create_unit",
				pyjson.NewObject("type", typeName), se.Message, false)}, false, nil)
		}
		return nil, err
	}
	label := typeName
	if l, ok := typeLabel[typeName]; ok {
		label = l
	}
	return reply("Created **"+pyjson.Str(unit.Value("uid"))+"** — "+pyjson.Str(unit.Value("title"))+" ("+label+").",
		[]any{tool("create_unit", pyjson.NewObject("type", typeName, "title", title),
			pyjson.NewObject("uid", unit.Value("uid"), "id", unit.Value("id")), true)},
		true, []any{"Open it in the inspector", "Link it to a claim"})
}

func (e *Env) search(query string) (*pyjson.Object, error) {
	data, err := e.list(listFilters("q", query, "limit", int64(8)))
	if err != nil {
		return nil, err
	}
	list := items(data)
	if len(list) == 0 {
		return reply("No nodes matched `"+query+"`.", nil, false, suggestions())
	}
	lines := []string{"Found **" + pyjson.Str(data.Value("total")) + "** nodes for `" + query + "`:"}
	for _, it := range list {
		u := obj(it)
		lines = append(lines, "- **"+pyjson.Str(u.Value("uid"))+"** "+pyjson.Str(u.Value("title"))+" · "+
			labelOf(u.Value("type"))+" · "+pyjson.Str(u.Value("status")))
	}
	return reply(strings.Join(lines, "\n"), []any{tool("search_units", pyjson.NewObject("query", query),
		pyjson.NewObject("total", data.Value("total")), true)}, false, nil)
}

func cleanRef(v string) string { return stripChars(v, " .,;:，。；：") }

func parseLink(text string) (string, string, string, bool) {
	if m := reLink1.search(text); m != nil {
		return cleanRef(m.group(1)), cleanRef(m.group(3)), pytext.Lower(m.group(2)), true
	}
	if m := reLink2.search(text); m != nil {
		relation := "relates_to"
		if m.has[3] && m.group(3) != "" {
			relation = pytext.Lower(m.group(3))
		}
		if !relationSet[relation] {
			relation = "relates_to"
		}
		return cleanRef(m.group(1)), cleanRef(m.group(2)), relation, true
	}
	return "", "", "", false
}

func (e *Env) link(source, target, relation string) (*pyjson.Object, error) {
	l, err := e.Store.CreateLink(e.Ctx, source, target, relation, "Created from chat", "assistant:chat")
	args := pyjson.NewObject("source", source, "target", target, "relation", relation)
	if err != nil {
		if se, ok := err.(*store.Error); ok {
			return reply("I couldn't create that link: "+se.Message, []any{tool("link_units", args, se.Message, false)}, false, nil)
		}
		return nil, err
	}
	return reply("Linked **"+source+"** → **"+target+"** as `"+relation+"`.",
		[]any{tool("link_units", args, pyjson.NewObject("id", l.Value("id")), true)}, true, nil)
}

func (e *Env) setStatus(ref, status string) (*pyjson.Object, error) {
	if !statusSet[status] {
		sorted := append([]string(nil), store.KnownStatuses...)
		sort.Strings(sorted)
		return reply("`"+status+"` isn't an ARK status. Try: "+strings.Join(sorted, ", ")+".", nil, false, nil)
	}
	unit, err := e.Store.UpdateUnit(e.Ctx, ref, pyjson.NewObject("status", status), "assistant:chat", e.Who)
	if err != nil {
		if se, ok := err.(*store.Error); ok {
			return reply("I couldn't update "+ref+": "+se.Message, []any{tool("update_unit",
				pyjson.NewObject("uid", ref, "status", status), se.Message, false)}, false, nil)
		}
		return nil, err
	}
	uid := unit.Value("uid")
	return reply("**"+pyjson.Str(uid)+"** is now `"+pyjson.Str(unit.Value("status"))+"`.",
		[]any{tool("update_unit", pyjson.NewObject("uid", uid, "status", status), pyjson.NewObject("uid", uid), true)}, true, nil)
}

func (e *Env) dispatch(ref string) (*pyjson.Object, error) {
	prompt, err := dispatchPrompt(e.Ctx, e.Store, ref)
	if err != nil {
		if se, ok := err.(*store.Error); ok {
			return reply("I couldn't dispatch "+ref+": "+se.Message, nil, false, nil)
		}
		return nil, err
	}
	text := pyjson.Str(prompt.Value("prompt"))
	return reply("Dispatch contract for **"+ref+"** is ready. Copy it to the coding agent; this action does not launch a remote worker.\n\n```text\n"+
		pytext.Prefix(text, 1800)+"\n```", []any{tool("dispatch_task", pyjson.NewObject("uid", ref),
		pyjson.NewObject("prompt", prompt.Value("prompt")), true)}, false, nil)
}
