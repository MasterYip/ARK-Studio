// Package api is the core of the HTTP layer that domain handler packages
// (internal/api/<domain>) build on: the request context they see (Ctx), the
// error type the pipeline maps to Python's ApiError replies, and the small
// request-parsing helpers several domains share.
//
// The request pipeline itself (Host allow-list, sign-in, device gate, project
// scope, same-origin, error mapping, router) still lives in internal/server,
// which implements Ctx and registers the handler packages; it moves here in
// milestone 3 (architecture.md).  Handler packages import api, domains,
// platform and compat, never internal/server.
package api

import (
	"context"
	"regexp"
	"strings"

	"arkstudio/go/internal/compat/pyjson"
	"arkstudio/go/internal/compat/pytext"
	"arkstudio/go/internal/compat/pyurl"
	"arkstudio/go/internal/project"
	"arkstudio/go/internal/sessions"
	"arkstudio/go/internal/store"
)

// Ctx is one request as a handler package sees it.
type Ctx interface {
	Context() context.Context
	// Query is the parsed query string (parse_qs, blank values dropped).
	Query() pyurl.Values
	// OK sends a 200 JSON reply.
	OK(data *pyjson.Object)
	// Require is Handler._require_project(level): the request's project when
	// the viewer has the permission ("read", "write", "admin").
	Require(level string) (*project.Project, error)
	// Actor is the signed-in user name ("" in local mode).
	Actor() string
	// ReadJSON reads the request body as a JSON object (_read_json).
	ReadJSON() (*pyjson.Object, error)
	// RequireTaskAgent is _require_task_agent: the bound task-agent session.
	RequireTaskAgent() (*sessions.Session, error)
	// BindTaskSession makes the rest of the request act for the session.
	BindTaskSession(s *sessions.Session)

	// Header is a request header value (ISO-8859-1 decoded like http.server).
	Header(name string) string
	// RequestContext is the client connection's context (done when the
	// client leaves); unlike Context it carries no request timeout.
	RequestContext() context.Context
	// DetachFromRequestLimits lets a long-lived stream leave the admission
	// pool; it is then bounded by its own deadlines, the client and shutdown.
	DetachFromRequestLimits()
	// StartStream sends a 200 with this Content-Type, the security headers
	// and the extra headers (in order), and returns the body stream.
	StartStream(contentType string, extra [][2]string) Stream
	// StartRawStream sends a 200 with exactly these headers (no security
	// headers; Handler._stream_terminal writes its own).
	StartRawStream(headers [][2]string) Stream
	// SendJSON sends a JSON reply with this status and extra headers.
	SendJSON(status int, value any, headers [][2]string)
	// Send sends a reply body with this status, type and extra headers.
	Send(status int, body []byte, contentType string, headers [][2]string)
	// SendFile is Handler._send_file (one byte range at most).
	SendFile(path, contentType string, download bool) error
	// RemoteAddr is the peer address ("host:port").
	RemoteAddr() string
	// RejectTaskAgentReadToken is Handler._reject_task_agent_read_token:
	// a 403 with message when the bearer token is a task-agent read token.
	RejectTaskAgentReadToken(message string) error
	// TaskAgentReadTokenUsed reports whether the read token or the agent
	// bearer token is a task-agent read token (App.open_external's check).
	TaskAgentReadTokenUsed() bool
	// Who resolves assignees and names the creator of writes to p.
	Who(p *project.Project) store.Who
}

// Error is server.ApiError: a reply with this status, message and code.
// Status 302 means "redirect to Message".
type Error struct {
	Status        int
	Message, Code string
}

func (e *Error) Error() string { return e.Message }

// Err builds an *Error.
func Err(status int, message, code string) *Error {
	return &Error{Status: status, Message: message, Code: code}
}

// PyRe compiles a Python re pattern used with re.match/fullmatch on a path:
// Python \d is Unicode, and a trailing `$` also matches before a final "\n".
func PyRe(pattern string, full bool) *regexp.Regexp {
	p := strings.TrimPrefix(pattern, "^")
	if strings.HasSuffix(p, "$") && !strings.HasSuffix(p, `\$`) {
		p = strings.TrimSuffix(p, "$") + `\n?\z`
	} else if full {
		p += `\z`
	}
	return pytext.MustCompile(`\A` + p)
}

// QList is _qlist(query, name, alias): comma-separated values of the named
// parameters, stripped, blanks and repeats dropped, in first-seen order.
func QList(q pyurl.Values, names ...string) []any {
	var out []any
	seen := map[string]bool{}
	for _, key := range names {
		for _, raw := range q[key] {
			for _, part := range strings.Split(raw, ",") {
				if part = pytext.Strip(part); part != "" && !seen[part] {
					seen[part] = true
					out = append(out, part)
				}
			}
		}
	}
	return out
}

// StrOr is `str(value or "")`.
func StrOr(v any) string {
	if !pyjson.Truthy(v) {
		return ""
	}
	return pyjson.Str(v)
}
