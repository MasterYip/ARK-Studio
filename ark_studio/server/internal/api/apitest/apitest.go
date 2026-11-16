// Package apitest provides a fake api.Ctx for the handler packages' tests.
package apitest

import (
	"bytes"
	"context"
	"time"

	"arkstudio/go/internal/api"
	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

// Ctx is an api.Ctx over plain fields: a query, headers, a body, a project
// (or the error Require returns) and the captured replies.  Embed it in a
// test type to override single methods.
type Ctx struct {
	Ctx        context.Context
	Q          pyurl.Values
	Headers    map[string]string
	Body       *pyjson.Object
	BodyErr    error
	Project    *project.Project
	RequireErr error
	User       string
	Session    *sessions.Session
	SessionErr error

	Sent         *pyjson.Object    // the last OK reply
	Level        string            // the last Require level
	Levels       []string          // every Require level, in order
	Bound        *sessions.Session // BindTaskSession
	Detached     bool              // DetachFromRequestLimits was called
	StreamType   string            // StartStream content type
	StreamExtra  [][2]string       // StartStream extra headers
	StreamBody   bytes.Buffer      // the streamed bytes
	StreamFlushs int               // Flush calls
	Replies      []Reply           // SendJSON / Send / SendFile calls
	Peer         string            // RemoteAddr (default 127.0.0.1:50000)
	ReadToken    bool              // the bearer token is a task-agent read token
}

var _ api.Ctx = (*Ctx)(nil)

func (f *Ctx) Context() context.Context {
	if f.Ctx != nil {
		return f.Ctx
	}
	return context.Background()
}
func (f *Ctx) Query() pyurl.Values {
	if f.Q == nil {
		return pyurl.Values{}
	}
	return f.Q
}
func (f *Ctx) OK(d *pyjson.Object) { f.Sent = d }
func (f *Ctx) Require(level string) (*project.Project, error) {
	f.Level = level
	f.Levels = append(f.Levels, level)
	if f.RequireErr != nil {
		return nil, f.RequireErr
	}
	if f.Project == nil {
		return nil, api.Err(403, "forbidden", "")
	}
	return f.Project, nil
}
func (f *Ctx) Actor() string { return f.User }
func (f *Ctx) ReadJSON() (*pyjson.Object, error) {
	if f.BodyErr != nil {
		return nil, f.BodyErr
	}
	if f.Body == nil {
		return pyjson.NewObject(), nil
	}
	return f.Body, nil
}
func (f *Ctx) RequireTaskAgent() (*sessions.Session, error) {
	if f.SessionErr != nil {
		return nil, f.SessionErr
	}
	return f.Session, nil
}
func (f *Ctx) BindTaskSession(s *sessions.Session) { f.Bound = s }
func (f *Ctx) Header(name string) string           { return f.Headers[name] }
func (f *Ctx) RequestContext() context.Context     { return f.Context() }
func (f *Ctx) DetachFromRequestLimits()            { f.Detached = true }
func (f *Ctx) StartStream(contentType string, extra [][2]string) api.Stream {
	f.StreamType, f.StreamExtra = contentType, extra
	return (*stream)(f)
}

type stream Ctx

func (s *stream) Write(p []byte) (int, error)      { return s.StreamBody.Write(p) }
func (s *stream) Flush() error                     { s.StreamFlushs++; return nil }
func (s *stream) SetWriteDeadline(time.Time) error { return nil }

// Reply is one SendJSON/Send call.
type Reply struct {
	Status      int
	Value       any
	Body        []byte
	ContentType string
	Headers     [][2]string
}

func (f *Ctx) StartRawStream(headers [][2]string) api.Stream {
	f.StreamExtra = headers
	return (*stream)(f)
}
func (f *Ctx) SendJSON(status int, value any, headers [][2]string) {
	f.Replies = append(f.Replies, Reply{Status: status, Value: value, Headers: headers})
}
func (f *Ctx) Send(status int, body []byte, contentType string, headers [][2]string) {
	f.Replies = append(f.Replies, Reply{Status: status, Body: body, ContentType: contentType, Headers: headers})
}
func (f *Ctx) SendFile(path, contentType string, download bool) error {
	f.Replies = append(f.Replies, Reply{Status: 200, Value: path, ContentType: contentType, Body: []byte(map[bool]string{true: "download"}[download])})
	return nil
}
func (f *Ctx) RemoteAddr() string {
	if f.Peer == "" {
		return "127.0.0.1:50000"
	}
	return f.Peer
}
func (f *Ctx) RejectTaskAgentReadToken(message string) error {
	if f.ReadToken {
		return api.Err(403, message, "")
	}
	return nil
}
func (f *Ctx) TaskAgentReadTokenUsed() bool { return f.ReadToken }

func (f *Ctx) Who(*project.Project) store.Who { return store.Who{Creator: f.Actor} }
