// Package chat is the HTTP side of the Assistant: chat (GET/POST /api/chat,
// /api/chat/status, the NDJSON /api/chat/stream), assistant dispatch, apply
// and extract, and the InfoHub browse/graph/unit/preview/import routes (the
// matching branches of arkstudio/server.py over internal/chat,
// internal/assistant and internal/infohub).  internal/server registers it.
package chat

import (
	"errors"
	"strings"
	"time"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/assistant"
	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/infohub"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/platform/pyerr"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/store"
)

// Handlers answers the Assistant routes for one server.
type Handlers struct {
	// Effective is App.effective_assistant() for the request's viewer.
	Effective func(c api.Ctx) (*pyjson.Object, error)
	// EffectivePublic is App.effective_assistant_public().
	EffectivePublic func(c api.Ctx) (*pyjson.Object, error)
	// AgentToken is the server's agent API token (chat tools call back).
	AgentToken func() string
	// LocalMode is a server without accounts: the local user's thread also
	// holds the pre-v10 rows with an empty user (store.ChatHistory legacy).
	LocalMode bool
}

// orDefault is `value or default`.
func orDefault(v any, def any) any {
	if pyjson.Truthy(v) {
		return v
	}
	return def
}

// chatErr maps the errors of chat.run_chat like the Python callers.
func chatErr(err error) error {
	var ce *assistcfg.Error
	var pe *chat.PyError
	switch {
	case errors.As(err, &ce):
		if ce.Type == "ValueError" {
			return &pyerr.ValueError{Message: ce.Message}
		}
		return &pyerr.InternalError{Message: ce.Message}
	case errors.As(err, &pe):
		if chat.IsValueError(pe) {
			return &pyerr.ValueError{Message: pe.Message}
		}
		return &pyerr.InternalError{Message: pe.Message}
	}
	return err
}

func (h *Handlers) chatGet(c api.Ctx, path string) error {
	if path == "/api/chat/status" {
		status, err := chat.HarnessStatus(nil)
		if err != nil {
			return chatErr(err)
		}
		c.OK(status)
		return nil
	}
	limit := api.QInt(c.Query(), "limit", 100, 1, 400)
	p, err := c.Require("read")
	if err != nil {
		return err
	}
	who := c.Actor()
	messages, err := p.Store.ChatHistory(c.Context(), limit, &who, h.LocalMode)
	if err != nil {
		return err
	}
	list := make([]any, len(messages))
	for i, m := range messages {
		if content := m.Value("content"); pyjson.Truthy(content) {
			m.Set("content", chat.CleanMessage(pyjson.Str(content)))
		}
		list[i] = m
	}
	config, err := h.Effective(c)
	if err != nil {
		return err
	}
	harness, err := chat.HarnessStatus(config)
	if err != nil {
		return chatErr(err)
	}
	public, err := h.EffectivePublic(c)
	if err != nil {
		return err
	}
	summary, err := p.Summary()
	if err != nil {
		return err
	}
	c.OK(pyjson.NewObject("messages", list, "harness", harness, "assistant", public, "project", summary))
	return nil
}

// chatMessage is str(body.get("message") or "").strip() with its checks.
func chatMessage(body *pyjson.Object) (string, error) {
	message := ""
	if v := body.Value("message"); pyjson.Truthy(v) {
		message = pyjson.Str(v)
	}
	message = pytext.Strip(message)
	if message == "" {
		return "", api.Err(400, "message is required", "")
	}
	if pytext.Len(message) > 8000 {
		return "", api.Err(400, "message is too long", "")
	}
	return message, nil
}

func (h *Handlers) chatEnv(c api.Ctx, p *project.Project, body *pyjson.Object, config *pyjson.Object, baseURL string, who string) *chat.Env {
	var hub *pyjson.Object
	if o, ok := body.Value("infohub").(*pyjson.Object); ok {
		hub = o
	}
	return &chat.Env{Ctx: c.RequestContext(), Store: p.Store, ProjectName: p.Name(), ProjectPath: p.Path, Config: config,
		Infohub: hub, AgentToken: h.AgentToken(), BaseURL: baseURL, HistoryUser: &who, HistoryLegacy: h.LocalMode, Who: c.Who(p)}
}

// assistantMeta is the meta of the stored assistant reply.
func assistantMeta(result, config *pyjson.Object) (*pyjson.Object, error) {
	harness := result.Value("harness")
	if !pyjson.Truthy(harness) {
		status, err := chat.HarnessStatus(config)
		if err != nil {
			return nil, err
		}
		harness = status.Value("label")
	}
	sugg := result.Value("suggestions")
	if !pyjson.Truthy(sugg) {
		sugg = []any{}
	}
	reasoning := result.Value("reasoning")
	if !pyjson.Truthy(reasoning) {
		reasoning = ""
	}
	return pyjson.NewObject("harness", harness, "suggestions", sugg, "refresh", pyjson.Truthy(result.Value("refresh")),
		"reasoning", reasoning), nil
}

func toolsOf(result *pyjson.Object) []any {
	if t, ok := result.Value("tools").([]any); ok && len(t) > 0 {
		return t
	}
	return nil
}

// chatSend is App.chat_send (POST /api/chat).
func (h *Handlers) chatSend(c api.Ctx, body *pyjson.Object) error {
	p, err := c.Require("write")
	if err != nil {
		return err
	}
	message, err := chatMessage(body)
	if err != nil {
		return err
	}
	config, err := h.Effective(c)
	if err != nil {
		return err
	}
	who := c.Actor()
	user, err := p.Store.AddChat(c.Context(), "user", message, nil, pyjson.NewObject("source", "ui"), who)
	if err != nil {
		return err
	}
	host := c.Header("Host")
	if host == "" {
		host = "127.0.0.1"
	}
	result, err := h.chatEnv(c, p, body, config, "http://"+host, who).Run(message)
	if err != nil {
		err = chatErr(err)
		var ve *pyerr.ValueError
		var se *store.Error
		if errors.As(err, &ve) {
			return api.Err(400, ve.Message, "")
		}
		if errors.As(err, &se) {
			return api.Err(400, se.Message, "")
		}
		return infohubErr(err, true)
	}
	meta, err := assistantMeta(result, config)
	if err != nil {
		return chatErr(err)
	}
	meta.Set("user", who)
	assistantMsg, err := p.Store.AddChat(c.Context(), "assistant", pyjson.Str(result.Value("message")), toolsOf(result), meta, who)
	if err != nil {
		return err
	}
	harness, err := chat.HarnessStatus(config)
	if err != nil {
		return chatErr(err)
	}
	c.OK(pyjson.NewObject("user", user, "assistant", assistantMsg, "refresh", pyjson.Truthy(result.Value("refresh")),
		"harness", harness))
	return nil
}

// pyStr is str(exc) of an error raised inside the chat stream generator.
func pyStr(err error) string {
	var ae *api.Error
	var se *store.Error
	var ve *pyerr.ValueError
	var ie *pyerr.InternalError
	var oe *org.Error
	var pe *chat.PyError
	var ce *assistcfg.Error
	var sq *infohub.SQLError
	switch {
	case errors.As(err, &ae):
		return ae.Message
	case errors.As(err, &se):
		return se.Message
	case errors.As(err, &ve):
		return ve.Message
	case errors.As(err, &ie):
		return ie.Message
	case errors.As(err, &oe):
		return oe.Message
	case errors.As(err, &pe):
		return pe.Message
	case errors.As(err, &ce):
		return ce.Message
	case errors.As(err, &sq):
		return sq.Message
	}
	return err.Error()
}

// Stream is Handler._stream_chat + App.chat_stream (POST /api/chat/stream).
func (h *Handlers) Stream(c api.Ctx) error {
	body, err := c.ReadJSON()
	if err != nil {
		return err
	}
	host := c.Header("Host")
	baseURL := ""
	if host != "" {
		baseURL = "http://" + host
	}
	c.DetachFromRequestLimits()
	st := c.StartRawStream([][2]string{
		{"Content-Type", "application/x-ndjson; charset=utf-8"},
		{"Cache-Control", "no-store"},
		{"X-Accel-Buffering", "no"},
		{"X-Content-Type-Options", "nosniff"},
		{"Connection", "close"},
	})
	failed := false
	writeLine := func(line []byte) {
		if failed {
			return
		}
		st.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
		if _, err := st.Write(append(line, '\n')); err != nil {
			failed = true
			return
		}
		if st.Flush() != nil {
			failed = true
		}
	}
	emit := func(event *pyjson.Object) { writeLine(pyjson.Dumps(event)) }
	if err := h.chatStreamEvents(c, body, baseURL, emit); err != nil && !failed {
		writeLine(pyjson.DumpsASCII(pyjson.NewObject("type", "error", "message", pyStr(err))))
	}
	return nil
}

func (h *Handlers) chatStreamEvents(c api.Ctx, body *pyjson.Object, baseURL string, emit func(*pyjson.Object)) error {
	p, err := c.Require("write")
	if err != nil {
		return err
	}
	message, err := chatMessage(body)
	if err != nil {
		return err
	}
	config, err := h.Effective(c)
	if err != nil {
		return err
	}
	who := c.Actor()
	user, err := p.Store.AddChat(c.Context(), "user", message, nil, pyjson.NewObject("source", "ui", "stream", true), who)
	if err != nil {
		return err
	}
	emit(pyjson.NewObject("type", "user", "message", user))
	result, err := h.chatEnv(c, p, body, config, baseURL, who).Stream(message, emit)
	if err != nil {
		if mapped := chatErr(err); isValueOrStore(mapped) {
			emit(pyjson.NewObject("type", "error", "message", pyStr(mapped)))
			return nil
		}
		return err
	}
	meta, err := assistantMeta(result, config)
	if err != nil {
		return err
	}
	meta.Set("stream", true)
	answer := ""
	if v := result.Value("message"); pyjson.Truthy(v) {
		answer = pyjson.Str(v)
	}
	meta.Set("reasoning", orEmptyStr(result.Value("reasoning")))
	assistantMsg, err := p.Store.AddChat(c.Context(), "assistant", answer, toolsOf(result), meta, who)
	if err != nil {
		return err
	}
	harness, err := chat.HarnessStatus(config)
	if err != nil {
		return err
	}
	emit(pyjson.NewObject("type", "done", "user", user, "assistant", assistantMsg, "harness", harness,
		"refresh", pyjson.Truthy(result.Value("refresh"))))
	return nil
}

func orEmptyStr(v any) string {
	if pyjson.Truthy(v) {
		return pyjson.Str(v)
	}
	return ""
}

func isValueOrStore(err error) bool {
	var ve *pyerr.ValueError
	var se *store.Error
	return errors.As(err, &ve) || errors.As(err, &se)
}

func infohubErr(err error, post bool) error {
	var se *infohub.SQLError
	if !errors.As(err, &se) {
		return err
	}
	if post {
		return api.Err(500, "Database error: "+se.Message, "")
	}
	return &pyerr.InternalError{Message: se.Message}
}

// firstInfohubPath is Handler._first_infohub_path.
func firstInfohubPath() (string, error) {
	candidates := infohub.Candidates()
	if len(candidates) == 0 {
		return "", api.Err(400, "No InfoHub database found; set INFOHUB_DB or pass path", "")
	}
	return pyjson.Str(candidates[0].(*pyjson.Object).Value("path")), nil
}

func pyBoolQuery(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func infohubGet(c api.Ctx, path string) error {
	q := c.Query()
	sourcePath := q.First("path", "")
	if sourcePath == "" {
		var err error
		if sourcePath, err = firstInfohubPath(); err != nil {
			return err
		}
	}
	key := q.First("key", "")
	if _, ok := q["key"]; !ok {
		key = q.First("id", "")
	}
	if path == "/api/infohub/unit" && key == "" {
		return api.Err(400, "key is required", "")
	}
	src, err := infohub.Open(sourcePath)
	if err != nil {
		return err
	}
	defer src.Close()
	p, err := c.Require("read")
	if err != nil {
		return err
	}
	if path == "/api/infohub/unit" {
		unit, err := src.Unit(c.Context(), key, p.Store)
		if err == nil {
			c.OK(pyjson.NewObject("unit", unit))
		}
		return err
	}
	limitDef := int64(60)
	if path == "/api/infohub/graph" {
		limitDef = 160
	}
	f := infohub.Filter{
		Q: q.First("q", ""), Kind: q.First("kind", ""), Tags: q.First("tags", ""), Status: q.First("status", ""),
		Days: infohub.FloatArg(q.First("days", "0"), 0, 0, 36500), Mine: pyBoolQuery(q.First("mine", "0")),
		Roots: pyBoolQuery(q.First("roots", "0")), Order: q.First("order", "activity"),
		Limit: api.QInt(c.Query(), "limit", limitDef, 1, 1000),
	}
	var out *pyjson.Object
	if path == "/api/infohub/browse" {
		f.Offset = api.QInt(c.Query(), "offset", 0, 0, 100_000)
		out, err = src.Browse(c.Context(), p.Store, f)
	} else {
		out, err = src.Graph(c.Context(), p.Store, f, pyBoolQuery(q.First("connected", "1")), pyBoolQuery(q.First("context", "0")))
	}
	if err == nil {
		c.OK(out)
	}
	return err
}

// pathArg is Path(value) for a JSON value (str only).
func pathArg(v any) (string, error) {
	if s, ok := v.(string); ok {
		return s, nil
	}
	return "", &pyerr.InternalError{Message: "expected str, bytes or os.PathLike object, not " + pyerr.TypeName(v)}
}

func infohubPost(c api.Ctx, path string, body *pyjson.Object, actor string) error {
	p, err := c.Require("read")
	if err != nil {
		return err
	}
	if path == "/api/import/infohub" {
		sp, err := pathArg(orDefault(body.Value("path"), ""))
		if err != nil {
			return err
		}
		src, err := infohub.Open(sp)
		if err != nil {
			return err
		}
		defer src.Close()
		mode := orDefault(body.Value("mode"), "topics")
		switch mode.(type) {
		case []any, *pyjson.Object:
			return &pyerr.InternalError{Message: "unhashable type: '" + pyerr.TypeName(mode) + "'"}
		}
		m, _ := mode.(string) // other values are not "topics" or "all"
		out, err := src.ImportInto(c.Context(), p.Store, m, pyjson.Truthy(body.Value("update")), actor, c.Who(p))
		if err == nil {
			c.OK(out)
		}
		return err
	}
	raw := orDefault(body.Value("path"), nil)
	var sp string
	if raw == nil {
		if sp, err = firstInfohubPath(); err != nil {
			return err
		}
	} else if sp, err = pathArg(raw); err != nil {
		return err
	}
	src, err := infohub.Open(sp)
	if err != nil {
		return err
	}
	defer src.Close()
	out, err := src.ImportKey(c.Context(), p.Store, pyjson.Str(orDefault(body.Value("key"), "")), actor,
		pyjson.Truthy(body.Value("as_idea")), pyjson.Truthy(body.Value("copy")), c.Who(p))
	if err == nil {
		c.OK(out)
	}
	return err
}

// servicesPost answers the services POST routes (Handler.do_POST, after the
// JSON body was read).

// Get answers the chat, assistant and InfoHub GET routes (servicesGet).
func (h *Handlers) Get(c api.Ctx, path string) (bool, error) {
	switch path {
	case "/api/chat", "/api/chat/status":
		return true, h.chatGet(c, path)
	case "/api/assistant/dispatch":
		task := c.Query().First("task", "")
		if task == "" {
			return true, api.Err(400, "task is required", "")
		}
		p, err := c.Require("read")
		if err != nil {
			return true, err
		}
		out, err := assistant.Dispatch(c.Context(), p.Store, task)
		if err == nil {
			c.OK(out)
		}
		return true, err
	case "/api/infohub/candidates", "/api/import/infohub/sources":
		name := "candidates"
		if path == "/api/import/infohub/sources" {
			name = "sources"
		}
		c.OK(pyjson.NewObject(name, infohub.Candidates()))
		return true, nil
	case "/api/import/infohub/preview":
		src := c.Query().First("path", "")
		if src == "" {
			return true, api.Err(400, "path is required", "")
		}
		out, err := infohub.Preview(c.Context(), src)
		if err == nil {
			c.OK(out)
		}
		return true, infohubErr(err, false)
	case "/api/infohub/browse", "/api/infohub/graph", "/api/infohub/unit":
		return true, infohubErr(infohubGet(c, path), false)
	}
	return false, nil
}

// Post answers the chat, assistant and InfoHub POST routes (servicesPost).
func (h *Handlers) Post(c api.Ctx, path string, body *pyjson.Object, actor string) (bool, error) {
	switch path {
	case "/api/assistant/apply", "/api/assistant/extract":
		p, err := c.Require("read")
		if err != nil {
			return true, err
		}
		var out *pyjson.Object
		if path == "/api/assistant/apply" {
			out, err = assistant.Apply(c.Context(), p.Store, orDefault(body.Value("insight"), pyjson.NewObject()), actor)
		} else {
			out, err = assistant.Extract(c.Context(), p.Store, orDefault(body.Value("text"), ""), body.Value("source"),
				orDefault(body.Value("limit"), int64(24)), actor, c.Who(p))
		}
		if err == nil {
			c.OK(out)
		}
		return true, err
	case "/api/chat":
		return true, h.chatSend(c, body)
	case "/api/import/infohub", "/api/infohub/import":
		return true, infohubErr(infohubPost(c, path, body, actor), true)
	}
	return false, nil
}
