package chat

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arkstudio/go/internal/platform/procx"
	"arkstudio/go/internal/platform/pyerr"

	"arkstudio/go/internal/assistant"
	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
)

var dispatchPrompt = assistant.Dispatch

func deepseekErr(msg string) error { return &PyError{"DeepSeekError", msg} }

// get is v.get(key) on a decoded JSON value (AttributeError unless a dict).
func get(v any, key string) (any, error) {
	o, ok := v.(*pyjson.Object)
	if !ok {
		return nil, &PyError{"AttributeError", "'" + pyerr.TypeName(v) + "' object has no attribute 'get'"}
	}
	return o.Value(key), nil
}

// iterate is `for x in v` over a decoded JSON value.
func iterate(v any) ([]any, error) {
	switch x := v.(type) {
	case []any:
		return x, nil
	case string:
		out := []any{}
		for _, r := range x {
			out = append(out, string(r))
		}
		return out, nil
	case *pyjson.Object:
		out := []any{}
		for _, k := range x.Keys() {
			out = append(out, k)
		}
		return out, nil
	}
	return nil, &PyError{"TypeError", "'" + pyerr.TypeName(v) + "' object is not iterable"}
}

func timeoutOf(config *pyjson.Object) time.Duration {
	n, err := assistcfg.PyInt(config.Value("timeout"))
	if err != nil || n <= 0 {
		n = assistcfg.DefaultTimeout
	}
	if f, ok := config.Value("timeout").(float64); ok && f > 0 {
		return time.Duration(f * float64(time.Second))
	}
	return time.Duration(n) * time.Second
}

// deadlineConn applies the socket timeout of urlopen(timeout=...) to every
// read and write (a per-operation timeout, not a total one).
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	c.Conn.SetReadDeadline(time.Now().Add(c.timeout)) //nolint:errcheck
	return c.Conn.Read(b)
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	c.Conn.SetWriteDeadline(time.Now().Add(c.timeout)) //nolint:errcheck
	return c.Conn.Write(b)
}

type connectError struct{ err error }

func (e *connectError) Error() string { return e.err.Error() }
func (e *connectError) Unwrap() error { return e.err }

func client(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, &connectError{err}
			}
			return &deadlineConn{conn, timeout}, nil
		},
		DisableKeepAlives:  true,
		DisableCompression: true,
	}
	return &http.Client{Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// urllib follows 301/302/303 (a POST becomes a GET) but raises
		// HTTPError for 307/308 on a POST.
		if req.Response != nil && (req.Response.StatusCode == 307 || req.Response.StatusCode == 308) {
			return http.ErrUseLastResponse
		}
		if len(via) > 10 {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}

// urlError is str(exc) of the URLError / OSError urlopen raised.
func urlError(err error) string {
	var ce *connectError
	var ne net.Error
	if errors.As(err, &ce) {
		if errors.As(err, &ne) && ne.Timeout() {
			return "<urlopen error timed out>"
		}
		return "<urlopen error " + procx.OSErrorText(ce.err) + ">"
	}
	if errors.As(err, &ne) && ne.Timeout() {
		return "timed out"
	}
	return "<urlopen error " + err.Error() + ">"
}

// request is urllib.request.urlopen(Request(base_url + "/chat/completions")).
func request(ctx context.Context, body *pyjson.Object, config *pyjson.Object, accept string) (*http.Response, error) {
	url := pyjson.Str(config.Value("base_url")) + "/chat/completions"
	if !strings.Contains(url, ":") {
		return nil, valueErr("unknown url type: " + pyjson.Repr(url))
	}
	timeout := timeoutOf(config)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(pyjson.Dumps(body)))
	if err != nil {
		return nil, deepseekErr("<urlopen error unknown url type: " + strings.SplitN(url, ":", 2)[0] + ">")
	}
	req.Header.Set("Authorization", "Bearer "+pyjson.Str(config.Value("api_key")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Python-urllib/3.10")
	resp, err := client(timeout).Do(req)
	if err != nil {
		return nil, deepseekErr(urlError(err))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 800))
		detail := pytext.Strip(pyurl.DecodeReplace(raw))
		if detail == "" {
			detail = strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
		}
		return nil, deepseekErr("HTTP " + strconv.Itoa(resp.StatusCode) + ": " + detail)
	}
	return resp, nil
}

func requestBody(messages []any, tools []any, config *pyjson.Object, stream bool) *pyjson.Object {
	body := pyjson.NewObject("model", config.Value("model"), "messages", messages,
		"reasoning_effort", config.Value("reasoning_effort"), "temperature", 0.2, "max_tokens", int64(3000), "stream", stream)
	if len(tools) > 0 {
		body.Set("tools", tools)
		body.Set("tool_choice", "auto")
	}
	return body
}

func jsonError(text string) string {
	if pytext.Strip(text) == "" {
		return "Expecting value: line 1 column " + strconv.Itoa(pytext.Len(text)+1) + " (char " + strconv.Itoa(pytext.Len(text)) + ")"
	}
	return "Expecting value: line 1 column 1 (char 0)"
}

// deepseekChat is _deepseek_chat: the assistant message of one completion.
func deepseekChat(ctx context.Context, messages, tools []any, config *pyjson.Object) (any, error) {
	resp, err := request(ctx, requestBody(messages, tools, config, false), config, "application/json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, deepseekErr(urlError(err))
	}
	text := pyurl.DecodeStrict(raw)
	if text == nil {
		return nil, deepseekErr("'utf-8' codec can't decode the response")
	}
	data, err := pyjson.Loads(*text)
	if err != nil {
		return nil, deepseekErr(jsonError(*text))
	}
	unexpected := deepseekErr("DeepSeek returned an unexpected response")
	o, ok := data.(*pyjson.Object)
	if !ok {
		return nil, unexpected
	}
	choices, present := o.Get("choices")
	if !present {
		return nil, unexpected
	}
	var first any
	switch c := choices.(type) {
	case []any:
		if len(c) == 0 {
			return nil, unexpected
		}
		first = c[0]
	case string:
		return nil, unexpected // "x"[0]["message"] is a TypeError
	default:
		return nil, unexpected
	}
	fo, ok := first.(*pyjson.Object)
	if !ok {
		return nil, unexpected
	}
	msg, present := fo.Get("message")
	if !present {
		return nil, unexpected
	}
	return msg, nil
}

// isZero is `value != 0` being false in Python (0, 0.0, False).
func isZero(v any) bool {
	switch x := v.(type) {
	case int64:
		return x == 0
	case float64:
		return x == 0
	case bool:
		return !x
	}
	return false
}

// deepseekStream is _deepseek_stream: calls fn with every delta of choice 0.
func deepseekStream(ctx context.Context, messages, tools []any, config *pyjson.Object, fn func(delta any) error) error {
	resp, err := request(ctx, requestBody(messages, tools, config, true), config, "text/event-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	finished := false
	for {
		raw, rerr := reader.ReadBytes('\n')
		if len(raw) > 0 {
			text := pyurl.DecodeStrict(raw)
			if text == nil {
				return deepseekErr("'utf-8' codec can't decode the stream")
			}
			line := pytext.Strip(*text)
			if strings.HasPrefix(line, "data:") {
				payload := pytext.Strip(line[5:])
				if payload == "[DONE]" {
					finished = true
					break
				}
				chunk, jerr := pyjson.Loads(payload)
				if jerr != nil {
					return deepseekErr(jsonError(payload))
				}
				errV, err := get(chunk, "error")
				if err != nil {
					return err
				}
				if pyjson.Truthy(errV) {
					m, err := get(errV, "message")
					if err != nil {
						return err
					}
					msg := "AI endpoint error"
					if pyjson.Truthy(m) {
						msg = pyjson.Str(m)
					}
					return deepseekErr(msg)
				}
				choicesV, _ := get(chunk, "choices")
				var choices []any
				if pyjson.Truthy(choicesV) {
					if choices, err = iterate(choicesV); err != nil {
						return err
					}
				}
				for _, choice := range choices {
					co, ok := choice.(*pyjson.Object)
					if !ok {
						_, err := get(choice, "index")
						return err
					}
					if idx, present := co.Get("index"); present && !isZero(idx) {
						continue
					}
					delta := co.Value("delta")
					if !pyjson.Truthy(delta) {
						delta = pyjson.NewObject()
					}
					if err := fn(delta); err != nil {
						return err
					}
					finished = finished || co.Value("finish_reason") != nil
				}
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				return deepseekErr(urlError(rerr))
			}
			break
		}
	}
	if !finished {
		return deepseekErr("AI stream ended before the assistant finished")
	}
	return nil
}

var (
	reMarker  = compile(`<[\s/]*[|｜]`, 0)
	reSpace   = compile(`\s`, 0)
	rePartial = fullCompile(`[|｜]{0,2}(?:D(?:S(?:M(?:L)?)?)?)?`, 0)
	dsmlTag   = `<\s*/?\s*[|｜]\s*[|｜]\s*DSML\s*[|｜]\s*[|｜]\s*`
	reCalls   = compile(dsmlTag+`calls\s*>.*?`+dsmlTag+`calls\s*>`, reS|reI)
	reInvokeW = compile(dsmlTag+`invoke\s*[^>]*>.*?`+dsmlTag+`invoke\s*>`, reS|reI)
	reTag     = compile(dsmlTag+`[^>]*>`, reI)
	reLoose   = compile(`<\s*/?\s*[|｜]\s*[|｜]\s*DSML[^>\n]*>?`, reI)
	reLine    = compile(`^\s*[|｜]\s*[|｜]\s*DSML.*$`, reM|reI)
	reBlank   = compile(`\n{3,}`, 0)
	reInvoke  = compile(dsmlTag+`invoke\s+name\s*=\s*["']([^"']+)["'][^>]*>(.*?)`+dsmlTag+`invoke\s*>`, reS|reI)
	reParam   = compile(dsmlTag+`parameter\s+name\s*=\s*["']([^"']+)["']([^>]*)>(.*?)`+dsmlTag+`parameter\s*>`, reS|reI)
	reAnyTag  = compile(`<[^>]+>`, 0)
	reStrAttr = compile(`string\s*=\s*["']true["']`, reI)
)

// streamSafeContent is _stream_safe_content.
func streamSafeContent(raw string) string {
	s := []rune(raw)
	if m := reMarker.search(raw); m != nil {
		return string(s[:m.start])
	}
	start := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '<' {
			start = i
			break
		}
	}
	if start >= 0 {
		tail := strings.TrimLeft(reSpace.sub("", string(s[start+1:])), "/")
		if rePartial.search(tail) != nil {
			return string(s[:start])
		}
	}
	return raw
}

// stripDSML is _strip_dsml (and clean_message).
func stripDSML(text string) string {
	if !strings.Contains(text, "DSML") {
		return pytext.Strip(text)
	}
	v := reCalls.sub("", text)
	v = reInvokeW.sub("", v)
	v = reTag.sub("", v)
	v = reLoose.sub("", v)
	v = reLine.sub("", v)
	return pytext.Strip(reBlank.sub("\n\n", v))
}

// CleanMessage is chat.clean_message.
func CleanMessage(text string) string { return stripDSML(text) }

// parseDSMLToolCalls is _parse_dsml_tool_calls.
func parseDSMLToolCalls(text string) ([]any, string) {
	if !strings.Contains(text, "DSML") {
		return nil, text
	}
	var calls []any
	for index, m := range reInvoke.finditer(text) {
		name := pytext.Strip(m.group(1))
		args := pyjson.NewObject()
		for _, p := range reParam.finditer(m.group(2)) {
			key := pytext.Strip(p.group(1))
			attrs, raw := p.group(2), p.group(3)
			raw = pytext.Strip(html.UnescapeString(reAnyTag.sub("", raw)))
			if reStrAttr.search(attrs) != nil {
				args.Set(key, raw)
			} else if v, err := pyjson.Loads(raw); err == nil {
				args.Set(key, v)
			} else {
				args.Set(key, raw)
			}
		}
		calls = append(calls, pyjson.NewObject("id", "dsml-"+strconv.Itoa(index), "type", "function",
			"function", pyjson.NewObject("name", name, "arguments", string(pyjson.Dumps(args)))))
	}
	return calls, stripDSML(text)
}

// systemPrompt is _system_prompt.
func (e *Env) systemPrompt() (string, error) {
	stats, err := e.Store.Stats(e.Ctx)
	if err != nil {
		return "", err
	}
	actions, err := e.Store.NextActions(e.Ctx)
	if err != nil {
		return "", err
	}
	recentData, err := e.list(listFilters("limit", int64(12), "sort", "updated"))
	if err != nil {
		return "", err
	}
	recent := items(recentData)
	lines := []string{
		"You are ARK Studio's project assistant, working on the current ARK project.",
		"Think through the request before acting. Use the provided tools to inspect or modify the project.",
		"Do not invent UIDs, units or links. Search/read first when identifiers are unclear.",
		"Only create/update/link when the user asks for a change. Keep answers concise and concrete.",
		"ARK is the source of truth: use get_context, list_units, search_units and get_unit to inspect any project data. When you mention a unit, include its UID (for example S-0004); the UI makes UIDs clickable.",
		"Respond in the user's language unless they ask otherwise. Use Markdown.",
		"",
		"Project: " + e.ProjectName,
		"Project path: " + e.ProjectPath,
		"Stats: " + pyjson.Str(stats.Value("total")) + " units, " + pyjson.Str(stats.Value("links")) + " links, " +
			pyjson.Str(obj(stats.Value("tasks")).Value("open")) + " open tasks.",
		"Types: " + string(pyjson.Dumps(stats.Value("by_type"))),
	}
	if len(actions) > 0 {
		var titles []string
		for _, a := range actions[:min(6, len(actions))] {
			titles = append(titles, pyjson.Str(obj(a).Value("title")))
		}
		lines = append(lines, "Next actions: "+strings.Join(titles, "; "))
	}
	if len(recent) > 0 {
		var parts []string
		for _, u := range recent[:min(10, len(recent))] {
			o := obj(u)
			parts = append(parts, pyjson.Str(o.Value("uid"))+" "+pyjson.Str(o.Value("title")))
		}
		lines = append(lines, "Recent units: "+strings.Join(parts, "; "))
	}
	if e.Infohub != nil && e.Infohub.Len() > 0 {
		if path, err := infohubPath(e.Infohub); err == nil {
			visible := "True"
			if v, ok := e.Infohub.Get("visible"); ok && !pyjson.Truthy(v) {
				visible = "False"
			}
			lines = append(lines, "InfoHub is available at "+path+". Current InfoHub graph filters: "+
				string(pyjson.Dumps(infohubFilters(e.Infohub)))+" (visible in UI: "+visible+").")
			lines = append(lines, "Use get_infohub_context to see the topics currently in the InfoHub graph, "+
				"search_infohub to search the read-only database, and get_infohub_unit for details.")
		} else if !IsValueError(err) {
			return "", err
		}
	}
	lines = append(lines, "Use at most 4-6 tool calls; do not repeatedly read the same unit. After inspecting enough evidence, answer the user directly.")
	return strings.Join(lines, "\n"), nil
}

// history is the prompt history of both DeepSeek loops.
func (e *Env) history(text string, strip bool) ([]any, error) {
	prompt, err := e.systemPrompt()
	if err != nil {
		return nil, err
	}
	messages := []any{pyjson.NewObject("role", "system", "content", prompt)}
	rows, err := e.Store.ChatHistory(e.Ctx, 24, e.HistoryUser, e.HistoryLegacy)
	if err != nil {
		return nil, err
	}
	for _, item := range rows {
		role := item.Value("role")
		if role != "user" && role != "assistant" {
			continue
		}
		content := stripDSML(orEmpty(item.Value("content")))
		if strip {
			content = pytext.Strip(content)
		}
		if content != "" {
			messages = append(messages, pyjson.NewObject("role", role, "content", pytext.Prefix(content, 6000)))
		}
	}
	found := false
	for _, m := range messages[1:] {
		o := m.(*pyjson.Object)
		if o.Value("role") == "user" && o.Value("content") == text {
			found = true
			break
		}
	}
	if !found {
		messages = append(messages, pyjson.NewObject("role", "user", "content", text))
	}
	return messages, nil
}

func lastChars(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// toolMessage is the "tool" role reply with the JSON result (12,000 code points).
func toolMessage(id any, result *pyjson.Object) *pyjson.Object {
	return pyjson.NewObject("role", "tool", "tool_call_id", id, "content", pytext.Prefix(string(pyjson.Dumps(result)), 12000))
}

// runDeepseek is _run_deepseek.
func (e *Env) runDeepseek(text string, status *pyjson.Object) (*pyjson.Object, error) {
	config := e.Config
	if config == nil {
		var err error
		if config, err = assistcfg.Load(); err != nil {
			return nil, err
		}
	}
	if !pyjson.Truthy(config.Value("api_key")) {
		return nil, deepseekErr("DEEPSEEK_API_KEY is not configured")
	}
	messages, err := e.history(text, true)
	if err != nil {
		return nil, err
	}
	tools := toolSpecs()
	used := []any{}
	reasoning, refresh := "", false
	for round := 0; round < 8; round++ {
		var roundTools []any
		if round < 7 {
			roundTools = tools
		}
		message, err := deepseekChat(e.Ctx, messages, roundTools, config)
		if err != nil {
			return nil, err
		}
		contentV, err := get(message, "content")
		if err != nil {
			return nil, err
		}
		content := pytext.Strip(orEmpty(contentV))
		m := message.(*pyjson.Object)
		if rc := m.Value("reasoning_content"); pyjson.Truthy(rc) {
			reasoning = lastChars(pyjson.Str(rc), 12000)
		}
		var toolCalls []any
		callsV := m.Value("tool_calls")
		if pyjson.Truthy(callsV) {
			if toolCalls, err = iterate(callsV); err != nil {
				return nil, err
			}
		} else {
			toolCalls, content = parseDSMLToolCalls(content)
		}
		content = stripDSML(content)
		if len(toolCalls) == 0 {
			msg := content
			if msg == "" {
				msg = "I updated the project."
			}
			return pyjson.NewObject("message", msg, "tools", used, "refresh", refresh, "suggestions", suggestions(),
				"harness", status.Value("label"), "reasoning", reasoning), nil
		}
		var storedCalls any = toolCalls
		if pyjson.Truthy(callsV) {
			storedCalls = callsV
		}
		messages = append(messages, pyjson.NewObject("role", "assistant", "content", content, "tool_calls", storedCalls))
		for _, call := range toolCalls {
			fnV, err := get(call, "function")
			if err != nil {
				return nil, err
			}
			if !pyjson.Truthy(fnV) {
				fnV = pyjson.NewObject()
			}
			nameV, err := get(fnV, "name")
			if err != nil {
				return nil, err
			}
			if !pyjson.Truthy(nameV) {
				nameV = ""
			}
			argsRaw := fnV.(*pyjson.Object).Value("arguments")
			var args any = pyjson.NewObject()
			if pyjson.Truthy(argsRaw) {
				s, ok := argsRaw.(string)
				if !ok {
					return nil, &PyError{"TypeError", "the JSON object must be str, bytes or bytearray, not " + pyerr.TypeName(argsRaw)}
				}
				if v, err := pyjson.Loads(s); err == nil {
					args = v
				}
			}
			result, didWrite, err := e.executeTool(nameV, args)
			if err != nil {
				return nil, err
			}
			refresh = refresh || didWrite
			used = append(used, pyjson.NewObject("name", nameV, "args", args, "ok", !result.Has("error"), "result", result))
			id := call.(*pyjson.Object).Value("id")
			if !pyjson.Truthy(id) {
				id = nameV
			}
			messages = append(messages, toolMessage(id, result))
		}
	}
	return pyjson.NewObject("message", "I reached the tool-call limit before finishing. Check the tools above and ask me to continue.",
		"tools", used, "refresh", refresh, "suggestions", suggestions(), "harness", status.Value("label"), "reasoning", reasoning), nil
}

// Run is run_chat.
func (e *Env) Run(message string) (*pyjson.Object, error) {
	text := pytext.Strip(message)
	if text == "" {
		return nil, valueErr("message is required")
	}
	status, err := HarnessStatus(e.Config)
	if err != nil {
		return nil, err
	}
	if status.Value("mode") == "dsh" {
		result, err := e.runDSH(text, status)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
	}
	if status.Value("mode") == "deepseek" {
		result, err := e.runDeepseek(text, status)
		var pe *PyError
		if errors.As(err, &pe) && pe.Type == "DeepSeekError" {
			return reply("DeepSeek API error: "+pe.Message+"\n\nCheck `DEEPSEEK_API_KEY`, `DEEPSEEK_BASE_URL` and `DEEPSEEK_MODEL`.",
				nil, false, suggestions())
		}
		return result, err
	}
	result, err := e.runLocal(text)
	if err != nil {
		return nil, err
	}
	if status.Value("mode") == "dsh" {
		result.Set("harness", "Local ARK harness (DSH fallback)")
	}
	return result, nil
}

// Stream is stream_chat: emit receives every event; the result is the
// same as Run's.
func (e *Env) Stream(message string, emit func(*pyjson.Object)) (*pyjson.Object, error) {
	config := e.Config
	if config == nil {
		var err error
		if config, err = assistcfg.Load(); err != nil {
			return nil, err
		}
	}
	status, err := HarnessStatus(config)
	if err != nil {
		return nil, err
	}
	emit(pyjson.NewObject("type", "status", "phase", "thinking", "harness", status.Value("label")))
	if status.Value("mode") != "deepseek" {
		result, err := e.Run(message)
		if err != nil {
			return nil, err
		}
		if r := result.Value("reasoning"); pyjson.Truthy(r) {
			emit(pyjson.NewObject("type", "reasoning", "text", r))
		}
		content := result.Value("message")
		if !pyjson.Truthy(content) {
			content = ""
		}
		emit(pyjson.NewObject("type", "content", "text", content))
		return result, nil
	}
	messages, err := e.history(message, false)
	if err != nil {
		return nil, err
	}
	used := []any{}
	reasoning, answer, refresh := "", "", false
	for round := 0; round < 8; round++ {
		emit(pyjson.NewObject("type", "status", "phase", "thinking"))
		raw, sent, roundReasoning := "", "", ""
		var callKeys []any
		calls := map[string]*pyjson.Object{}
		prefix := ""
		if answer != "" {
			prefix = "\n\n"
		}
		var roundTools []any
		if round < 7 {
			roundTools = toolSpecs()
		}
		err := deepseekStream(e.Ctx, messages, roundTools, config, func(delta any) error {
			d, ok := delta.(*pyjson.Object)
			if !ok {
				_, err := get(delta, "reasoning_content")
				return err
			}
			if rc := d.Value("reasoning_content"); pyjson.Truthy(rc) {
				text := pyjson.Str(rc)
				if roundReasoning == "" && reasoning != "" {
					text = "\n\n" + text
				}
				roundReasoning += text
				reasoning += text
				emit(pyjson.NewObject("type", "reasoning", "text", text))
			}
			if c := d.Value("content"); pyjson.Truthy(c) {
				raw += pyjson.Str(c)
				safe := streamSafeContent(raw)
				if pytext.Len(safe) > pytext.Len(sent) {
					text := pytext.SliceFrom(safe, pytext.Len(sent))
					if sent == "" {
						text = prefix + text
					}
					sent = safe
					answer += text
					emit(pyjson.NewObject("type", "content", "text", text))
				}
			}
			tc := d.Value("tool_calls")
			var list []any
			if pyjson.Truthy(tc) {
				var err error
				if list, err = iterate(tc); err != nil {
					return err
				}
			}
			for _, call := range list {
				co, ok := call.(*pyjson.Object)
				if !ok {
					_, err := get(call, "index")
					return err
				}
				var index any = int64(0)
				if v, present := co.Get("index"); present {
					index = v
				}
				key := hashKey(index)
				slot, ok := calls[key]
				if !ok {
					slot = pyjson.NewObject("id", "", "type", "function", "function", pyjson.NewObject("name", "", "arguments", ""))
					calls[key] = slot
					callKeys = append(callKeys, key)
				}
				if id := co.Value("id"); pyjson.Truthy(id) {
					slot.Set("id", id)
				}
				fn := co.Value("function")
				if !pyjson.Truthy(fn) {
					fn = pyjson.NewObject()
				}
				for _, k := range []string{"name", "arguments"} {
					part, err := get(fn, k)
					if err != nil {
						return err
					}
					if !pyjson.Truthy(part) {
						part = ""
					}
					sf := slot.Value("function").(*pyjson.Object)
					cur, curOK := sf.Value(k).(string)
					ps, partOK := part.(string)
					if !curOK || !partOK {
						if curOK {
							return &PyError{"TypeError", "can only concatenate str (not \"" + pyerr.TypeName(part) + "\") to str"}
						}
						return &PyError{"TypeError", "unsupported operand type(s) for +=: '" + pyerr.TypeName(sf.Value(k)) + "' and '" + pyerr.TypeName(part) + "'"}
					}
					sf.Set(k, cur+ps)
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		legacy, clean := parseDSMLToolCalls(raw)
		var toolCalls []any
		for _, k := range callKeys {
			toolCalls = append(toolCalls, calls[k.(string)])
		}
		if len(toolCalls) == 0 {
			toolCalls = legacy
		}
		if pytext.Len(clean) > pytext.Len(sent) && strings.HasPrefix(clean, sent) {
			text := pytext.SliceFrom(clean, pytext.Len(sent))
			if sent == "" {
				text = prefix + text
			}
			answer += text
			emit(pyjson.NewObject("type", "content", "text", text))
		}
		if len(toolCalls) == 0 {
			if pytext.Strip(answer) == "" {
				answer = "The model returned no answer. Please try again."
				emit(pyjson.NewObject("type", "content", "text", answer))
			}
			return pyjson.NewObject("message", answer, "reasoning", lastChars(reasoning, 24000), "tools", used,
				"refresh", refresh, "suggestions", suggestions(), "harness", status.Value("label")), nil
		}
		assistantMessage := pyjson.NewObject("role", "assistant", "content", clean, "tool_calls", toolCalls)
		if roundReasoning != "" {
			assistantMessage.Set("reasoning_content", strings.TrimLeft(roundReasoning, "\n"))
		}
		messages = append(messages, assistantMessage)
		for index, call := range toolCalls {
			co := call.(*pyjson.Object)
			fn := co.Value("function")
			if !pyjson.Truthy(fn) {
				fn = pyjson.NewObject()
			}
			fo := obj(fn)
			name := fo.Value("name")
			if !pyjson.Truthy(name) {
				name = ""
			}
			var args any = pyjson.NewObject()
			if a := fo.Value("arguments"); pyjson.Truthy(a) {
				if s, ok := a.(string); ok {
					if v, err := pyjson.Loads(s); err == nil {
						args = v
					}
				}
			}
			if _, ok := args.(*pyjson.Object); !ok {
				args = pyjson.NewObject()
			}
			toolID := co.Value("id")
			if !pyjson.Truthy(toolID) {
				toolID = "tool-" + strconv.Itoa(round) + "-" + strconv.Itoa(index)
			}
			co.Set("id", toolID)
			emit(pyjson.NewObject("type", "tool", "id", toolID, "name", name, "args", args, "state", "running"))
			result, didWrite, err := e.executeTool(name, args)
			if err != nil {
				return nil, err
			}
			refresh = refresh || didWrite
			used = append(used, pyjson.NewObject("name", name, "args", args, "ok", !result.Has("error"), "result", result))
			emit(pyjson.NewObject("type", "tool", "id", toolID, "state", "done", "name", name, "args", args,
				"ok", !result.Has("error"), "result", result))
			messages = append(messages, toolMessage(toolID, result))
		}
	}
	ending := "I reached the tool-call limit before finishing. Please ask me to continue."
	emit(pyjson.NewObject("type", "content", "text", "\n\n"+ending))
	return pyjson.NewObject("message", answer+"\n\n"+ending, "reasoning", lastChars(reasoning, 24000),
		"tools", used, "refresh", refresh, "suggestions", suggestions(), "harness", status.Value("label")), nil
}

// hashKey is a dict key for a decoded JSON value (0 == 0.0 == False).
func hashKey(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "n:1"
		}
		return "n:0"
	case int64:
		return "n:" + strconv.FormatInt(x, 10)
	case float64:
		if x == float64(int64(x)) {
			return "n:" + strconv.FormatInt(int64(x), 10)
		}
		return "f:" + strconv.FormatFloat(x, 'g', -1, 64)
	case string:
		return "s:" + x
	case nil:
		return "none"
	}
	return "x:" + string(pyjson.Dumps(v))
}
