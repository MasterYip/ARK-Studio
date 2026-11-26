package chat

// Scheduled assistant tasks (Go only, Q-0003 follow-up, 2026-11-26): one
// unattended run of a fixed prompt with the Assistant's tool-call loop.
// The server (internal/server/routes_schedules.go) supplies the project,
// the creating admin's model config, extra tools and the caps.  In suggest
// mode (dry run) no write tool changes the project: each call becomes a
// proposal, like the ai:chat permission of view plugins (plugin.go), and the
// server files them as one review unit.

import (
	"strconv"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
)

// ExtraTool is a tool the server adds to a scheduled run.
type ExtraTool struct {
	Name  string
	Spec  *pyjson.Object // fnSpec(...)
	Write bool           // changes the project (a proposal in suggest mode)
	Run   func(args *pyjson.Object) (*pyjson.Object, error)
}

// ScheduledRequest is one run.
type ScheduledRequest struct {
	Name, Prompt string
	Suggest      bool  // dry run: writes become proposals
	MaxSteps     int   // model calls
	MaxWrites    int   // writes (or proposals)
	MaxTokens    int64 // total tokens the provider reports (0: no cap)
	Extra        []ExtraTool
}

// ScheduledResult is what a run did.
type ScheduledResult struct {
	Message    string
	Proposals  []any    // suggest mode: {op, ...}
	Writes     []any    // apply mode: {name, args, ok}
	Reads      int      // read tool calls
	Touched    []string // uids written (apply) or named by proposals
	Steps      int
	Tokens     int64
	Stopped    string // "" | "steps" | "tokens" (a cap ended the run)
	Transcript []any  // the messages, for the run log
	Model      string
}

var scheduledReadTools = map[string]bool{"get_context": true, "search_units": true, "list_units": true, "get_unit": true}
var scheduledWriteTools = map[string]bool{"create_unit": true, "update_unit": true, "link_units": true}

func (e *Env) scheduledSystemPrompt(req ScheduledRequest) string {
	lines := []string{
		"You are the ARK Studio Assistant running the scheduled task \"" + req.Name + "\" on the ARK project " + e.ProjectName + ". Nobody is watching: work only from the task below and the tools.",
		"Read the project with the read tools before you change anything. Do not invent UIDs, units or links.",
		"Never remove tags, links or units unless the task says so. Prefer few, precise changes.",
	}
	if req.Suggest {
		lines = append(lines, "This is a dry run: the write tools do NOT change the project. Each call is a proposal that a person reviews in ARK afterwards. "+
			"create_unit answers with a ref such as $n1; use that ref in link_units for the new unit.")
	}
	lines = append(lines, "Finish with a short summary in the language of the task: what you found and what you changed (or proposed), with UIDs.",
		"", "Task:", req.Prompt)
	return strings.Join(lines, "\n")
}

func touch(list []string, uid string) []string {
	uid = pytext.Strip(uid)
	if uid == "" || strings.HasPrefix(uid, "$n") {
		return list
	}
	for _, x := range list {
		if x == uid {
			return list
		}
	}
	return append(list, uid)
}

// ScheduledRun runs one scheduled task.  A provider error ends it with the
// error; a cap ends it with Stopped set and the work done so far.
func (e *Env) ScheduledRun(req ScheduledRequest) (*ScheduledResult, error) {
	config := e.Config
	if config == nil || !pyjson.Truthy(config.Value("api_key")) {
		return nil, ErrNoModel
	}
	if req.MaxSteps <= 0 {
		req.MaxSteps = 8
	}
	if req.MaxWrites <= 0 {
		req.MaxWrites = 20
	}
	extra := map[string]ExtraTool{}
	var tools []any
	for _, spec := range toolSpecs() {
		name := pyjson.Str(spec.(*pyjson.Object).Value("function").(*pyjson.Object).Value("name"))
		if scheduledReadTools[name] || scheduledWriteTools[name] {
			tools = append(tools, spec)
		}
	}
	for _, t := range req.Extra {
		extra[t.Name] = t
		tools = append(tools, t.Spec)
	}
	res := &ScheduledResult{Model: orEmpty(config.Value("model")), Proposals: []any{}, Writes: []any{}}
	messages := []any{pyjson.NewObject("role", "system", "content", e.scheduledSystemPrompt(req)),
		pyjson.NewObject("role", "user", "content", "Run the task now.")}
	refs, known := 0, map[string]bool{}
	writes := 0
	finish := func(message string) *ScheduledResult {
		res.Message = message
		res.Transcript = messages
		return res
	}
	for {
		if res.Steps >= req.MaxSteps {
			res.Stopped = "steps"
			return finish("Stopped at the step limit (" + strconv.Itoa(req.MaxSteps) + " model calls)."), nil
		}
		if req.MaxTokens > 0 && res.Tokens >= req.MaxTokens {
			res.Stopped = "tokens"
			return finish("Stopped at the token limit (" + pyjson.Str(req.MaxTokens) + ")."), nil
		}
		roundTools := tools
		if res.Steps == req.MaxSteps-1 {
			roundTools = nil // the last call must answer
		}
		message, tokens, err := deepseekChatUsage(e.Ctx, messages, roundTools, config)
		res.Steps++
		res.Tokens += tokens
		if err != nil {
			res.Transcript = messages
			return res, err
		}
		m, ok := message.(*pyjson.Object)
		if !ok {
			return res, deepseekErr("DeepSeek returned an unexpected response")
		}
		content := pytext.Strip(orEmpty(m.Value("content")))
		var calls []any
		callsV := m.Value("tool_calls")
		if pyjson.Truthy(callsV) {
			if calls, err = iterate(callsV); err != nil {
				return res, err
			}
		} else {
			calls, content = parseDSMLToolCalls(content)
		}
		content = stripDSML(content)
		if len(calls) == 0 || roundTools == nil {
			messages = append(messages, pyjson.NewObject("role", "assistant", "content", content))
			return finish(content), nil
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
			answer, err := e.scheduledTool(req, res, extra, name, args, &writes, &refs, known)
			if err != nil {
				res.Transcript = messages
				return res, err
			}
			id := co.Value("id")
			if !pyjson.Truthy(id) {
				id = name
			}
			messages = append(messages, toolMessage(id, answer))
		}
	}
}

func (e *Env) scheduledTool(req ScheduledRequest, res *ScheduledResult, extra map[string]ExtraTool, name string, args *pyjson.Object,
	writes, refs *int, known map[string]bool) (*pyjson.Object, error) {
	t, isExtra := extra[name]
	isWrite := scheduledWriteTools[name] || (isExtra && t.Write)
	switch {
	case scheduledReadTools[name]:
		res.Reads++
		out, _, err := e.executeTool(name, args)
		return out, err
	case isExtra && !t.Write:
		res.Reads++
		out, err := t.Run(args)
		if err != nil {
			if toolCaught(err) {
				return pyjson.NewObject("error", errMessage(err)), nil
			}
			return nil, err
		}
		return out, nil
	case !isWrite:
		return pyjson.NewObject("error", "Unknown tool: "+name), nil
	case *writes >= req.MaxWrites:
		return pyjson.NewObject("error", "the write limit of this task ("+strconv.Itoa(req.MaxWrites)+") is reached; summarise and stop"), nil
	}
	*writes++
	if req.Suggest {
		var op, answer *pyjson.Object
		if isExtra {
			op = pyjson.NewObject("op", name, "args", args)
			answer = pyjson.NewObject("proposed", true, "note", "Not changed yet: a person confirms it in ARK.")
		} else {
			op, answer = proposal(name, args, refs, known)
		}
		if op != nil {
			res.Proposals = append(res.Proposals, op)
			for _, k := range []string{"uid", "source", "target", "ref"} {
				if v, ok := op.Value(k).(string); ok {
					res.Touched = touch(res.Touched, v)
				}
			}
			if a, ok := op.Value("args").(*pyjson.Object); ok {
				res.Touched = touch(res.Touched, orEmpty(a.Value("ref")))
			}
		}
		return answer, nil
	}
	var out *pyjson.Object
	var err error
	if isExtra {
		out, err = t.Run(args)
		if err != nil {
			if !toolCaught(err) {
				return nil, err
			}
			out = pyjson.NewObject("error", errMessage(err))
		}
	} else if out, _, err = e.executeTool(name, args); err != nil {
		return nil, err
	}
	ok := !out.Has("error")
	res.Writes = append(res.Writes, pyjson.NewObject("name", name, "args", args, "ok", ok))
	if ok {
		res.Touched = touch(res.Touched, orEmpty(out.Value("uid")))
		for _, k := range []string{"ref", "source", "target"} {
			res.Touched = touch(res.Touched, orEmpty(args.Value(k)))
		}
	}
	return out, nil
}
