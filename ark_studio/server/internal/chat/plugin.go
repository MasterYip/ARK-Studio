package chat

// The ai:chat permission of user view plugins (Go only, decision D-0003,
// docs/development/plugin-api.md): one model conversation for a plugin,
// with the Assistant's tool-call model, except that nothing is written.
// Read tools (get_context, search_units, list_units, get_unit) run against
// the project; the write tools (create_unit, update_unit, link_units) only
// become *proposals*, returned as node:write operations that the plugin
// must send through node:write, where the user confirms them.  No chat
// history is read or stored.

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// PluginRequest is one validated ai:chat call.
type PluginRequest struct {
	Plugin   string // the plugin id
	Messages []any  // {role: user|assistant, content}, the last one from the user
	System   string // the plugin's own instructions (may be empty)
	Tools    bool   // offer the tools (read tools run, write tools propose)
}

// ErrNoModel: no OpenAI-compatible model (API key) is configured for the
// user or project.
var ErrNoModel = errors.New("no assistant model is configured")

// MaxPluginProposals caps the proposals of one call (the node:write batch limit).
const MaxPluginProposals = 50

const pluginRounds = 6

var (
	pluginReadTools  = map[string]bool{"get_context": true, "search_units": true, "list_units": true, "get_unit": true}
	pluginWriteTools = map[string]bool{"create_unit": true, "update_unit": true, "link_units": true}
	uidPattern       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,7}-[0-9A-Za-z]{1,12}$`)
	refPattern       = regexp.MustCompile(`^\$n[0-9]{1,3}$`)
)

// pluginToolSpecs are the Assistant's tools a plugin conversation offers.
func pluginToolSpecs() []any {
	var out []any
	for _, spec := range toolSpecs() {
		name := pyjson.Str(spec.(*pyjson.Object).Value("function").(*pyjson.Object).Value("name"))
		if pluginReadTools[name] || pluginWriteTools[name] {
			out = append(out, spec)
		}
	}
	return out
}

func (e *Env) pluginSystemPrompt(req PluginRequest) string {
	lines := []string{
		"You are the AI model of the ARK Studio view plugin \"" + req.Plugin + "\", working for the signed-in user on the ARK project " + e.ProjectName + ".",
		"Use get_context, search_units, list_units and get_unit to read the project. Do not invent UIDs, units or links.",
		"create_unit, update_unit and link_units do NOT change the project: each call is a proposal that the user reviews and confirms in ARK afterwards. " +
			"create_unit answers with a ref such as $n1; use that ref as the source or target of link_units for the new unit.",
		"Only propose changes the user asked for. Respond in the user's language, concisely, in Markdown.",
	}
	if req.System != "" {
		lines = append(lines, "", "Instructions from the plugin:", req.System)
	}
	return strings.Join(lines, "\n")
}

func strField(args *pyjson.Object, key string) (string, bool) {
	v, ok := args.Value(key).(string)
	if !ok {
		return "", false
	}
	v = pytext.Strip(v)
	return v, v != ""
}

// proposal turns a write tool call into a node:write operation and the
// tool answer the model sees.  op is nil when the call is refused.
func proposal(name string, args *pyjson.Object, refs *int, known map[string]bool) (*pyjson.Object, *pyjson.Object) {
	refuse := func(msg string) (*pyjson.Object, *pyjson.Object) { return nil, pyjson.NewObject("error", msg) }
	nodeRef := func(key string) (string, bool) {
		v, ok := strField(args, key)
		return v, ok && (uidPattern.MatchString(v) || known[v])
	}
	op := pyjson.NewObject()
	copyFields := func(keys ...string) {
		for _, k := range keys {
			if v, ok := strField(args, k); ok {
				op.Set(k, v)
			}
		}
		if list, ok := args.Value("tags").([]any); ok {
			tags := []any{}
			for _, t := range list {
				if s, ok := t.(string); ok && pytext.Strip(s) != "" {
					tags = append(tags, pytext.Strip(s))
				}
			}
			op.Set("tags", tags)
		}
	}
	switch name {
	case "create_unit":
		title, ok := strField(args, "title")
		if !ok {
			return refuse("title is required")
		}
		*refs++
		ref := "$n" + strconv.Itoa(*refs)
		known[ref] = true
		op.Set("op", "create")
		op.Set("ref", ref)
		op.Set("title", title)
		copyFields("type", "body", "status", "importance")
		return op, pyjson.NewObject("proposed", true, "ref", ref,
			"note", "Not created yet: the user confirms it in ARK. Use "+ref+" as source or target in link_units.")
	case "update_unit":
		uid, ok := nodeRef("ref")
		if !ok {
			return refuse("ref must be a unit UID such as T-0001 (or a $n ref of a proposed unit)")
		}
		op.Set("op", "update")
		op.Set("uid", uid)
		copyFields("title", "status", "importance")
		if op.Len() <= 2 {
			return refuse("nothing to change")
		}
		return op, pyjson.NewObject("proposed", true, "note", "Not changed yet: the user confirms it in ARK.")
	case "link_units":
		source, ok1 := nodeRef("source")
		target, ok2 := nodeRef("target")
		relation, ok3 := strField(args, "relation")
		if !ok1 || !ok2 || !ok3 {
			return refuse("source and target must be unit UIDs (or $n refs) and relation is required")
		}
		op.Set("op", "link")
		op.Set("source", source)
		op.Set("target", target)
		op.Set("relation", relation)
		if note, ok := strField(args, "note"); ok {
			op.Set("note", note)
		}
		return op, pyjson.NewObject("proposed", true, "note", "Not linked yet: the user confirms it in ARK.")
	}
	return refuse("Unknown tool: " + name)
}

// PluginChat runs one plugin conversation.  The result is
// {message, proposals: [node:write ops], reads: [{name, args, ok}], model}.
func (e *Env) PluginChat(req PluginRequest) (*pyjson.Object, error) {
	config := e.Config
	if config == nil || !pyjson.Truthy(config.Value("api_key")) {
		return nil, ErrNoModel
	}
	messages := append([]any{pyjson.NewObject("role", "system", "content", e.pluginSystemPrompt(req))}, req.Messages...)
	var tools []any
	if req.Tools {
		tools = pluginToolSpecs()
	}
	proposals, reads := []any{}, []any{}
	refs, known := 0, map[string]bool{}
	result := func(message string) *pyjson.Object {
		return pyjson.NewObject("message", message, "proposals", proposals, "reads", reads, "model", orEmpty(config.Value("model")))
	}
	for round := 0; round < pluginRounds; round++ {
		roundTools := tools
		if round == pluginRounds-1 {
			roundTools = nil
		}
		message, err := deepseekChat(e.Ctx, messages, roundTools, config)
		if err != nil {
			return nil, err
		}
		m, ok := message.(*pyjson.Object)
		if !ok {
			return nil, deepseekErr("DeepSeek returned an unexpected response")
		}
		content := pytext.Strip(orEmpty(m.Value("content")))
		var calls []any
		callsV := m.Value("tool_calls")
		if pyjson.Truthy(callsV) {
			if calls, err = iterate(callsV); err != nil {
				return nil, err
			}
		} else {
			calls, content = parseDSMLToolCalls(content)
		}
		content = stripDSML(content)
		if len(calls) == 0 || len(tools) == 0 {
			return result(content), nil
		}
		var stored any = calls
		if pyjson.Truthy(callsV) {
			stored = callsV
		}
		messages = append(messages, pyjson.NewObject("role", "assistant", "content", content, "tool_calls", stored))
		for _, call := range calls {
			co, _ := call.(*pyjson.Object)
			if co == nil {
				co = pyjson.NewObject()
			}
			fn, _ := co.Value("function").(*pyjson.Object)
			if fn == nil {
				fn = pyjson.NewObject()
			}
			name, _ := fn.Value("name").(string)
			args := pyjson.NewObject()
			if raw, ok := fn.Value("arguments").(string); ok && raw != "" {
				if v, err := pyjson.Loads(raw); err == nil {
					if o, ok := v.(*pyjson.Object); ok {
						args = o
					}
				}
			} else if o, ok := fn.Value("arguments").(*pyjson.Object); ok {
				args = o
			}
			var answer *pyjson.Object
			switch {
			case pluginReadTools[name]:
				out, _, err := e.executeTool(name, args)
				if err != nil {
					return nil, err
				}
				answer = out
				reads = append(reads, pyjson.NewObject("name", name, "args", args, "ok", !out.Has("error")))
			case pluginWriteTools[name] && len(proposals) >= MaxPluginProposals:
				answer = pyjson.NewObject("error", "too many proposed changes in one reply")
			case pluginWriteTools[name]:
				var op *pyjson.Object
				op, answer = proposal(name, args, &refs, known)
				if op != nil {
					proposals = append(proposals, op)
				}
			default:
				answer = pyjson.NewObject("error", "Unknown tool: "+name)
			}
			id := co.Value("id")
			if !pyjson.Truthy(id) {
				id = name
			}
			messages = append(messages, toolMessage(id, answer))
		}
	}
	return result("I reached the tool-call limit before finishing."), nil
}
