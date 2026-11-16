package server

import (
	"context"
	"net/http"
	"time"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

// reqCtx implements api.Ctx for the handler packages (internal/api/<domain>).
var _ api.Ctx = (*reqCtx)(nil)

func (c *reqCtx) Context() context.Context                       { return c.ctx }
func (c *reqCtx) Query() pyurl.Values                            { return c.query }
func (c *reqCtx) OK(data *pyjson.Object)                         { c.ok(data) }
func (c *reqCtx) Require(level string) (*project.Project, error) { return c.require(level) }
func (c *reqCtx) Actor() string                                  { return c.actor() }
func (c *reqCtx) ReadJSON() (*pyjson.Object, error)              { return c.readJSON() }
func (c *reqCtx) RequireTaskAgent() (*sessions.Session, error)   { return c.requireTaskAgent() }
func (c *reqCtx) BindTaskSession(s *sessions.Session)            { c.bindTaskSession(s) }

func (c *reqCtx) Header(name string) string       { return c.header(name) }
func (c *reqCtx) RequestContext() context.Context { return c.r.Context() }
func (c *reqCtx) DetachFromRequestLimits()        { c.detachFromRequestLimits() }

// StartStream sends the headers of a streamed 200 reply.
func (c *reqCtx) StartStream(contentType string, extra [][2]string) api.Stream {
	h := c.w.Header()
	h.Set("Content-Type", contentType)
	securityHeaders(h, headerList(extra))
	c.w.WriteHeader(200)
	return &replyStream{w: c.w, rc: http.NewResponseController(c.w)}
}

type replyStream struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (s *replyStream) Write(p []byte) (int, error)        { return s.w.Write(p) }
func (s *replyStream) Flush() error                       { return s.rc.Flush() }
func (s *replyStream) SetWriteDeadline(t time.Time) error { return s.rc.SetWriteDeadline(t) }

// StartRawStream sends exactly these headers with a 200.
func (c *reqCtx) StartRawStream(headers [][2]string) api.Stream {
	h := c.w.Header()
	for _, kv := range headers {
		h.Set(kv[0], kv[1])
	}
	c.w.WriteHeader(200)
	return &replyStream{w: c.w, rc: http.NewResponseController(c.w)}
}

func (c *reqCtx) SendJSON(status int, value any, headers [][2]string) {
	c.sendJSON(status, value, headerList(headers))
}

func (c *reqCtx) Send(status int, body []byte, contentType string, headers [][2]string) {
	c.send(status, body, contentType, headerList(headers), true)
}

func (c *reqCtx) SendFile(path, contentType string, download bool) error {
	return c.sendFile(path, contentType, download)
}

func (c *reqCtx) RemoteAddr() string { return c.r.RemoteAddr }

func (c *reqCtx) RejectTaskAgentReadToken(message string) error {
	return c.rejectTaskAgentReadToken(message)
}

func (c *reqCtx) TaskAgentReadTokenUsed() bool {
	for _, candidate := range []string{c.readToken(), c.agentToken()} {
		if candidate != "" && c.app.Sessions != nil && c.app.Sessions.ForReadToken(candidate) != nil {
			return true
		}
	}
	return false
}

func (c *reqCtx) Who(p *project.Project) store.Who { return c.who(p) }
