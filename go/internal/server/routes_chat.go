package server

// Assistant chat (App.chat_state / chat_send / chat_stream, chat.py).

import (
	"errors"
	"net/http"
	"time"

	"arkstudio/go/internal/assistcfg"
	"arkstudio/go/internal/chat"
	"arkstudio/go/internal/infohub"
	"arkstudio/go/internal/org"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/pyjson"
	"arkstudio/go/internal/pytext"
	"arkstudio/go/internal/store"
)

var chatPorted = []PortedRoute{
	{"exact:/api/chat", "GET,HEAD"},
	{"exact:/api/chat", "POST"},
	{"exact:/api/chat/status", "GET,HEAD"},
	{"exact:/api/chat/stream", "POST"},
}

func init() { Ported = append(Ported, chatPorted...) }

// effectiveAssistant is App.effective_assistant().
func (c *reqCtx) effectiveAssistant() (*pyjson.Object, error) {
	summary := pyjson.NewObject()
	p, err := c.currentProject()
	if err != nil {
		return nil, err
	}
	if p != nil {
		s, err := p.Summary()
		if err != nil {
			return nil, err
		}
		summary = s
	}
	global, err := assistcfg.Load()
	if err != nil {
		return nil, err
	}
	return c.app.Org.ResolveAssistant(c.ctx, summary, c.actor(), global)
}

// effectiveAssistantPublic is App.effective_assistant_public().
func (c *reqCtx) effectiveAssistantPublic() (*pyjson.Object, error) {
	resolved, err := c.effectiveAssistant()
	if err != nil {
		return nil, err
	}
	key := pyjson.Str(resolved.Value("api_key"))
	resolved.Delete("api_key")
	resolved.Set("configured", key != "")
	resolved.Set("has_key", key != "")
	hint := resolved.Value("key_hint")
	if !pyjson.Truthy(hint) {
		hint = ""
	}
	resolved.Set("key_hint", hint)
	return resolved, nil
}

// chatErr maps the errors of chat.run_chat like the Python callers.
func chatErr(err error) error {
	var ce *assistcfg.Error
	var pe *chat.PyError
	switch {
	case errors.As(err, &ce):
		if ce.Type == "ValueError" {
			return &store.ValueError{Message: ce.Message}
		}
		return &store.InternalError{Message: ce.Message}
	case errors.As(err, &pe):
		if chat.IsValueError(pe) {
			return &store.ValueError{Message: pe.Message}
		}
		return &store.InternalError{Message: pe.Message}
	}
	return err
}

func (c *reqCtx) chatGet(path string) error {
	if path == "/api/chat/status" {
		status, err := chat.HarnessStatus(nil)
		if err != nil {
			return chatErr(err)
		}
		c.ok(status)
		return nil
	}
	limit := c.qInt("limit", 100, 1, 400)
	p, err := c.require("read")
	if err != nil {
		return err
	}
	who := c.actor()
	messages, err := p.Store.ChatHistory(c.ctx, limit, &who)
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
	config, err := c.effectiveAssistant()
	if err != nil {
		return err
	}
	harness, err := chat.HarnessStatus(config)
	if err != nil {
		return chatErr(err)
	}
	public, err := c.effectiveAssistantPublic()
	if err != nil {
		return err
	}
	summary, err := p.Summary()
	if err != nil {
		return err
	}
	c.ok(pyjson.NewObject("messages", list, "harness", harness, "assistant", public, "project", summary))
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
		return "", apiErr(400, "message is required", "")
	}
	if pytext.Len(message) > 8000 {
		return "", apiErr(400, "message is too long", "")
	}
	return message, nil
}

func (c *reqCtx) chatEnv(p *project.Project, body *pyjson.Object, config *pyjson.Object, baseURL string, who string) *chat.Env {
	var hub *pyjson.Object
	if o, ok := body.Value("infohub").(*pyjson.Object); ok {
		hub = o
	}
	return &chat.Env{Ctx: c.r.Context(), Store: p.Store, ProjectName: p.Name(), ProjectPath: p.Path, Config: config,
		Infohub: hub, AgentToken: c.app.AgentToken(), BaseURL: baseURL, HistoryUser: &who, Who: c.who(p)}
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
func (c *reqCtx) chatSend(body *pyjson.Object) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	message, err := chatMessage(body)
	if err != nil {
		return err
	}
	config, err := c.effectiveAssistant()
	if err != nil {
		return err
	}
	who := c.actor()
	user, err := p.Store.AddChat(c.ctx, "user", message, nil, pyjson.NewObject("source", "ui"), who)
	if err != nil {
		return err
	}
	host := c.header("Host")
	if host == "" {
		host = "127.0.0.1"
	}
	result, err := c.chatEnv(p, body, config, "http://"+host, who).Run(message)
	if err != nil {
		err = chatErr(err)
		var ve *store.ValueError
		var se *store.Error
		if errors.As(err, &ve) {
			return apiErr(400, ve.Message, "")
		}
		if errors.As(err, &se) {
			return apiErr(400, se.Message, "")
		}
		return infohubErr(err, true)
	}
	meta, err := assistantMeta(result, config)
	if err != nil {
		return chatErr(err)
	}
	meta.Set("user", who)
	assistantMsg, err := p.Store.AddChat(c.ctx, "assistant", pyjson.Str(result.Value("message")), toolsOf(result), meta, who)
	if err != nil {
		return err
	}
	harness, err := chat.HarnessStatus(config)
	if err != nil {
		return chatErr(err)
	}
	c.ok(pyjson.NewObject("user", user, "assistant", assistantMsg, "refresh", pyjson.Truthy(result.Value("refresh")),
		"harness", harness))
	return nil
}

// pyStr is str(exc) of an error raised inside the chat stream generator.
func pyStr(err error) string {
	var api *ApiError
	var se *store.Error
	var ve *store.ValueError
	var ie *store.InternalError
	var oe *org.Error
	var pe *chat.PyError
	var ce *assistcfg.Error
	var sq *infohub.SQLError
	switch {
	case errors.As(err, &api):
		return api.Message
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

// chatStream is Handler._stream_chat + App.chat_stream (POST /api/chat/stream).
func (c *reqCtx) chatStream() error {
	body, err := c.readJSON()
	if err != nil {
		return err
	}
	host := c.header("Host")
	baseURL := ""
	if host != "" {
		baseURL = "http://" + host
	}
	c.detachFromRequestLimits()
	h := c.w.Header()
	h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Connection", "close")
	c.w.WriteHeader(200)
	rc := http.NewResponseController(c.w)
	failed := false
	writeLine := func(line []byte) {
		if failed {
			return
		}
		rc.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
		if _, err := c.w.Write(append(line, '\n')); err != nil {
			failed = true
			return
		}
		if rc.Flush() != nil {
			failed = true
		}
	}
	emit := func(event *pyjson.Object) { writeLine(pyjson.Dumps(event)) }
	if err := c.chatStreamEvents(body, baseURL, emit); err != nil && !failed {
		writeLine(pyjson.DumpsASCII(pyjson.NewObject("type", "error", "message", pyStr(err))))
	}
	return nil
}

func (c *reqCtx) chatStreamEvents(body *pyjson.Object, baseURL string, emit func(*pyjson.Object)) error {
	p, err := c.require("write")
	if err != nil {
		return err
	}
	message, err := chatMessage(body)
	if err != nil {
		return err
	}
	config, err := c.effectiveAssistant()
	if err != nil {
		return err
	}
	who := c.actor()
	user, err := p.Store.AddChat(c.ctx, "user", message, nil, pyjson.NewObject("source", "ui", "stream", true), who)
	if err != nil {
		return err
	}
	emit(pyjson.NewObject("type", "user", "message", user))
	result, err := c.chatEnv(p, body, config, baseURL, who).Stream(message, emit)
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
	assistantMsg, err := p.Store.AddChat(c.ctx, "assistant", answer, toolsOf(result), meta, who)
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
	var ve *store.ValueError
	var se *store.Error
	return errors.As(err, &ve) || errors.As(err, &se)
}
